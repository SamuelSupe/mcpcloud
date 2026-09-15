package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	tchttp "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/http"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
	"regexp"
	"strings"
	"time"
)

const tencentPartnerCostsOperation = "tencent.intlpartnersmgt.describe_customer_own_cost_explorer_summary"

type tencentPartnerCostCursor struct {
	Profile, Account, Start, End, Currency string
	Page, Limit, Total                     int
}

func partnerCostError(code, message string) error {
	return &provider.Error{Code: code, Operation: tencentPartnerCostsOperation, Message: message}
}

func (a *tencentAdapter) queryPartnerCosts(ctx context.Context, req provider.QueryRequest) (provider.Page, error) {
	start, end, err := rangeRequired(req, tencentPartnerCostsOperation)
	if err != nil {
		return provider.Page{}, err
	}
	if a.profile.Options["billing_currency"] != "USD" || len(a.profile.Scopes.Accounts) != 1 {
		return provider.Page{}, partnerCostError("capability_unavailable", "international partner customer costs require explicit USD and one configured account")
	}
	accounts, err := restrictValues(a.profile.Scopes.Accounts, req.Accounts, tencentPartnerCostsOperation, "account")
	if err != nil {
		return provider.Page{}, err
	}
	start, end = partnerDailyBounds(start, end)
	if !start.Before(end) {
		if req.PageToken != "" {
			return provider.Page{}, partnerCostError("invalid_cursor", "partner cost cursor is outside the requested dates")
		}
		return provider.Page{}, nil
	}
	monthStart := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, start.Location())
	if end.After(monthStart.AddDate(0, 2, 0)) {
		return provider.Page{}, partnerCostError("invalid_range", "daily partner costs support at most two calendar months")
	}
	limit := req.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	scope := tencentPartnerCostCursor{Profile: a.name, Account: scopeTail(accounts[0]), Start: start.Format("2006-01-02"), End: end.Format("2006-01-02"), Currency: "USD", Page: 1, Limit: limit, Total: -1}
	if req.PageToken != "" {
		var c tencentPartnerCostCursor
		raw, e := base64.RawURLEncoding.DecodeString(req.PageToken)
		if e != nil || json.Unmarshal(raw, &c) != nil || c.Profile != scope.Profile || c.Account != scope.Account || c.Start != scope.Start || c.End != scope.End || c.Currency != scope.Currency || c.Page < 2 || c.Page > 10000 || c.Limit < 1 || c.Limit > 100 || c.Total < 1 || (c.Page-1)*c.Limit >= c.Total {
			return provider.Page{}, partnerCostError("invalid_cursor", "invalid partner cost cursor or changed scope")
		}
		scope = c
	}
	client, err := a.tencentClient("intlpartnersmgt.intl.tencentcloudapi.com", "", tencentPartnerCostsOperation)
	if err != nil {
		return provider.Page{}, err
	}
	request := tchttp.NewCommonRequest("intlpartnersmgt", "2022-09-28", "DescribeCustomerOwnCostExplorerSummary")
	request.SetContext(ctx)
	err = request.SetActionParameters(map[string]any{"StartTime": start.Format("2006-01-02 15:04:05"), "EndTime": end.Add(-time.Second).Format("2006-01-02 15:04:05"), "Dimension": "Business", "FeeType": "totalCost", "BillType": 1, "PeriodType": "day", "Page": scope.Page, "PageSize": scope.Limit})
	if err != nil {
		return provider.Page{}, err
	}
	response := tchttp.NewCommonResponse()
	if err = client.Send(request, response); err != nil {
		return provider.Page{}, tencentSourceError(tencentPartnerCostsOperation, err)
	}
	return a.partnerCostPage(response.GetBody(), scope)
}

func partnerDailyBounds(start, end time.Time) (time.Time, time.Time) {
	ceilDay := func(value time.Time) time.Time {
		value = value.UTC()
		day := time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
		if day.Before(value) {
			day = day.AddDate(0, 0, 1)
		}
		return day
	}
	return ceilDay(start), ceilDay(end)
}

var partnerAmountPattern = regexp.MustCompile(`^-?[0-9]+(\.[0-9]{1,8})?$`)

func (a *tencentAdapter) partnerCostPage(body []byte, c tencentPartnerCostCursor) (provider.Page, error) {
	var payload struct {
		Response *struct {
			TotalDetail *struct{ TotalCount *int }
			Detail      []struct {
				Code, Name string
				ItemDetail []struct{ Period, Cost string }
			}
			Error *struct{ Code, Message string }
		}
	}
	bad := func() (provider.Page, error) {
		return provider.Page{}, partnerCostError("invalid_provider_response", "invalid or incomplete partner cost response")
	}
	if json.Unmarshal(body, &payload) != nil || payload.Response == nil {
		return bad()
	}
	r := payload.Response
	if r.Error != nil {
		return provider.Page{}, partnerCostError(r.Error.Code, "partner cost API request failed")
	}
	if r.TotalDetail == nil || r.TotalDetail.TotalCount == nil {
		return bad()
	}
	total := *r.TotalDetail.TotalCount
	offset := (c.Page - 1) * c.Limit
	if total < 0 || (c.Total >= 0 && total != c.Total) || len(r.Detail) > c.Limit || offset+len(r.Detail) > total || (offset+len(r.Detail) < total && len(r.Detail) != c.Limit) {
		return bad()
	}
	rows := []map[string]any{}
	seen := map[string]bool{}
	for _, d := range r.Detail {
		if d.Code == "" || len(d.ItemDetail) == 0 {
			return bad()
		}
		for _, p := range d.ItemDetail {
			date, err := time.Parse("2006-01-02", p.Period)
			if err != nil || date.Format("2006-01-02") < c.Start || date.Format("2006-01-02") >= c.End || !partnerAmountPattern.MatchString(p.Cost) {
				return bad()
			}
			id := strings.Join([]string{p.Period, c.Account, d.Code}, ":")
			if seen[id] {
				return bad()
			}
			seen[id] = true
			amount, err := parseAmount(p.Cost)
			if err != nil {
				return bad()
			}
			row := costRow(model.ProviderTencent, a.name, id, p.Period, c.Account, d.Code, "", amount, c.Currency)
			row["native"].(map[string]any)["billing_source"] = "intl_partner_customer"
			native := row["native"].(map[string]any)
			native["amount_decimal"] = p.Cost
			native["product_name"] = d.Name
			native["dimension"] = "Business"
			native["fee_type"] = "totalCost"
			native["bill_type"] = 1
			native["currency_source"] = "profile_configuration"
			rows = append(rows, row)
		}
	}
	next := ""
	if offset+len(r.Detail) < total {
		c.Page++
		c.Total = total
		raw, err := json.Marshal(c)
		if err != nil {
			return provider.Page{}, fmt.Errorf("encode partner cursor: %w", err)
		}
		next = base64.RawURLEncoding.EncodeToString(raw)
	}
	return provider.Page{Rows: rows, NextToken: next, Scanned: len(r.Detail), Requests: 1}, nil
}
