package providers

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/requests"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/bssopenapi"
	"mcpcloud/internal/config"
	"mcpcloud/internal/provider"
)

func dailyResponse(day string) *bssopenapi.QueryInstanceBillResponse {
	r := bssopenapi.CreateQueryInstanceBillResponse()
	r.Success = true
	r.Data.PageNum = 1
	r.Data.PageSize = 300
	r.Data.TotalCount = 2
	r.Data.AccountID = "123"
	r.Data.Items.Item = []bssopenapi.Item{{BillingDate: day, OwnerID: "123", Currency: "CNY", Region: "华东2（上海）", PretaxAmount: 1.25}, {BillingDate: day, OwnerID: "999", Currency: "CNY", PretaxAmount: 100}}
	return r
}
func TestAlibabaDailyCostsRequestsAndScope(t *testing.T) {
	t.Setenv("ALIBABA_CLOUD_ACCESS_KEY_ID", "test")
	t.Setenv("ALIBABA_CLOUD_ACCESS_KEY_SECRET", "test")
	t.Setenv("ALIBABA_CLOUD_SECURITY_TOKEN", "")
	old := queryAlibabaDailyBill
	t.Cleanup(func() { queryAlibabaDailyBill = old })
	calls := 0
	queryAlibabaDailyBill = func(_ *bssopenapi.Client, r *bssopenapi.QueryInstanceBillRequest) (*bssopenapi.QueryInstanceBillResponse, error) {
		calls++
		if e := requests.InitParams(r); e != nil {
			t.Fatal(e)
		}
		q := r.GetQueryParams()
		if q["OwnerId"] != "" || q["BillOwnerId"] != "123" || q["Granularity"] != "DAILY" || q["BillingDate"] != "2026-09-03" || q["BillingCycle"] != "2026-09" || q["PageSize"] != "300" || q["PageNum"] != "1" {
			t.Fatalf("request=%v", q)
		}
		return dailyResponse("2026-09-03"), nil
	}
	a := &alibabaAdapter{name: "test", profile: envCredentialProfile(nil)}
	a.profile.Scopes.Accounts = []string{"123"}
	a.profile.Regions = []string{"cn-shanghai"}
	start := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 2)
	p, e := a.queryCosts(context.Background(), provider.QueryRequest{Start: &start, End: &end, Limit: 2})
	if e != nil {
		t.Fatal(e)
	}
	if calls != 1 || len(p.Rows) != 1 || p.Rows[0]["region"] != "cn-shanghai" || p.Rows[0]["amount"] != 1.25 {
		t.Fatalf("page=%+v", p)
	}
	c, _ := decodePeriodCursor(p.NextToken)
	if c.Period != "2026-09-04" || c.Page != 1 {
		t.Fatalf("cursor=%+v", c)
	}
}
func TestAlibabaDailyCostsBoundaries(t *testing.T) {
	day := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	end := day.AddDate(0, 0, 2)
	a := &alibabaAdapter{profile: config.Profile{Regions: []string{"cn-shanghai"}}}
	r := dailyResponse("2026-09-30")
	r.Data.TotalCount = 301
	r.Data.Items.Item = make([]bssopenapi.Item, 300)
	for i := range r.Data.Items.Item {
		r.Data.Items.Item[i] = bssopenapi.Item{BillingDate: "2026-09-30", OwnerID: "123", Currency: "CNY", PretaxAmount: float64(i + 1)}
	}
	p, e := a.alibabaDailyPage(r, []string{"123"}, day, end, periodCursor{Period: "2026-09-30", Page: 1}, 2)
	if e != nil {
		t.Fatal(e)
	}
	c, _ := decodePeriodCursor(p.NextToken)
	if c.Page != 1 || c.Offset != 2 || c.Total != 301 || c.Period != "2026-09-30" {
		t.Fatal(c)
	}
	p, e = a.alibabaDailyPage(r, []string{"123"}, day, end, c, 2)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Rows) != 2 || p.Rows[0]["amount"] == 1.25 || p.Rows[0]["amount"] != 3.0 {
		t.Fatalf("second local slice=%+v", p.Rows)
	}
	r.Data.PageNum = 2
	r.Data.Items.Item = []bssopenapi.Item{{BillingDate: "2026-09-30", OwnerID: "123", Currency: "CNY", PretaxAmount: 301}}
	p, e = a.alibabaDailyPage(r, []string{"123"}, day, end, periodCursor{Period: "2026-09-30", Page: 2, Total: 301}, 2)
	if e != nil {
		t.Fatal(e)
	}
	c, _ = decodePeriodCursor(p.NextToken)
	if c.Period != "2026-10-01" {
		t.Fatal(c)
	}
	for _, mutate := range []func(*bssopenapi.QueryInstanceBillResponse){func(r *bssopenapi.QueryInstanceBillResponse) { r.Data.Items.Item[0].BillingDate = "" }, func(r *bssopenapi.QueryInstanceBillResponse) { r.Data.Items.Item[0].Currency = "" }, func(r *bssopenapi.QueryInstanceBillResponse) { r.Data.TotalCount = 50001 }, func(r *bssopenapi.QueryInstanceBillResponse) { r.Data.Items.Item = nil }} {
		r := dailyResponse("2026-09-30")
		mutate(r)
		if _, e := a.alibabaDailyPage(r, []string{"123"}, day, end, periodCursor{Period: "2026-09-30", Page: 1}, 2); !hasProviderErrorCode(e, "invalid_provider_response") {
			t.Fatalf("error=%v", e)
		}
	}
	r = dailyResponse("2026-09-30")
	r.Data.Items.Item[0].Region = "未知区域"
	p, e = a.alibabaDailyPage(r, []string{"123"}, day, end, periodCursor{Period: "2026-09-30", Page: 1}, 2)
	if e != nil || len(p.Rows) != 0 {
		t.Fatalf("unknown region must not bypass scope: %+v %v", p, e)
	}
	for _, token := range []string{encodePeriodCursor(periodCursor{Period: "2026-09", Page: 1}), encodePeriodCursor(periodCursor{Period: "2026-09-29", Page: 1}), encodePeriodCursor(periodCursor{Period: "2026-10-02", Page: 1})} {
		if _, _, e := alibabaDailyPosition(day, end, token); !hasProviderErrorCode(e, "invalid_cursor") {
			t.Fatal(e)
		}
	}
	d, _, e := alibabaDailyPosition(day.Add(time.Hour), end, "")
	if e != nil || d.Format("2006-01-02") != "2026-10-01" {
		t.Fatalf("partial day=%v %v", d, e)
	}
}

func TestAlibabaDailyCostsAllowChangingTotals(t *testing.T) {
	day := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	end := day.AddDate(0, 0, 1)
	a := &alibabaAdapter{}
	for _, total := range []int{3, 1, 0} {
		t.Run(strconv.Itoa(total), func(t *testing.T) {
			r := dailyResponse("2026-09-30")
			r.Data.TotalCount = total
			r.Data.Items.Item = make([]bssopenapi.Item, total)
			for i := range r.Data.Items.Item {
				r.Data.Items.Item[i] = bssopenapi.Item{BillingDate: "2026-09-30", OwnerID: "123", Currency: "CNY", PretaxAmount: float64(i + 1)}
			}
			page, err := a.alibabaDailyPage(r, []string{"123"}, day, end, periodCursor{Page: 1, Offset: 1, Total: 2}, 300)
			if err != nil || len(page.Rows) != max(total-1, 0) || page.NextToken != "" {
				t.Fatalf("changed total %d: %+v %v", total, page, err)
			}
		})
	}
}
