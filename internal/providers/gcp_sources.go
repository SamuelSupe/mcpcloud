package providers

import (
	"bytes"
	"context"
	"encoding/base64"
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

func (a *gcpAdapter) queryMetrics(ctx context.Context, scope string, req provider.QueryRequest) (provider.Page, error) {
	start, end, err := rangeRequired(req, gcpMetricsOperation)
	if err != nil {
		return provider.Page{}, err
	}
	client, err := a.httpClient(ctx)
	if err != nil {
		return provider.Page{}, err
	}
	selector, err := singleMetricSelector(req.Metrics, 1, 1, gcpMetricsOperation)
	if err != nil {
		return provider.Page{}, err
	}
	filter, err := gcpMetricFilter(selector)
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_metric", Operation: gcpMetricsOperation, Message: err.Error()}
	}
	step := req.Step
	if step <= 0 {
		step = 5 * time.Minute
	}
	if step < time.Minute {
		step = time.Minute
	}
	limit := req.Limit
	if limit <= 0 || limit > 100000 {
		limit = 500
	}
	values := url.Values{
		"filter":                       []string{filter},
		"interval.startTime":           []string{start.Format(time.RFC3339Nano)},
		"interval.endTime":             []string{end.Format(time.RFC3339Nano)},
		"aggregation.alignmentPeriod":  []string{fmt.Sprintf("%ds", int64(step.Seconds()))},
		"aggregation.perSeriesAligner": []string{"ALIGN_MEAN"},
		"view":                         []string{"FULL"},
		"pageSize":                     []string{strconv.Itoa(limit)},
	}
	if req.PageToken != "" {
		values.Set("pageToken", req.PageToken)
	}
	endpoint := "https://monitoring.googleapis.com/v3/" + scope + "/timeSeries?" + values.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return provider.Page{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "gcp_api_error", Operation: gcpMetricsOperation, Message: err.Error(), Retryable: true}
	}
	defer response.Body.Close()
	if err := providerHTTPStatus(response, gcpMetricsOperation, "gcp_api_error"); err != nil {
		return provider.Page{}, err
	}
	var payload struct {
		TimeSeries []struct {
			Metric struct {
				Type   string            `json:"type"`
				Labels map[string]string `json:"labels"`
			} `json:"metric"`
			Resource struct {
				Type   string            `json:"type"`
				Labels map[string]string `json:"labels"`
			} `json:"resource"`
			Points []struct {
				Interval struct {
					EndTime string `json:"endTime"`
				} `json:"interval"`
				Value struct {
					DoubleValue *float64 `json:"doubleValue"`
					Int64Value  *string  `json:"int64Value"`
				} `json:"value"`
			} `json:"points"`
		} `json:"timeSeries"`
		NextPageToken string `json:"nextPageToken"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 32<<20)).Decode(&payload); err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: gcpMetricsOperation, Message: err.Error()}
	}
	rows := make([]map[string]any, 0)
	for _, series := range payload.TimeSeries {
		dimensions := make(map[string]string, len(series.Metric.Labels)+len(series.Resource.Labels))
		for name, value := range series.Metric.Labels {
			dimensions["metric."+name] = value
		}
		for name, value := range series.Resource.Labels {
			dimensions["resource."+name] = value
		}
		resourceID := gcpMetricResourceID(series.Resource.Labels)
		for _, point := range series.Points {
			timestamp, err := time.Parse(time.RFC3339Nano, point.Interval.EndTime)
			if err != nil {
				continue
			}
			value, ok := gcpPointValue(point.Value.DoubleValue, point.Value.Int64Value)
			if !ok {
				continue
			}
			row := metricRow(model.ProviderGCP, a.name, selector.Raw, "monitoring", resourceID, stringValue(dimensions["resource.location"]), scope, timestamp, value, "", dimensions)
			native := row["native"].(map[string]any)
			native["metric_type"] = series.Metric.Type
			native["resource_type"] = series.Resource.Type
			rows = append(rows, row)
		}
	}
	return provider.Page{Rows: rows, NextToken: payload.NextPageToken, Scanned: len(rows), Requests: 1}, nil
}

func gcpMetricFilter(selector metricSelector) (string, error) {
	filter := `metric.type = "` + escapeGoogleFilter(selector.Parts[0]) + `"`
	for name, value := range selector.Dimensions {
		prefix, label, ok := strings.Cut(name, ".")
		if !ok || label == "" || !validGCPIdentifier(label) {
			return "", fmt.Errorf("GCP dimensions must use metric.<label> or resource.<label>")
		}
		switch prefix {
		case "metric":
			filter += ` AND metric.labels."` + label + `" = "` + escapeGoogleFilter(value) + `"`
		case "resource":
			filter += ` AND resource.labels."` + label + `" = "` + escapeGoogleFilter(value) + `"`
		default:
			return "", fmt.Errorf("GCP dimensions must use metric.<label> or resource.<label>")
		}
	}
	return filter, nil
}

func escapeGoogleFilter(value string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value)
}

func gcpPointValue(doubleValue *float64, int64Value *string) (float64, bool) {
	if doubleValue != nil {
		return *doubleValue, true
	}
	if int64Value != nil {
		value, err := strconv.ParseFloat(*int64Value, 64)
		return value, err == nil
	}
	return 0, false
}

func gcpMetricResourceID(labels map[string]string) string {
	for _, key := range []string{"instance_id", "database_id", "cluster_name", "resource_id", "project_id"} {
		if value := labels[key]; value != "" {
			return value
		}
	}
	return ""
}

type bigQueryCursor struct {
	JobID     string `json:"j"`
	Location  string `json:"l,omitempty"`
	PageToken string `json:"p,omitempty"`
}

func encodeBigQueryCursor(cursor bigQueryCursor) string {
	data, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeBigQueryCursor(token string) (bigQueryCursor, error) {
	data, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(data) > 4096 {
		return bigQueryCursor{}, fmt.Errorf("invalid provider cursor")
	}
	var cursor bigQueryCursor
	if err := json.Unmarshal(data, &cursor); err != nil || cursor.JobID == "" || !validGCPIdentifier(cursor.JobID) {
		return bigQueryCursor{}, fmt.Errorf("invalid provider cursor")
	}
	if cursor.Location != "" && !validGCPIdentifier(cursor.Location) {
		return bigQueryCursor{}, fmt.Errorf("invalid provider cursor")
	}
	return cursor, nil
}

type bigQueryResponse struct {
	JobComplete bool   `json:"jobComplete"`
	PageToken   string `json:"pageToken"`
	TotalRows   string `json:"totalRows"`
	Errors      []struct {
		Reason  string `json:"reason"`
		Message string `json:"message"`
	} `json:"errors"`
	JobReference struct {
		JobID    string `json:"jobId"`
		Location string `json:"location"`
	} `json:"jobReference"`
	Schema struct {
		Fields []struct {
			Name string `json:"name"`
		} `json:"fields"`
	} `json:"schema"`
	Rows []struct {
		Fields []struct {
			Value any `json:"v"`
		} `json:"f"`
	} `json:"rows"`
}

func (a *gcpAdapter) queryCosts(ctx context.Context, scope string, req provider.QueryRequest) (provider.Page, error) {
	start, end, err := rangeRequired(req, gcpCostsOperation)
	if err != nil {
		return provider.Page{}, err
	}
	table := a.profile.Options["billing_table"]
	if table == "" {
		return provider.Page{}, &provider.Error{Code: "capability_unavailable", Operation: gcpCostsOperation, Message: "options.billing_table is not configured"}
	}
	client, err := a.httpClient(ctx)
	if err != nil {
		return provider.Page{}, err
	}
	project := a.profile.Options["billing_project"]
	if project == "" {
		project = strings.Split(table, ".")[0]
	}
	limit := req.Limit
	if limit <= 0 || limit > 100000 {
		limit = 500
	}
	var payload bigQueryResponse
	requests := 1
	if req.PageToken == "" {
		query := "SELECT FORMAT_DATE('%F', DATE(usage_start_time)) AS date, COALESCE(project.id, '') AS account, COALESCE(service.description, '') AS service, currency, CAST(SUM(cost + IFNULL((SELECT SUM(c.amount) FROM UNNEST(credits) AS c), 0)) AS STRING) AS amount FROM `" + table + "` WHERE DATE(usage_start_time) >= @start AND DATE(usage_start_time) < @end"
		parameters := []map[string]any{bigQueryParameter("start", "DATE", start.Format("2006-01-02")), bigQueryParameter("end", "DATE", end.Format("2006-01-02"))}
		if strings.HasPrefix(scope, "projects/") {
			query += " AND project.id = @project"
			parameters = append(parameters, bigQueryParameter("project", "STRING", strings.TrimPrefix(scope, "projects/")))
		}
		query += " GROUP BY date, account, service, currency ORDER BY date, account, service, currency"
		body := map[string]any{"query": query, "useLegacySql": false, "maxResults": limit, "timeoutMs": 30000, "parameterMode": "NAMED", "queryParameters": parameters}
		if location := a.profile.Options["billing_location"]; location != "" {
			body["location"] = location
		}
		encoded, _ := json.Marshal(body)
		endpoint := "https://bigquery.googleapis.com/bigquery/v2/projects/" + url.PathEscape(project) + "/queries"
		if err := gcpJSONRequest(ctx, client, http.MethodPost, endpoint, encoded, gcpCostsOperation, &payload); err != nil {
			return provider.Page{}, err
		}
	} else {
		cursor, err := decodeBigQueryCursor(req.PageToken)
		if err != nil {
			return provider.Page{}, &provider.Error{Code: "invalid_cursor", Operation: gcpCostsOperation, Message: err.Error()}
		}
		values := url.Values{"maxResults": []string{strconv.Itoa(limit)}}
		if cursor.Location != "" {
			values.Set("location", cursor.Location)
		}
		if cursor.PageToken != "" {
			values.Set("pageToken", cursor.PageToken)
		}
		endpoint := "https://bigquery.googleapis.com/bigquery/v2/projects/" + url.PathEscape(project) + "/queries/" + url.PathEscape(cursor.JobID) + "?" + values.Encode()
		if err := gcpJSONRequest(ctx, client, http.MethodGet, endpoint, nil, gcpCostsOperation, &payload); err != nil {
			return provider.Page{}, err
		}
		if payload.JobReference.JobID == "" {
			payload.JobReference.JobID = cursor.JobID
		}
		if payload.JobReference.Location == "" {
			payload.JobReference.Location = cursor.Location
		}
	}
	if len(payload.Errors) > 0 {
		message := payload.Errors[0].Message
		if message == "" {
			message = payload.Errors[0].Reason
		}
		return provider.Page{}, &provider.Error{Code: "gcp_api_error", Operation: gcpCostsOperation, Message: message}
	}
	next := ""
	if !payload.JobComplete || payload.PageToken != "" {
		next = encodeBigQueryCursor(bigQueryCursor{JobID: payload.JobReference.JobID, Location: payload.JobReference.Location, PageToken: payload.PageToken})
	}
	rows, err := a.bigQueryCostRows(payload)
	if err != nil {
		return provider.Page{}, err
	}
	return provider.Page{Rows: rows, NextToken: next, Scanned: len(rows), Requests: requests}, nil
}

func bigQueryParameter(name, typeName, value string) map[string]any {
	return map[string]any{"name": name, "parameterType": map[string]any{"type": typeName}, "parameterValue": map[string]any{"value": value}}
}

func gcpJSONRequest(ctx context.Context, client *http.Client, method, endpoint string, body []byte, operation string, target any) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return &provider.Error{Code: "gcp_api_error", Operation: operation, Message: err.Error(), Retryable: true}
	}
	defer response.Body.Close()
	if err := providerHTTPStatus(response, operation, "gcp_api_error"); err != nil {
		return err
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 32<<20)).Decode(target); err != nil {
		return &provider.Error{Code: "invalid_provider_response", Operation: operation, Message: err.Error()}
	}
	return nil
}

func providerHTTPStatus(response *http.Response, operation, code string) error {
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
	return &provider.Error{Code: code, Operation: operation, Message: fmt.Sprintf("HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body))), Retryable: response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500}
}

func (a *gcpAdapter) bigQueryCostRows(payload bigQueryResponse) ([]map[string]any, error) {
	columns := make([]string, len(payload.Schema.Fields))
	for i, field := range payload.Schema.Fields {
		columns[i] = field.Name
	}
	rows := make([]map[string]any, 0, len(payload.Rows))
	for _, item := range payload.Rows {
		values := map[string]string{}
		for i, field := range item.Fields {
			if i >= len(columns) || field.Value == nil {
				continue
			}
			values[columns[i]] = fmt.Sprint(field.Value)
		}
		amount, err := parseAmount(values["amount"])
		if err != nil {
			return nil, &provider.Error{Code: "invalid_provider_response", Operation: gcpCostsOperation, Message: err.Error()}
		}
		id := strings.Join([]string{values["date"], values["account"], values["service"], values["currency"]}, ":")
		row := costRow(model.ProviderGCP, a.name, id, values["date"], values["account"], values["service"], "", amount, values["currency"])
		native := row["native"].(map[string]any)
		native["billing_table"] = a.profile.Options["billing_table"]
		rows = append(rows, row)
	}
	return rows, nil
}

func singleMetricSelector(metrics []string, minParts, maxParts int, operation string) (metricSelector, error) {
	if len(metrics) != 1 {
		return metricSelector{}, &provider.Error{Code: "invalid_metric", Operation: operation, Message: "this provider accepts exactly one metric selector per request"}
	}
	selector, err := parseMetricSelector(metrics[0], minParts, maxParts)
	if err != nil {
		return metricSelector{}, &provider.Error{Code: "invalid_metric", Operation: operation, Message: err.Error()}
	}
	return selector, nil
}
