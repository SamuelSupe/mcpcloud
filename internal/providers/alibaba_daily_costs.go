package providers

import (
	"context"
	"fmt"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/requests"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/bssopenapi"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
	"slices"
	"strconv"
	"strings"
	"time"
)

var queryAlibabaDailyBill = func(c *bssopenapi.Client, r *bssopenapi.QueryInstanceBillRequest) (*bssopenapi.QueryInstanceBillResponse, error) {
	return c.QueryInstanceBill(r)
}

func (a *alibabaAdapter) queryCosts(ctx context.Context, req provider.QueryRequest) (provider.Page, error) {
	start, end, err := rangeRequired(req, alibabaCostsOperation)
	if err != nil {
		return provider.Page{}, err
	}
	day, page, err := alibabaDailyPosition(start, end, req.PageToken)
	if err != nil {
		return provider.Page{}, err
	}
	if !day.Before(end) {
		return provider.Page{}, nil
	}
	accounts, err := restrictValues(a.profile.Scopes.Accounts, req.Accounts, alibabaCostsOperation, "account")
	if err != nil {
		return provider.Page{}, err
	}
	r := bssopenapi.CreateQueryInstanceBillRequest()
	r.Granularity = "DAILY"
	r.BillingDate = day.Format("2006-01-02")
	r.BillingCycle = day.Format("2006-01")
	r.IsBillingItem = requests.NewBoolean(false)
	r.IsHideZeroCharge = requests.NewBoolean(false)
	if len(accounts) == 1 {
		n, e := strconv.ParseInt(accounts[0], 10, 64)
		if e != nil {
			return provider.Page{}, &provider.Error{Code: "invalid_scope", Operation: alibabaCostsOperation, Message: "Alibaba account scope must be numeric"}
		}
		r.BillOwnerId = requests.Integer(strconv.FormatInt(n, 10))
	}
	limit := req.Limit
	if limit <= 0 || limit > 300 {
		limit = 300
	}
	r.PageSize = requests.NewInteger(limit)
	r.PageNum = requests.NewInteger(page)
	setAlibabaTimeout(ctx, r.RpcRequest)
	c, err := a.bssClient()
	if err != nil {
		return provider.Page{}, err
	}
	response, err := queryAlibabaDailyBill(c, r)
	if err != nil {
		return provider.Page{}, alibabaSourceError(alibabaCostsOperation, err)
	}
	if err = ctx.Err(); err != nil {
		return provider.Page{}, err
	}
	if !response.Success {
		return provider.Page{}, &provider.Error{Code: response.Code, Operation: alibabaCostsOperation, Message: response.Message}
	}
	return a.alibabaDailyPage(response, accounts, day, end, page, limit)
}
func alibabaDailyPosition(start, end time.Time, token string) (time.Time, int, error) {
	day := time.Date(start.UTC().Year(), start.UTC().Month(), start.UTC().Day(), 0, 0, 0, 0, time.UTC)
	if day.Before(start) {
		day = day.AddDate(0, 0, 1)
	}
	cursor, err := decodePeriodCursor(token)
	if err != nil {
		return time.Time{}, 0, &provider.Error{Code: "invalid_cursor", Operation: alibabaCostsOperation, Message: err.Error()}
	}
	page := 1
	if token != "" {
		d, e := time.Parse("2006-01-02", cursor.Period)
		if e != nil || d.Before(day) || !d.Before(end) || cursor.Page < 1 || cursor.Offset != 0 {
			return time.Time{}, 0, &provider.Error{Code: "invalid_cursor", Operation: alibabaCostsOperation, Message: "Daily billing cursor is outside the requested dates"}
		}
		day = d
		page = cursor.Page
	}
	return day, page, nil
}
func (a *alibabaAdapter) alibabaDailyPage(response *bssopenapi.QueryInstanceBillResponse, accounts []string, day, end time.Time, page, limit int) (provider.Page, error) {
	bad := func(msg string) (provider.Page, error) {
		return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: alibabaCostsOperation, Message: msg}
	}
	data := response.Data
	if data.TotalCount > 50000 {
		return bad("Daily bill exceeds QueryInstanceBill's 50000-row limit")
	}
	if data.PageNum != page || data.PageSize != limit || data.TotalCount < 0 {
		return bad("Unexpected billing pagination metadata")
	}
	if len(data.Items.Item) == 0 && (page-1)*limit < data.TotalCount {
		return bad("Empty billing page before total count")
	}
	rows := make([]map[string]any, 0, len(data.Items.Item))
	for i, item := range data.Items.Item {
		if item.BillingDate != day.Format("2006-01-02") {
			return bad("Missing or mismatched daily billing date")
		}
		currency := strings.TrimSpace(item.Currency)
		if currency == "" {
			return bad("Daily bill currency is missing")
		}
		account := item.OwnerID
		if account == "" {
			account = data.AccountID
		}
		if account == "" {
			return bad("Daily bill account is missing")
		}
		if len(accounts) > 0 && !slices.Contains(accounts, account) {
			continue
		}
		region := alibabaBillingRegion(item.Region)
		if len(a.profile.Regions) > 0 && !slices.Contains(a.profile.Regions, "*") && region != "" && !slices.Contains(a.profile.Regions, region) {
			continue
		}
		service := item.ProductName
		if service == "" {
			service = item.ProductCode
		}
		id := fmt.Sprintf("%s:%s:%d:%d", day.Format("2006-01-02"), account, page, i)
		row := costRow(model.ProviderAlibaba, a.name, id, item.BillingDate, account, service, region, item.PretaxAmount, currency)
		native := row["native"].(map[string]any)
		native["product_code"] = item.ProductCode
		native["billing_region"] = item.Region
		native["granularity"] = "DAILY"
		native["amount_basis"] = "PretaxAmount"
		rows = append(rows, row)
	}
	next := ""
	if page*limit < data.TotalCount {
		next = encodePeriodCursor(periodCursor{Period: day.Format("2006-01-02"), Page: page + 1})
	} else if tomorrow := day.AddDate(0, 0, 1); tomorrow.Before(end) {
		next = encodePeriodCursor(periodCursor{Period: tomorrow.Format("2006-01-02"), Page: 1})
	}
	return provider.Page{Rows: rows, NextToken: next, Scanned: len(data.Items.Item), Requests: 1}, nil
}
func alibabaBillingRegion(region string) string {
	aliases := map[string]string{"华东1（杭州）": "cn-hangzhou", "华东2（上海）": "cn-shanghai", "华北2（北京）": "cn-beijing", "华北3（张家口）": "cn-zhangjiakou", "华南1（深圳）": "cn-shenzhen", "中国香港": "cn-hongkong", "新加坡": "ap-southeast-1", "德国（法兰克福）": "eu-central-1", "美国（硅谷）": "us-west-1", "美国（弗吉尼亚）": "us-east-1", "日本（东京）": "ap-northeast-1", "印度尼西亚（雅加达）": "ap-southeast-5", "英国（伦敦）": "eu-west-1"}
	if mapped, ok := aliases[region]; ok {
		return mapped
	}
	return region
}
