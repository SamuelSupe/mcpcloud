package providers

import (
	"context"
	"fmt"
	"sort"
	"strings"

	awsbase "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cloudwatchtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	costtypes "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"

	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

func (a *awsAdapter) queryMetrics(ctx context.Context, req provider.QueryRequest) (provider.Page, error) {
	start, end, err := rangeRequired(req, awsMetricsOperation)
	if err != nil {
		return provider.Page{}, err
	}
	region := req.Region
	if region == "" || region == "*" {
		if len(a.profile.Regions) > 0 && a.profile.Regions[0] != "*" {
			region = a.profile.Regions[0]
		} else {
			region = "us-east-1"
		}
	}
	cfg, err := a.sdkConfig(ctx, region, awsMetricsOperation)
	if err != nil {
		return provider.Page{}, err
	}
	period := int32(req.Step.Seconds())
	if period <= 0 {
		period = 300
	}
	if period < 60 {
		period = 60
	}
	queries := make([]cloudwatchtypes.MetricDataQuery, 0, len(req.Metrics))
	selectors := make(map[string]metricSelector, len(req.Metrics))
	account := ""
	if len(req.Accounts) == 1 {
		account = req.Accounts[0]
	}
	for index, raw := range req.Metrics {
		selector, err := parseMetricSelector(raw, 2, 2)
		if err != nil {
			return provider.Page{}, &provider.Error{Code: "invalid_metric", Operation: awsMetricsOperation, Message: err.Error()}
		}
		dimensions := make([]cloudwatchtypes.Dimension, 0, len(selector.Dimensions))
		keys := make([]string, 0, len(selector.Dimensions))
		for name := range selector.Dimensions {
			keys = append(keys, name)
		}
		sort.Strings(keys)
		for _, name := range keys {
			value := selector.Dimensions[name]
			dimensions = append(dimensions, cloudwatchtypes.Dimension{Name: awsbase.String(name), Value: awsbase.String(value)})
		}
		id := fmt.Sprintf("m%d", index)
		selectors[id] = selector
		query := cloudwatchtypes.MetricDataQuery{
			Id: awsbase.String(id),
			MetricStat: &cloudwatchtypes.MetricStat{
				Metric: &cloudwatchtypes.Metric{Namespace: awsbase.String(selector.Parts[0]), MetricName: awsbase.String(selector.Parts[1]), Dimensions: dimensions},
				Period: awsbase.Int32(period), Stat: awsbase.String("Average"),
			},
			ReturnData: awsbase.Bool(true),
		}
		if account != "" {
			query.AccountId = awsbase.String(account)
		}
		queries = append(queries, query)
	}
	maxPoints := int32(req.Limit)
	if maxPoints <= 0 || maxPoints > 100800 {
		maxPoints = 500
	}
	input := &cloudwatch.GetMetricDataInput{StartTime: &start, EndTime: &end, MetricDataQueries: queries, MaxDatapoints: &maxPoints, ScanBy: cloudwatchtypes.ScanByTimestampAscending}
	if req.PageToken != "" {
		input.NextToken = awsbase.String(req.PageToken)
	}
	output, err := cloudwatch.NewFromConfig(cfg).GetMetricData(ctx, input)
	if err != nil {
		return provider.Page{}, awsSourceError(awsMetricsOperation, err)
	}
	var rows []map[string]any
	for _, result := range output.MetricDataResults {
		if result.Id == nil {
			continue
		}
		selector, ok := selectors[*result.Id]
		if !ok {
			continue
		}
		for i := 0; i < len(result.Timestamps) && i < len(result.Values); i++ {
			resourceID := firstDimension(selector.Dimensions)
			row := metricRow(model.ProviderAWS, a.name, selector.Raw, "cloudwatch", resourceID, region, account, result.Timestamps[i], result.Values[i], "", selector.Dimensions)
			native := row["native"].(map[string]any)
			native["namespace"] = selector.Parts[0]
			native["metric_name"] = selector.Parts[1]
			native["statistic"] = "Average"
			rows = append(rows, row)
		}
	}
	next := ""
	if output.NextToken != nil {
		next = *output.NextToken
	}
	return provider.Page{Rows: rows, NextToken: next, Scanned: len(rows), Requests: 1}, nil
}

func (a *awsAdapter) queryCosts(ctx context.Context, req provider.QueryRequest) (provider.Page, error) {
	start, end, err := rangeRequired(req, awsCostsOperation)
	if err != nil {
		return provider.Page{}, err
	}
	cfg, err := a.sdkConfig(ctx, "us-east-1", awsCostsOperation)
	if err != nil {
		return provider.Page{}, err
	}
	serviceKey, accountKey := "SERVICE", "LINKED_ACCOUNT"
	input := &costexplorer.GetCostAndUsageInput{
		TimePeriod:  &costtypes.DateInterval{Start: awsbase.String(start.Format("2006-01-02")), End: awsbase.String(end.Format("2006-01-02"))},
		Granularity: costtypes.GranularityDaily,
		Metrics:     []string{"UnblendedCost"},
		GroupBy:     []costtypes.GroupDefinition{{Type: costtypes.GroupDefinitionTypeDimension, Key: &serviceKey}, {Type: costtypes.GroupDefinitionTypeDimension, Key: &accountKey}},
	}
	if len(req.Accounts) > 0 {
		input.Filter = &costtypes.Expression{Dimensions: &costtypes.DimensionValues{Key: costtypes.DimensionLinkedAccount, Values: append([]string(nil), req.Accounts...)}}
	}
	if req.PageToken != "" {
		input.NextPageToken = awsbase.String(req.PageToken)
	}
	output, err := costexplorer.NewFromConfig(cfg).GetCostAndUsage(ctx, input)
	if err != nil {
		return provider.Page{}, awsSourceError(awsCostsOperation, err)
	}
	var rows []map[string]any
	for _, result := range output.ResultsByTime {
		date := ""
		if result.TimePeriod != nil && result.TimePeriod.Start != nil {
			date = *result.TimePeriod.Start
		}
		for _, group := range result.Groups {
			service, account := "", ""
			if len(group.Keys) > 0 {
				service = group.Keys[0]
			}
			if len(group.Keys) > 1 {
				account = group.Keys[1]
			}
			value, ok := group.Metrics["UnblendedCost"]
			if !ok || value.Amount == nil || value.Unit == nil {
				continue
			}
			amount, err := parseAmount(*value.Amount)
			if err != nil {
				return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: awsCostsOperation, Message: err.Error()}
			}
			id := strings.Join([]string{date, account, service}, ":")
			row := costRow(model.ProviderAWS, a.name, id, date, account, service, "", amount, *value.Unit)
			native := row["native"].(map[string]any)
			native["metric"] = "UnblendedCost"
			rows = append(rows, row)
		}
	}
	next := ""
	if output.NextPageToken != nil {
		next = *output.NextPageToken
	}
	return provider.Page{Rows: rows, NextToken: next, Scanned: len(rows), Requests: 1}, nil
}

func awsSourceError(operation string, err error) error {
	message := err.Error()
	return &provider.Error{Code: "aws_api_error", Operation: operation, Message: message, Retryable: containsAny(strings.ToLower(message), "throttl", "timeout", "limitexceeded")}
}

func firstDimension(dimensions map[string]string) string {
	for _, name := range []string{"InstanceId", "instance_id", "resource_id", "ResourceId"} {
		if value := dimensions[name]; value != "" {
			return value
		}
	}
	return ""
}
