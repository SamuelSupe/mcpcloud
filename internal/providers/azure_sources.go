package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

func (a *azureAdapter) queryMetrics(ctx context.Context, req provider.QueryRequest) (provider.Page, error) {
	start, end, err := rangeRequired(req, azureMetricsOperation)
	if err != nil {
		return provider.Page{}, err
	}
	selector, err := singleMetricSelector(req.Metrics, 2, 2, azureMetricsOperation)
	if err != nil {
		return provider.Page{}, err
	}
	if req.Region == "" || req.Region == "*" || !cloudRegionPattern.MatchString(req.Region) {
		return provider.Page{}, &provider.Error{Code: "invalid_region", Operation: azureMetricsOperation, Message: "Azure subscription metrics require an exact region"}
	}
	subscription, err := a.subscription(req.Accounts, azureMetricsOperation)
	if err != nil {
		return provider.Page{}, err
	}
	token, err := a.accessToken(ctx, azureMetricsOperation)
	if err != nil {
		return provider.Page{}, err
	}
	step := req.Step
	if step <= 0 {
		step = 5 * time.Minute
	}
	values := url.Values{
		"api-version":     []string{"2023-10-01"},
		"region":          []string{req.Region},
		"metricnamespace": []string{selector.Parts[0]},
		"metricnames":     []string{selector.Parts[1]},
		"timespan":        []string{start.Format(time.RFC3339) + "/" + end.Format(time.RFC3339)},
		"interval":        []string{azureDuration(step)},
		"aggregation":     []string{"Average,Total,Minimum,Maximum,Count"},
	}
	if len(selector.Dimensions) > 0 {
		filters := make([]string, 0, len(selector.Dimensions))
		for name, value := range selector.Dimensions {
			filters = append(filters, name+" eq '"+strings.ReplaceAll(value, "'", "''")+"'")
		}
		values.Set("$filter", strings.Join(filters, " and "))
	}
	endpoint := "https://management.azure.com/subscriptions/" + url.PathEscape(subscription) + "/providers/microsoft.insights/metrics?" + values.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return provider.Page{}, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "azure_api_error", Operation: azureMetricsOperation, Message: err.Error(), Retryable: true}
	}
	defer response.Body.Close()
	if err := providerHTTPStatus(response, azureMetricsOperation, "azure_api_error"); err != nil {
		return provider.Page{}, err
	}
	var payload struct {
		Value []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
			Name struct {
				Value string `json:"value"`
			} `json:"name"`
			Unit       string `json:"unit"`
			TimeSeries []struct {
				Metadata []struct {
					Name struct {
						Value string `json:"value"`
					} `json:"name"`
					Value string `json:"value"`
				} `json:"metadatavalues"`
				Data []struct {
					Timestamp string   `json:"timeStamp"`
					Average   *float64 `json:"average"`
					Total     *float64 `json:"total"`
					Minimum   *float64 `json:"minimum"`
					Maximum   *float64 `json:"maximum"`
					Count     *float64 `json:"count"`
				} `json:"data"`
			} `json:"timeseries"`
		} `json:"value"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 32<<20)).Decode(&payload); err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: azureMetricsOperation, Message: err.Error()}
	}
	rows := make([]map[string]any, 0)
	for _, metric := range payload.Value {
		for _, series := range metric.TimeSeries {
			dimensions := make(map[string]string, len(series.Metadata))
			for _, item := range series.Metadata {
				dimensions[item.Name.Value] = item.Value
			}
			for _, point := range series.Data {
				timestamp, err := time.Parse(time.RFC3339Nano, point.Timestamp)
				if err != nil {
					continue
				}
				value, statistic, ok := azureMetricValue(point.Average, point.Total, point.Minimum, point.Maximum, point.Count)
				if !ok {
					continue
				}
				row := metricRow(model.ProviderAzure, a.name, selector.Raw, "monitor", metric.ID, req.Region, subscription, timestamp, value, metric.Unit, dimensions)
				native := row["native"].(map[string]any)
				native["metric_namespace"] = selector.Parts[0]
				native["metric_name"] = metric.Name.Value
				native["statistic"] = statistic
				rows = append(rows, row)
			}
		}
	}
	return provider.Page{Rows: rows, Scanned: len(rows), Requests: 1}, nil
}

func (a *azureAdapter) queryCosts(ctx context.Context, req provider.QueryRequest) (provider.Page, error) {
	start, end, err := rangeRequired(req, azureCostsOperation)
	if err != nil {
		return provider.Page{}, err
	}
	subscription, err := a.subscription(req.Accounts, azureCostsOperation)
	if err != nil {
		return provider.Page{}, err
	}
	token, err := a.accessToken(ctx, azureCostsOperation)
	if err != nil {
		return provider.Page{}, err
	}
	values := url.Values{"api-version": []string{"2025-03-01"}}
	if req.PageToken != "" {
		if len(req.PageToken) > 4096 || strings.ContainsAny(req.PageToken, "\r\n\x00") {
			return provider.Page{}, &provider.Error{Code: "invalid_cursor", Operation: azureCostsOperation, Message: "invalid provider cursor"}
		}
		values.Set("$skiptoken", req.PageToken)
	}
	endpoint := "https://management.azure.com/subscriptions/" + url.PathEscape(subscription) + "/providers/Microsoft.CostManagement/query?" + values.Encode()
	body := map[string]any{
		"type":       "ActualCost",
		"timeframe":  "Custom",
		"timePeriod": map[string]any{"from": start.Format(time.RFC3339), "to": end.Format(time.RFC3339)},
		"dataset": map[string]any{
			"granularity": "Daily",
			"aggregation": map[string]any{"totalCost": map[string]any{"name": "Cost", "function": "Sum"}},
			"grouping":    []map[string]any{{"type": "Dimension", "name": "ServiceName"}, {"type": "Dimension", "name": "Currency"}},
		},
	}
	encoded, _ := json.Marshal(body)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return provider.Page{}, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "azure_api_error", Operation: azureCostsOperation, Message: err.Error(), Retryable: true}
	}
	defer response.Body.Close()
	if err := providerHTTPStatus(response, azureCostsOperation, "azure_api_error"); err != nil {
		return provider.Page{}, err
	}
	var payload struct {
		Properties struct {
			NextLink string `json:"nextLink"`
			Columns  []struct {
				Name string `json:"name"`
			} `json:"columns"`
			Rows [][]any `json:"rows"`
		} `json:"properties"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 32<<20)).Decode(&payload); err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: azureCostsOperation, Message: err.Error()}
	}
	next, err := azureCostSkipToken(payload.Properties.NextLink, subscription)
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: azureCostsOperation, Message: err.Error()}
	}
	columns := make([]string, len(payload.Properties.Columns))
	for i, column := range payload.Properties.Columns {
		columns[i] = strings.ToLower(column.Name)
	}
	rows := make([]map[string]any, 0, len(payload.Properties.Rows))
	for _, item := range payload.Properties.Rows {
		values := map[string]any{}
		for i, value := range item {
			if i < len(columns) {
				values[columns[i]] = value
			}
		}
		amount, err := numericValue(firstNonNil(values["cost"], values["totalcost"], values["pretaxcost"]))
		if err != nil {
			return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: azureCostsOperation, Message: "cost amount is invalid"}
		}
		date := azureCostDate(firstNonNil(values["usagedate"], values["date"]))
		service := fmt.Sprint(firstNonNil(values["servicename"], values["service"]))
		currency := fmt.Sprint(values["currency"])
		id := strings.Join([]string{date, subscription, service, currency}, ":")
		row := costRow(model.ProviderAzure, a.name, id, date, subscription, service, "", amount, currency)
		native := row["native"].(map[string]any)
		native["scope_type"] = "subscription"
		rows = append(rows, row)
	}
	return provider.Page{Rows: rows, NextToken: next, Scanned: len(rows), Requests: 1}, nil
}

func (a *azureAdapter) subscription(requested []string, operation string) (string, error) {
	subscriptions, err := restrictValues(a.profile.Scopes.Subscriptions, requested, operation, "subscription")
	if err != nil {
		return "", err
	}
	if len(subscriptions) != 1 {
		return "", &provider.Error{Code: "invalid_scope", Operation: operation, Message: "Azure metrics and costs require exactly one subscription target"}
	}
	return subscriptions[0], nil
}

func azureDuration(value time.Duration) string {
	seconds := int64(value.Seconds())
	if seconds%3600 == 0 {
		return fmt.Sprintf("PT%dH", seconds/3600)
	}
	if seconds%60 == 0 {
		return fmt.Sprintf("PT%dM", seconds/60)
	}
	return fmt.Sprintf("PT%dS", seconds)
}

func azureMetricValue(values ...*float64) (float64, string, bool) {
	names := []string{"Average", "Total", "Minimum", "Maximum", "Count"}
	for i, value := range values {
		if value != nil {
			return *value, names[i], true
		}
	}
	return 0, "", false
}

func azureCostSkipToken(nextLink, subscription string) (string, error) {
	if nextLink == "" {
		return "", nil
	}
	parsed, err := url.Parse(nextLink)
	expectedPath := "/subscriptions/" + subscription + "/providers/Microsoft.CostManagement/query"
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || !strings.EqualFold(parsed.Host, "management.azure.com") || !strings.EqualFold(parsed.Path, expectedPath) || parsed.User != nil {
		return "", fmt.Errorf("provider returned an unsafe next link")
	}
	token := parsed.Query().Get("$skiptoken")
	if token == "" || len(token) > 4096 {
		return "", fmt.Errorf("provider returned an invalid next link")
	}
	return token, nil
}

func numericValue(value any) (float64, error) {
	switch item := value.(type) {
	case float64:
		return item, nil
	case json.Number:
		return item.Float64()
	case string:
		return strconv.ParseFloat(item, 64)
	default:
		return strconv.ParseFloat(fmt.Sprint(value), 64)
	}
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil && fmt.Sprint(value) != "" {
			return value
		}
	}
	return nil
}

func azureCostDate(value any) string {
	raw := fmt.Sprint(value)
	if len(raw) == 8 {
		if parsed, err := time.Parse("20060102", raw); err == nil {
			return parsed.Format("2006-01-02")
		}
	}
	return raw
}
