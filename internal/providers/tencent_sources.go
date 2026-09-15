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
	if tencentDefaultStatisticMetric(selector.Parts[0]) {
		delete(params, "SpecifyStatistics")
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
		if tencentDefaultStatisticMetric(selector.Parts[0]) || len(values) == 0 {
			values = series.Values
		}
		for index, timestamp := range series.Timestamps {
			// GetMonitorData can include the end instant; our windows are [start,end).
			instant := time.Unix(int64(timestamp), 0)
			if instant.Before(pageStart) || !instant.Before(pageEnd) {
				continue
			}
			if index >= len(values) {
				break
			}
			row := metricRow(model.ProviderTencent, a.name, selector.Raw, "monitor", firstDimension(dimensionValues), region, account, time.Unix(int64(timestamp), 0), values[index], tencentMetricUnit(selector.Parts[0], selector.Parts[1]), dimensionValues)
			native := row["native"].(map[string]any)
			native["namespace"] = selector.Parts[0]
			native["metric_name"] = selector.Parts[1]
			native["statistic"] = "Average"
			if tencentDefaultStatisticMetric(selector.Parts[0]) {
				native["statistic"] = "ProviderDefault"
			}
			rows = append(rows, row)
		}
	}
	next := ""
	if pageEnd.Before(end) {
		next = encodePeriodCursor(periodCursor{Offset: int(pageEnd.Unix())})
	}
	return provider.Page{Rows: rows, NextToken: next, Scanned: len(rows), Requests: 1}, nil
}

// Units for the verified CVM metrics, without changing the provider values.
// https://cloud.tencent.com/document/product/248/6843
// Unknown metrics remain unspecified rather than receiving a guessed unit.
func tencentDefaultStatisticMetric(namespace string) bool {
	return namespace == "QCE/LB" || namespace == "QCE/COS" || namespace == "QCE/NAT_GATEWAY" || (namespace == "QCE/LB_PUBLIC" || namespace == "QCE/LB_PRIVATE")
}
func tencentMetricUnit(namespace, metric string) string {
	// EIP reference: /document/product/248/45099.
	if namespace == "QCE/LB" && (metric == "VipIntraffic" || metric == "VipOuttraffic") {
		return "Mbps"
	}
	// COS reference: /document/product/248/45140.
	if namespace == "QCE/COS" {
		switch metric {
		case "TotalRequestsPs":
			return "count/s"
		case "StdStorage":
			return "MB"
		}
	}
	if namespace == "QCE/NAT_GATEWAY" {
		switch metric {
		case "WanInDropPkg", "WanOutDropPkg":
			return "pps"
		case "Conns":
			return "count"
		}
	}
	if (namespace == "QCE/LB_PUBLIC" || namespace == "QCE/LB_PRIVATE") && metric == "ClientConnum" {
		return "count"
	}
	// Network references: /document/product/248/45069 and /document/product/248/51898.
	if namespace == "QCE/NAT_GATEWAY" && (metric == "OutBandwidth" || metric == "InBandwidth") {
		return "Mbps"
	}
	if (namespace == "QCE/LB_PUBLIC" || namespace == "QCE/LB_PRIVATE") && (metric == "OutTraffic" || metric == "InTraffic") {
		return "Mbps"
	}
	// Product metric references: /document/api/248/45147 and /document/product/248/49729.
	if namespace == "QCE/CDB" && (metric == "CpuUseRate" || metric == "VolumeRate") {
		return "%"
	}
	if namespace == "QCE/REDIS_MEM" && metric == "MemUtil" {
		return "%"
	}
	if namespace != "QCE/CVM" {
		return ""
	}
	switch metric {
	case "CpuUsage", "CPUUsage", "BaseCpuUsage", "MemUsage", "CvmDiskUsage", "DiskUsage":
		return "%"
	case "WanIntraffic", "WanOuttraffic":
		return "Mbps"
	case "CpuLoadavg", "Cpuloadavg5m", "Cpuloadavg15m":
		return "1"
	}
	return ""
}

func (a *tencentAdapter) queryCosts(ctx context.Context, req provider.QueryRequest) (provider.Page, error) {
	if a.profile.Options["billing_source"] == "intl_partner_customer" {
		return a.queryPartnerCosts(ctx, req)
	}
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
