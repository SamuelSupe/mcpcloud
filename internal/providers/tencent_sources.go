package providers

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	tencent "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	tchttp "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/http"
	tcprofile "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"

	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

func (a *tencentAdapter) queryMetrics(ctx context.Context, req provider.QueryRequest) (provider.Page, error) {
	start, end, err := rangeRequired(req, tencentMetricsOperation)
	if err != nil {
		return provider.Page{}, err
	}
	if len(a.profile.Scopes.Accounts) != 1 {
		return provider.Page{}, &provider.Error{Code: "capability_unavailable", Operation: tencentMetricsOperation, Message: "Tencent Cloud metrics require exactly one configured account"}
	}
	accounts, err := restrictValues(a.profile.Scopes.Accounts, req.Accounts, tencentMetricsOperation, "account")
	if err != nil {
		return provider.Page{}, err
	}
	selector, err := singleMetricSelector(req.Metrics, 2, 2, tencentMetricsOperation)
	if err != nil {
		return provider.Page{}, err
	}
	if len(selector.Dimensions) == 0 {
		return provider.Page{}, &provider.Error{Code: "invalid_metric", Operation: tencentMetricsOperation, Message: "Tencent Cloud metric selectors require at least one instance dimension"}
	}
	region := req.Region
	if region == "" || region == "*" {
		return provider.Page{}, &provider.Error{Code: "invalid_region", Operation: tencentMetricsOperation, Message: "Tencent Cloud metrics require an explicit profile region"}
	}
	client, err := a.tencentClient("monitor.tencentcloudapi.com", region, tencentMetricsOperation)
	if err != nil {
		return provider.Page{}, err
	}
	step := req.Step
	if step <= 0 {
		step = 5 * time.Minute
	}
	pageStart := start
	if req.PageToken != "" {
		cursor, err := decodePeriodCursor(req.PageToken)
		if err != nil || cursor.Offset <= 0 {
			return provider.Page{}, &provider.Error{Code: "invalid_cursor", Operation: tencentMetricsOperation, Message: "invalid provider cursor"}
		}
		pageStart = time.Unix(int64(cursor.Offset), 0).UTC()
		if pageStart.Before(start) || !pageStart.Before(end) {
			return provider.Page{}, &provider.Error{Code: "invalid_cursor", Operation: tencentMetricsOperation, Message: "provider cursor is outside the requested range"}
		}
	}
	limit := req.Limit
	if limit <= 0 || limit > 7200 {
		limit = 500
	}
	pageEnd := pageStart.Add(time.Duration(limit) * step)
	if pageEnd.After(end) {
		pageEnd = end
	}
	dimensions := make([]map[string]any, 0, len(selector.Dimensions))
	for name, value := range selector.Dimensions {
		dimensions = append(dimensions, map[string]any{"Name": name, "Value": value})
	}
	params := map[string]any{
		"Namespace": selector.Parts[0], "MetricName": selector.Parts[1],
		"Instances":         []map[string]any{{"Dimensions": dimensions}},
		"Period":            uint64(step.Seconds()),
		"StartTime":         pageStart.Format("2006-01-02T15:04:05-07:00"),
		"EndTime":           pageEnd.Format("2006-01-02T15:04:05-07:00"),
		"SpecifyStatistics": int64(1),
	}
	request := tchttp.NewCommonRequest("monitor", "2018-07-24", "GetMonitorData")
	request.SetContext(ctx)
	if err := request.SetActionParameters(params); err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_metric", Operation: tencentMetricsOperation, Message: err.Error()}
	}
	response := tchttp.NewCommonResponse()
	if err := client.Send(request, response); err != nil {
		return provider.Page{}, tencentSourceError(tencentMetricsOperation, err)
	}
	var payload struct {
		Response struct {
			DataPoints []struct {
				Dimensions []struct {
					Name  string `json:"Name"`
					Value string `json:"Value"`
				} `json:"Dimensions"`
				Timestamps []float64 `json:"Timestamps"`
				Values     []float64 `json:"Values"`
				AvgValues  []float64 `json:"AvgValues"`
			} `json:"DataPoints"`
			Error *struct {
				Code    string `json:"Code"`
				Message string `json:"Message"`
			} `json:"Error"`
		} `json:"Response"`
	}
	if err := json.Unmarshal(response.GetBody(), &payload); err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: tencentMetricsOperation, Message: err.Error()}
	}
	if payload.Response.Error != nil {
		return provider.Page{}, &provider.Error{Code: payload.Response.Error.Code, Operation: tencentMetricsOperation, Message: payload.Response.Error.Message, Retryable: containsAny(strings.ToLower(payload.Response.Error.Code), "requestlimit", "internalerror")}
	}
	account := scopeTail(accounts[0])
	rows := make([]map[string]any, 0)
	for _, series := range payload.Response.DataPoints {
		dimensionValues := map[string]string{}
		for _, dimension := range series.Dimensions {
			dimensionValues[dimension.Name] = dimension.Value
		}
		values := series.AvgValues
		if len(values) == 0 {
			values = series.Values
		}
		for index, timestamp := range series.Timestamps {
			if index >= len(values) {
				break
			}
			row := metricRow(model.ProviderTencent, a.name, selector.Raw, "monitor", firstDimension(dimensionValues), region, account, time.Unix(int64(timestamp), 0), values[index], "", dimensionValues)
			native := row["native"].(map[string]any)
			native["namespace"] = selector.Parts[0]
			native["metric_name"] = selector.Parts[1]
			native["statistic"] = "Average"
			rows = append(rows, row)
		}
	}
	next := ""
	if pageEnd.Before(end) {
		next = encodePeriodCursor(periodCursor{Offset: int(pageEnd.Unix())})
	}
	return provider.Page{Rows: rows, NextToken: next, Scanned: len(rows), Requests: 1}, nil
}

func (a *tencentAdapter) queryCosts(ctx context.Context, req provider.QueryRequest) (provider.Page, error) {
	start, end, err := rangeRequired(req, tencentCostsOperation)
	if err != nil {
		return provider.Page{}, err
	}
	currency := a.profile.Options["billing_currency"]
	if currency == "" {
		return provider.Page{}, &provider.Error{Code: "capability_unavailable", Operation: tencentCostsOperation, Message: "options.billing_currency is not configured"}
	}
	if len(a.profile.Scopes.Accounts) != 1 {
		return provider.Page{}, &provider.Error{Code: "capability_unavailable", Operation: tencentCostsOperation, Message: "Tencent Cloud costs require exactly one configured account"}
	}
	accounts, err := restrictValues(a.profile.Scopes.Accounts, req.Accounts, tencentCostsOperation, "account")
	if err != nil {
		return provider.Page{}, err
	}
	account := scopeTail(accounts[0])
	cursor, err := decodePeriodCursor(req.PageToken)
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_cursor", Operation: tencentCostsOperation, Message: err.Error()}
	}
	page := max(1, cursor.Page)
	limit := req.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	client, err := a.tencentClient("billing.tencentcloudapi.com", "", tencentCostsOperation)
	if err != nil {
		return provider.Page{}, err
	}
	params := map[string]any{
		"BeginTime": start.Format("2006-01-02 15:04:05"),
		"EndTime":   end.Add(-time.Second).Format("2006-01-02 15:04:05"),
		"BillType":  "1", "PeriodType": "day", "Dimensions": "business", "FeeType": "cost",
		"PageSize": limit, "PageNo": page,
	}
	request := tchttp.NewCommonRequest("billing", "2018-07-09", "DescribeCostExplorerSummary")
	request.SetContext(ctx)
	if err := request.SetActionParameters(params); err != nil {
		return provider.Page{}, err
	}
	response := tchttp.NewCommonResponse()
	if err := client.Send(request, response); err != nil {
		return provider.Page{}, tencentSourceError(tencentCostsOperation, err)
	}
	var payload struct {
		Response struct {
			Total  int `json:"Total"`
			Detail []struct {
				Name       string `json:"Name"`
				TimeDetail []struct {
					Time  string `json:"Time"`
					Money string `json:"Money"`
				} `json:"TimeDetail"`
			} `json:"Detail"`
			Error *struct {
				Code    string `json:"Code"`
				Message string `json:"Message"`
			} `json:"Error"`
		} `json:"Response"`
	}
	if err := json.Unmarshal(response.GetBody(), &payload); err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: tencentCostsOperation, Message: err.Error()}
	}
	if payload.Response.Error != nil {
		return provider.Page{}, &provider.Error{Code: payload.Response.Error.Code, Operation: tencentCostsOperation, Message: payload.Response.Error.Message, Retryable: containsAny(strings.ToLower(payload.Response.Error.Code), "requestlimit", "internalerror")}
	}
	rows := make([]map[string]any, 0)
	for _, detail := range payload.Response.Detail {
		for _, point := range detail.TimeDetail {
			amount, err := parseAmount(point.Money)
			if err != nil {
				return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: tencentCostsOperation, Message: err.Error()}
			}
			date := point.Time
			if parsed, err := time.Parse("2006-01-02 15:04:05", point.Time); err == nil {
				date = parsed.Format("2006-01-02")
			}
			id := strings.Join([]string{date, account, detail.Name}, ":")
			row := costRow(model.ProviderTencent, a.name, id, date, account, detail.Name, "", amount, currency)
			native := row["native"].(map[string]any)
			native["dimension"] = "business"
			native["fee_type"] = "cost"
			rows = append(rows, row)
		}
	}
	next := ""
	if page*limit < payload.Response.Total {
		next = encodePeriodCursor(periodCursor{Page: page + 1})
	}
	return provider.Page{Rows: rows, NextToken: next, Scanned: len(payload.Response.Detail), Requests: 1}, nil
}

func (a *tencentAdapter) tencentClient(endpoint, region, operation string) (*tencent.Client, error) {
	credential, err := a.credential()
	if err != nil {
		return nil, &provider.Error{Code: "authentication_error", Operation: operation, Message: err.Error()}
	}
	profile := tcprofile.NewClientProfile()
	profile.HttpProfile.Endpoint = endpoint
	profile.NetworkFailureMaxRetries = 0
	profile.RateLimitExceededMaxRetries = 0
	return tencent.NewCommonClient(credential, region, profile), nil
}

func tencentSourceError(operation string, err error) error {
	message := err.Error()
	return &provider.Error{Code: "tencent_api_error", Operation: operation, Message: message, Retryable: containsAny(strings.ToLower(message), "requestlimit", "timeout", "internalerror")}
}
