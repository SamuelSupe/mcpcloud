package providers

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/volcengine/volcengine-go-sdk/service/billing"
	"github.com/volcengine/volcengine-go-sdk/service/cloudmonitor"
	volc "github.com/volcengine/volcengine-go-sdk/volcengine"

	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

func (a *volcengineAdapter) queryMetrics(ctx context.Context, req provider.QueryRequest) (provider.Page, error) {
	start, end, err := rangeRequired(req, volcengineMetricsOperation)
	if err != nil {
		return provider.Page{}, err
	}
	if len(a.profile.Scopes.Accounts) != 1 {
		return provider.Page{}, &provider.Error{Code: "capability_unavailable", Operation: volcengineMetricsOperation, Message: "Volcengine metrics require exactly one configured account"}
	}
	accounts, err := restrictValues(a.profile.Scopes.Accounts, req.Accounts, volcengineMetricsOperation, "account")
	if err != nil {
		return provider.Page{}, err
	}
	selector, err := singleMetricSelector(req.Metrics, 3, 3, volcengineMetricsOperation)
	if err != nil {
		return provider.Page{}, err
	}
	if len(selector.Dimensions) == 0 {
		return provider.Page{}, &provider.Error{Code: "invalid_metric", Operation: volcengineMetricsOperation, Message: "Volcengine metric selectors require at least one instance dimension"}
	}
	region := req.Region
	if region == "" || region == "*" {
		region = "cn-beijing"
	}
	sess, err := a.session(region, volcengineMetricsOperation)
	if err != nil {
		return provider.Page{}, err
	}
	step := req.Step
	if step <= 0 {
		step = 5 * time.Minute
	}
	if step < 30*time.Second {
		step = 30 * time.Second
	}
	pageStart := start
	if req.PageToken != "" {
		cursor, err := decodePeriodCursor(req.PageToken)
		if err != nil || cursor.Offset <= 0 {
			return provider.Page{}, &provider.Error{Code: "invalid_cursor", Operation: volcengineMetricsOperation, Message: "invalid provider cursor"}
		}
		pageStart = time.Unix(int64(cursor.Offset), 0).UTC()
		if pageStart.Before(start) || !pageStart.Before(end) {
			return provider.Page{}, &provider.Error{Code: "invalid_cursor", Operation: volcengineMetricsOperation, Message: "provider cursor is outside the requested range"}
		}
	}
	limit := req.Limit
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	pageEnd := pageStart.Add(time.Duration(limit) * step)
	if pageEnd.After(end) {
		pageEnd = end
	}
	dimensions := make([]*cloudmonitor.DimensionForGetMetricDataInput, 0, len(selector.Dimensions))
	for name, value := range selector.Dimensions {
		dimensions = append(dimensions, &cloudmonitor.DimensionForGetMetricDataInput{Name: volc.String(name), Value: volc.String(value)})
	}
	input := &cloudmonitor.GetMetricDataInput{
		Namespace: volc.String(selector.Parts[0]), SubNamespace: volc.String(selector.Parts[1]), MetricName: volc.String(selector.Parts[2]),
		Instances: []*cloudmonitor.InstanceForGetMetricDataInput{{Dimensions: dimensions}},
		StartTime: volc.Int32(int32(pageStart.Unix())), EndTime: volc.Int32(int32(pageEnd.Unix())),
		Period: volc.String(fmt.Sprintf("%ds", int64(step.Seconds()))), StatisticsMethods: []*string{volc.String("Average")},
	}
	output, err := cloudmonitor.New(sess).GetMetricDataWithContext(ctx, input)
	if err != nil {
		return provider.Page{}, volcengineSourceError(volcengineMetricsOperation, err)
	}
	account := scopeTail(accounts[0])
	rows := make([]map[string]any, 0)
	if output.Data != nil {
		for _, series := range output.Data.MetricDataResults {
			if series == nil {
				continue
			}
			dimensionValues := map[string]string{}
			for _, dimension := range series.Dimensions {
				if dimension != nil {
					dimensionValues[deref(dimension.Name)] = deref(dimension.Value)
				}
			}
			for _, point := range series.DataPoints {
				if point == nil || point.Timestamp == nil || point.Value == nil {
					continue
				}
				row := metricRow(model.ProviderVolcengine, a.name, selector.Raw, "cloudmonitor", firstDimension(dimensionValues), region, account, time.Unix(int64(*point.Timestamp), 0), *point.Value, deref(output.Data.Unit), dimensionValues)
				native := row["native"].(map[string]any)
				native["namespace"] = selector.Parts[0]
				native["sub_namespace"] = selector.Parts[1]
				native["metric_name"] = selector.Parts[2]
				native["statistic"] = deref(series.StatisticsMethods)
				rows = append(rows, row)
			}
		}
	}
	next := ""
	if pageEnd.Before(end) {
		next = encodePeriodCursor(periodCursor{Offset: int(pageEnd.Unix())})
	}
	return provider.Page{Rows: rows, NextToken: next, Scanned: len(rows), Requests: 1}, nil
}

func (a *volcengineAdapter) queryCosts(ctx context.Context, req provider.QueryRequest) (provider.Page, error) {
	start, end, err := rangeRequired(req, volcengineCostsOperation)
	if err != nil {
		return provider.Page{}, err
	}
	cursor, err := decodePeriodCursor(req.PageToken)
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_cursor", Operation: volcengineCostsOperation, Message: err.Error()}
	}
	month := monthStart(start)
	if cursor.Period != "" {
		month, err = periodInRange(cursor.Period, start, end)
		if err != nil {
			return provider.Page{}, &provider.Error{Code: "invalid_cursor", Operation: volcengineCostsOperation, Message: err.Error()}
		}
	}
	limit := req.Limit
	if limit <= 0 || limit > 300 {
		limit = 300
	}
	region := "cn-beijing"
	if len(a.profile.Regions) > 0 && a.profile.Regions[0] != "*" {
		region = a.profile.Regions[0]
	}
	sess, err := a.session(region, volcengineCostsOperation)
	if err != nil {
		return provider.Page{}, err
	}
	limit32, offset32, needTotal, ignoreZero := int32(limit), int32(cursor.Offset), int32(1), int32(1)
	input := &billing.ListBillDetailInput{BillPeriod: volc.String(month.Format("2006-01")), Limit: &limit32, Offset: &offset32, NeedRecordNum: &needTotal, IgnoreZero: &ignoreZero}
	if len(req.Accounts) == 1 {
		account, err := strconv.ParseInt(scopeTail(req.Accounts[0]), 10, 64)
		if err != nil {
			return provider.Page{}, &provider.Error{Code: "invalid_scope", Operation: volcengineCostsOperation, Message: "Volcengine account scope must be numeric"}
		}
		input.OwnerID = []*int64{&account}
	}
	output, err := billing.New(sess).ListBillDetailWithContext(ctx, input)
	if err != nil {
		return provider.Page{}, volcengineSourceError(volcengineCostsOperation, err)
	}
	rows := make([]map[string]any, 0, len(output.List))
	for _, item := range output.List {
		if item == nil {
			continue
		}
		date, parsed := volcengineBillDate(deref(item.ExpenseDate), deref(item.ExpenseBeginTime), month)
		if parsed.Before(start) || !parsed.Before(end) {
			continue
		}
		amount, err := parseAmount(deref(item.PretaxAmount))
		if err != nil {
			return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: volcengineCostsOperation, Message: err.Error()}
		}
		service := deref(item.Product)
		if service == "" {
			service = deref(item.ProductZh)
		}
		account := deref(item.OwnerID)
		regionValue := deref(item.RegionCode)
		if regionValue == "" {
			regionValue = deref(item.Region)
		}
		id := deref(item.BillDetailId)
		if id == "" {
			id = strings.Join([]string{date, account, service, deref(item.InstanceNo)}, ":")
		}
		row := costRow(model.ProviderVolcengine, a.name, id, date, account, service, regionValue, amount, deref(item.Currency))
		native := row["native"].(map[string]any)
		native["bill_category"] = deref(item.BillCategory)
		native["billing_mode"] = deref(item.BillingMode)
		rows = append(rows, row)
	}
	next := ""
	if output.Total != nil && cursor.Offset+limit < int(*output.Total) {
		next = encodePeriodCursor(periodCursor{Period: month.Format("2006-01"), Offset: cursor.Offset + limit})
	} else if candidate := nextMonth(month); candidate.Before(end) {
		next = encodePeriodCursor(periodCursor{Period: candidate.Format("2006-01")})
	}
	return provider.Page{Rows: rows, NextToken: next, Scanned: len(output.List), Requests: 1}, nil
}

func volcengineBillDate(expenseDate, begin string, month time.Time) (string, time.Time) {
	for _, value := range []string{expenseDate, begin} {
		for _, layout := range []string{"2006-01-02", "2006-01-02 15:04:05", time.RFC3339} {
			if parsed, err := time.Parse(layout, value); err == nil {
				return parsed.UTC().Format("2006-01-02"), parsed.UTC()
			}
		}
	}
	return month.Format("2006-01-02"), month
}

func volcengineSourceError(operation string, err error) error {
	message := err.Error()
	return &provider.Error{Code: "volcengine_api_error", Operation: operation, Message: message, Retryable: containsAny(strings.ToLower(message), "throttl", "limit", "timeout")}
}
