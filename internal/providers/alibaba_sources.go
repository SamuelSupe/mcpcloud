package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/requests"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/bssopenapi"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/cms"

	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

func (a *alibabaAdapter) queryMetrics(ctx context.Context, req provider.QueryRequest) (provider.Page, error) {
	start, end, err := rangeRequired(req, alibabaMetricsOperation)
	if err != nil {
		return provider.Page{}, err
	}
	if len(a.profile.Scopes.Accounts) != 1 {
		return provider.Page{}, &provider.Error{Code: "capability_unavailable", Operation: alibabaMetricsOperation, Message: "Alibaba Cloud metrics require exactly one configured account"}
	}
	accounts, err := restrictValues(a.profile.Scopes.Accounts, req.Accounts, alibabaMetricsOperation, "account")
	if err != nil {
		return provider.Page{}, err
	}
	selector, err := singleMetricSelector(req.Metrics, 2, 2, alibabaMetricsOperation)
	if err != nil {
		return provider.Page{}, err
	}
	client, err := a.cmsClient(req.Region)
	if err != nil {
		return provider.Page{}, err
	}
	limit := req.Limit
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	step := req.Step
	if step <= 0 {
		step = 5 * time.Minute
	}
	dimensions, _ := json.Marshal(selector.Dimensions)
	request := cms.CreateDescribeMetricListRequest()
	setAlibabaTimeout(ctx, request.RpcRequest)
	request.Namespace = selector.Parts[0]
	request.MetricName = selector.Parts[1]
	request.StartTime = strconv.FormatInt(start.UnixMilli(), 10)
	request.EndTime = strconv.FormatInt(end.UnixMilli(), 10)
	request.Period = strconv.FormatInt(int64(step.Seconds()), 10)
	request.Length = strconv.Itoa(limit)
	request.NextToken = req.PageToken
	if len(selector.Dimensions) > 0 {
		request.Dimensions = string(dimensions)
	}
	response, err := client.DescribeMetricList(request)
	if err != nil {
		return provider.Page{}, alibabaSourceError(alibabaMetricsOperation, err)
	}
	if err := ctx.Err(); err != nil {
		return provider.Page{}, err
	}
	if !response.Success {
		return provider.Page{}, &provider.Error{Code: response.Code, Operation: alibabaMetricsOperation, Message: response.Message, Retryable: containsAny(strings.ToLower(response.Code), "throttl", "limit")}
	}
	var datapoints []map[string]any
	if strings.TrimSpace(response.Datapoints) != "" {
		decoder := json.NewDecoder(strings.NewReader(response.Datapoints))
		decoder.UseNumber()
		if err := decoder.Decode(&datapoints); err != nil {
			return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: alibabaMetricsOperation, Message: err.Error()}
		}
	}
	account := scopeTail(accounts[0])
	rows := make([]map[string]any, 0, len(datapoints))
	for _, point := range datapoints {
		timestamp, ok := alibabaMetricTime(point)
		if !ok {
			continue
		}
		value, statistic, ok := alibabaMetricValue(point)
		if !ok {
			continue
		}
		pointDimensions := make(map[string]string, len(selector.Dimensions))
		for name, value := range selector.Dimensions {
			pointDimensions[name] = value
		}
		for name, value := range point {
			if name == "timestamp" || name == "Timestamp" || alibabaStatisticName(name) {
				continue
			}
			if text, ok := value.(string); ok && len(text) <= 256 {
				pointDimensions[name] = text
			}
		}
		resourceID := firstDimension(pointDimensions)
		row := metricRow(model.ProviderAlibaba, a.name, selector.Raw, "cms", resourceID, req.Region, account, timestamp, value, "", pointDimensions)
		native := row["native"].(map[string]any)
		native["namespace"] = selector.Parts[0]
		native["metric_name"] = selector.Parts[1]
		native["statistic"] = statistic
		rows = append(rows, row)
	}
	return provider.Page{Rows: rows, NextToken: response.NextToken, Scanned: len(datapoints), Requests: 1}, nil
}

func (a *alibabaAdapter) cmsClient(region string) (*cms.Client, error) {
	if region == "" || region == "*" {
		region = "cn-hangzhou"
	}
	if a.profile.Credential.Source != "env" {
		client, err := cms.NewClient()
		return client, alibabaClientError(alibabaMetricsOperation, err)
	}
	access := envValue(a.profile, "ALIBABA_CLOUD_ACCESS_KEY_ID", "ALIBABA_CLOUD_ACCESS_KEY_ID")
	secret := envValue(a.profile, "ALIBABA_CLOUD_ACCESS_KEY_SECRET", "ALIBABA_CLOUD_ACCESS_KEY_SECRET")
	token := envValue(a.profile, "ALIBABA_CLOUD_SECURITY_TOKEN", "ALIBABA_CLOUD_SECURITY_TOKEN")
	if access == "" || secret == "" {
		return nil, &provider.Error{Code: "missing_credentials", Operation: alibabaMetricsOperation, Message: "Alibaba Cloud credential environment variables are not set"}
	}
	var client *cms.Client
	var err error
	if token != "" {
		client, err = cms.NewClientWithStsToken(region, access, secret, token)
	} else {
		client, err = cms.NewClientWithAccessKey(region, access, secret)
	}
	return client, alibabaClientError(alibabaMetricsOperation, err)
}

func (a *alibabaAdapter) bssClient() (*bssopenapi.Client, error) {
	if a.profile.Credential.Source != "env" {
		client, err := bssopenapi.NewClient()
		return client, alibabaClientError(alibabaCostsOperation, err)
	}
	access := envValue(a.profile, "ALIBABA_CLOUD_ACCESS_KEY_ID", "ALIBABA_CLOUD_ACCESS_KEY_ID")
	secret := envValue(a.profile, "ALIBABA_CLOUD_ACCESS_KEY_SECRET", "ALIBABA_CLOUD_ACCESS_KEY_SECRET")
	token := envValue(a.profile, "ALIBABA_CLOUD_SECURITY_TOKEN", "ALIBABA_CLOUD_SECURITY_TOKEN")
	if access == "" || secret == "" {
		return nil, &provider.Error{Code: "missing_credentials", Operation: alibabaCostsOperation, Message: "Alibaba Cloud credential environment variables are not set"}
	}
	var client *bssopenapi.Client
	var err error
	if token != "" {
		client, err = bssopenapi.NewClientWithStsToken("cn-hangzhou", access, secret, token)
	} else {
		client, err = bssopenapi.NewClientWithAccessKey("cn-hangzhou", access, secret)
	}
	return client, alibabaClientError(alibabaCostsOperation, err)
}

func alibabaClientError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return &provider.Error{Code: "authentication_error", Operation: operation, Message: err.Error()}
}

func alibabaSourceError(operation string, err error) error {
	return &provider.Error{Code: "alibaba_api_error", Operation: operation, Message: err.Error(), Retryable: containsAny(strings.ToLower(err.Error()), "throttl", "timeout", "limit")}
}

func setAlibabaTimeout(ctx context.Context, request *requests.RpcRequest) {
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining > 0 {
			request.SetReadTimeout(remaining)
			request.SetConnectTimeout(minDuration(10*time.Second, remaining))
		}
	}
}

func alibabaMetricTime(point map[string]any) (time.Time, bool) {
	for _, key := range []string{"timestamp", "Timestamp"} {
		if value, ok := point[key]; ok {
			milliseconds, err := strconv.ParseInt(fmt.Sprint(value), 10, 64)
			if err == nil {
				return time.UnixMilli(milliseconds).UTC(), true
			}
		}
	}
	return time.Time{}, false
}

func alibabaMetricValue(point map[string]any) (float64, string, bool) {
	for _, key := range []string{"Average", "Value", "Sum", "Maximum", "Minimum"} {
		if value, ok := point[key]; ok {
			parsed, err := numericValue(value)
			if err == nil {
				return parsed, key, true
			}
		}
	}
	return 0, "", false
}

func alibabaStatisticName(name string) bool {
	for _, candidate := range []string{"Average", "Value", "Sum", "Maximum", "Minimum"} {
		if name == candidate {
			return true
		}
	}
	return false
}
