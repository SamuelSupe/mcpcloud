package providers

import (
	"context"
	"strings"
	"time"

	"github.com/huaweicloud/huaweicloud-sdk-go-v3/core/auth/basic"
	"github.com/huaweicloud/huaweicloud-sdk-go-v3/core/auth/global"
	httpconfig "github.com/huaweicloud/huaweicloud-sdk-go-v3/core/config"
	bss "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/bss/v2"
	bssmodel "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/bss/v2/model"
	bssregion "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/bss/v2/region"
	bssintl "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/bssintl/v2"
	bssintlmodel "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/bssintl/v2/model"
	bssintlregion "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/bssintl/v2/region"
	ces "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/ces/v1"
	cesmodel "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/ces/v1/model"
	cesregion "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/ces/v1/region"

	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

func (a *huaweiAdapter) queryMetrics(ctx context.Context, req provider.QueryRequest) (provider.Page, error) {
	start, end, err := rangeRequired(req, huaweiMetricsOperation)
	if err != nil {
		return provider.Page{}, err
	}
	if len(a.profile.Scopes.Projects) == 0 {
		return provider.Page{}, &provider.Error{Code: "capability_unavailable", Operation: huaweiMetricsOperation, Message: "Huawei CES metrics require a configured project"}
	}
	projects, err := restrictValues(a.profile.Scopes.Projects, req.Accounts, huaweiMetricsOperation, "project")
	if err != nil {
		return provider.Page{}, err
	}
	if len(projects) != 1 {
		return provider.Page{}, &provider.Error{Code: "invalid_scope", Operation: huaweiMetricsOperation, Message: "Huawei CES metrics require exactly one project target"}
	}
	selector, err := singleMetricSelector(req.Metrics, 2, 2, huaweiMetricsOperation)
	if err != nil {
		return provider.Page{}, err
	}
	if len(selector.Dimensions) == 0 || len(selector.Dimensions) > 4 {
		return provider.Page{}, &provider.Error{Code: "invalid_metric", Operation: huaweiMetricsOperation, Message: "Huawei CES metric selectors require one to four dimensions"}
	}
	regionID := req.Region
	if regionID == "" || regionID == "*" {
		regionID = "cn-north-4"
	}
	region, err := cesregion.SafeValueOf(regionID)
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_region", Operation: huaweiMetricsOperation, Message: err.Error()}
	}
	projectID := scopeTail(projects[0])
	credential, err := a.huaweiBasicCredential(huaweiMetricsOperation, projectID)
	if err != nil {
		return provider.Page{}, err
	}
	hc, err := ces.CesClientBuilder().WithRegion(region).WithCredential(credential).WithHttpConfig(huaweiHTTPConfig(ctx)).SafeBuild()
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "authentication_error", Operation: huaweiMetricsOperation, Message: err.Error()}
	}
	period, periodSeconds, maximumRange := huaweiMetricPeriod(req.Step)
	pageStart := start
	if req.PageToken != "" {
		cursor, err := decodePeriodCursor(req.PageToken)
		if err != nil || cursor.Offset <= 0 {
			return provider.Page{}, &provider.Error{Code: "invalid_cursor", Operation: huaweiMetricsOperation, Message: "invalid provider cursor"}
		}
		pageStart = time.Unix(int64(cursor.Offset), 0).UTC()
		if pageStart.Before(start) || !pageStart.Before(end) {
			return provider.Page{}, &provider.Error{Code: "invalid_cursor", Operation: huaweiMetricsOperation, Message: "provider cursor is outside the requested range"}
		}
	}
	limit := req.Limit
	if limit <= 0 || limit > 3000 {
		limit = 500
	}
	pageRange := time.Duration(periodSeconds*int64(limit)) * time.Second
	if pageRange > maximumRange {
		pageRange = maximumRange
	}
	pageEnd := pageStart.Add(pageRange)
	if pageEnd.After(end) {
		pageEnd = end
	}
	dimensions := make([]cesmodel.MetricsDimension, 0, len(selector.Dimensions))
	for name, value := range selector.Dimensions {
		dimensions = append(dimensions, cesmodel.MetricsDimension{Name: name, Value: value})
	}
	filter := cesmodel.GetFilterEnum().AVERAGE
	request := &cesmodel.BatchListMetricDataRequest{Body: &cesmodel.BatchListMetricDataRequestBody{
		Metrics: []cesmodel.MetricInfo{{Namespace: selector.Parts[0], MetricName: selector.Parts[1], Dimensions: dimensions}},
		Period:  &period, Filter: &filter, From: pageStart.UnixMilli(), To: pageEnd.UnixMilli(),
	}}
	response, err := ces.NewCesClient(hc).BatchListMetricData(request)
	if err != nil {
		return provider.Page{}, huaweiSourceError(huaweiMetricsOperation, err)
	}
	account := ""
	if len(req.Accounts) == 1 {
		account = req.Accounts[0]
	}
	rows := make([]map[string]any, 0)
	if response.Metrics != nil {
		for _, metric := range *response.Metrics {
			metricDimensions := map[string]string{}
			if metric.Dimensions != nil {
				for _, dimension := range *metric.Dimensions {
					metricDimensions[deref(dimension.Name)] = deref(dimension.Value)
				}
			}
			resourceID := firstDimension(metricDimensions)
			unit := deref(metric.Unit)
			for _, point := range metric.Datapoints {
				value, statistic, ok := huaweiMetricValue(point)
				if !ok {
					continue
				}
				row := metricRow(model.ProviderHuawei, a.name, selector.Raw, "ces", resourceID, regionID, account, time.UnixMilli(point.Timestamp), value, unit, metricDimensions)
				native := row["native"].(map[string]any)
				native["namespace"] = deref(metric.Namespace)
				native["metric_name"] = metric.MetricName
				native["statistic"] = statistic
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

func (a *huaweiAdapter) queryCosts(ctx context.Context, req provider.QueryRequest) (provider.Page, error) {
	start, end, err := rangeRequired(req, huaweiCostsOperation)
	if err != nil {
		return provider.Page{}, err
	}
	if a.profile.Options["billing_site"] == "" {
		return provider.Page{}, &provider.Error{Code: "capability_unavailable", Operation: huaweiCostsOperation, Message: "options.billing_site is not configured"}
	}
	cursor, err := decodePeriodCursor(req.PageToken)
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_cursor", Operation: huaweiCostsOperation, Message: err.Error()}
	}
	limit := req.Limit
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	offset := cursor.Offset
	if a.profile.Options["billing_site"] == "intl" {
		return a.queryIntlCosts(ctx, req, start, end, offset, limit)
	}
	credential, err := a.huaweiGlobalCredential(huaweiCostsOperation)
	if err != nil {
		return provider.Page{}, err
	}
	hc, err := bss.BssClientBuilder().WithRegion(bssregion.CN_NORTH_1).WithCredential(credential).WithHttpConfig(huaweiHTTPConfig(ctx)).SafeBuild()
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "authentication_error", Operation: huaweiCostsOperation, Message: err.Error()}
	}
	offset32, limit32 := int32(offset), int32(limit)
	body := &bssmodel.ListCostsReq{
		TimeCondition: &bssmodel.TimeCondition{TimeMeasureId: 1, BeginTime: start.Format("2006-01-02"), EndTime: end.Add(-time.Nanosecond).Format("2006-01-02")},
		Groupby:       []bssmodel.GroupBy{{Type: "dimension", Key: "CLOUD_SERVICE_TYPE"}, {Type: "dimension", Key: "ASSOCIATED_ACCOUNT"}, {Type: "dimension", Key: "REGION_CODE"}},
		CostType:      "ORIGINAL_COST", AmountType: "NET_AMOUNT", Offset: &offset32, Limit: &limit32,
	}
	if len(req.Accounts) == 1 {
		body.Filters = &[]bssmodel.FilterV2{{Operator: 0, FilterFactor: &bssmodel.FilterFactor{Key: "ASSOCIATED_ACCOUNT", Value: []string{req.Accounts[0]}}}}
	}
	response, err := bss.NewBssClient(hc).ListCosts(&bssmodel.ListCostsRequest{Body: body})
	if err != nil {
		return provider.Page{}, huaweiSourceError(huaweiCostsOperation, err)
	}
	groups := huaweiChinaCostGroups(response.CostData)
	rows, err := a.huaweiCostRows(groups, deref(response.Currency))
	if err != nil {
		return provider.Page{}, err
	}
	next := huaweiCostNext(offset, limit, response.TotalCount)
	return provider.Page{Rows: rows, NextToken: next, Scanned: len(groups), Requests: 1}, nil
}

func (a *huaweiAdapter) queryIntlCosts(ctx context.Context, req provider.QueryRequest, start, end time.Time, offset, limit int) (provider.Page, error) {
	credential, err := a.huaweiGlobalCredential(huaweiCostsOperation)
	if err != nil {
		return provider.Page{}, err
	}
	region := bssintlregion.AP_SOUTHEAST_1
	if len(a.profile.Regions) > 0 && a.profile.Regions[0] == "eu-west-101" {
		region = bssintlregion.EU_WEST_101
	}
	hc, err := bssintl.BssintlClientBuilder().WithRegion(region).WithCredential(credential).WithHttpConfig(huaweiHTTPConfig(ctx)).SafeBuild()
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "authentication_error", Operation: huaweiCostsOperation, Message: err.Error()}
	}
	offset32, limit32 := int32(offset), int32(limit)
	body := &bssintlmodel.ListCostsReq{
		TimeCondition: &bssintlmodel.TimeCondition{TimeMeasureId: 1, BeginTime: start.Format("2006-01-02"), EndTime: end.Add(-time.Nanosecond).Format("2006-01-02")},
		Groupby:       []bssintlmodel.GroupBy{{Type: "dimension", Key: "CLOUD_SERVICE_TYPE"}, {Type: "dimension", Key: "ASSOCIATED_ACCOUNT"}, {Type: "dimension", Key: "REGION_CODE"}},
		CostType:      "ORIGINAL_COST", AmountType: "NET_AMOUNT", Offset: &offset32, Limit: &limit32,
	}
	if len(req.Accounts) == 1 {
		body.Filters = &[]bssintlmodel.FilterV2{{Operator: 0, FilterFactor: &bssintlmodel.FilterFactor{Key: "ASSOCIATED_ACCOUNT", Value: []string{req.Accounts[0]}}}}
	}
	response, err := bssintl.NewBssintlClient(hc).ListCosts(&bssintlmodel.ListCostsRequest{Body: body})
	if err != nil {
		return provider.Page{}, huaweiSourceError(huaweiCostsOperation, err)
	}
	groups := huaweiIntlCostGroups(response.CostData)
	rows, err := a.huaweiCostRows(groups, deref(response.Currency))
	if err != nil {
		return provider.Page{}, err
	}
	next := huaweiCostNext(offset, limit, response.TotalCount)
	return provider.Page{Rows: rows, NextToken: next, Scanned: len(groups), Requests: 1}, nil
}

type huaweiCostGroup struct {
	Dimensions map[string]string
	Costs      []huaweiCost
}

type huaweiCost struct {
	Date   string
	Amount string
}

func huaweiChinaCostGroups(items *[]bssmodel.CostDataByDimension) []huaweiCostGroup {
	if items == nil {
		return nil
	}
	groups := make([]huaweiCostGroup, 0, len(*items))
	for _, item := range *items {
		group := huaweiCostGroup{Dimensions: map[string]string{}}
		if item.Dimensions != nil {
			for _, dimension := range *item.Dimensions {
				group.Dimensions[deref(dimension.Key)] = deref(dimension.Value)
			}
		}
		if item.Costs != nil {
			for _, cost := range *item.Costs {
				group.Costs = append(group.Costs, huaweiCost{Date: deref(cost.TimeDimensionValue), Amount: deref(cost.Amount)})
			}
		}
		groups = append(groups, group)
	}
	return groups
}

func huaweiIntlCostGroups(items *[]bssintlmodel.CostDataByDimension) []huaweiCostGroup {
	if items == nil {
		return nil
	}
	groups := make([]huaweiCostGroup, 0, len(*items))
	for _, item := range *items {
		group := huaweiCostGroup{Dimensions: map[string]string{}}
		if item.Dimensions != nil {
			for _, dimension := range *item.Dimensions {
				group.Dimensions[deref(dimension.Key)] = deref(dimension.Value)
			}
		}
		if item.Costs != nil {
			for _, cost := range *item.Costs {
				group.Costs = append(group.Costs, huaweiCost{Date: deref(cost.TimeDimensionValue), Amount: deref(cost.Amount)})
			}
		}
		groups = append(groups, group)
	}
	return groups
}

func (a *huaweiAdapter) huaweiCostRows(groups []huaweiCostGroup, currency string) ([]map[string]any, error) {
	rows := make([]map[string]any, 0)
	for _, group := range groups {
		service := group.Dimensions["CLOUD_SERVICE_TYPE"]
		account := group.Dimensions["ASSOCIATED_ACCOUNT"]
		region := group.Dimensions["REGION_CODE"]
		for _, cost := range group.Costs {
			amount, err := parseAmount(cost.Amount)
			if err != nil {
				return nil, &provider.Error{Code: "invalid_provider_response", Operation: huaweiCostsOperation, Message: err.Error()}
			}
			id := strings.Join([]string{cost.Date, account, service, region}, ":")
			row := costRow(model.ProviderHuawei, a.name, id, cost.Date, account, service, region, amount, currency)
			native := row["native"].(map[string]any)
			native["cost_type"] = "ORIGINAL_COST"
			native["amount_type"] = "NET_AMOUNT"
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func (a *huaweiAdapter) huaweiBasicCredential(operation, projectID string) (*basic.Credentials, error) {
	ak := envValue(a.profile, "HUAWEICLOUD_SDK_AK", "HUAWEICLOUD_SDK_AK")
	sk := envValue(a.profile, "HUAWEICLOUD_SDK_SK", "HUAWEICLOUD_SDK_SK")
	if ak == "" || sk == "" {
		return nil, &provider.Error{Code: "missing_credentials", Operation: operation, Message: "Huawei Cloud credential environment variables are not set"}
	}
	builder := basic.NewCredentialsBuilder().WithAk(ak).WithSk(sk).WithSecurityToken(envValue(a.profile, "HUAWEICLOUD_SDK_SECURITY_TOKEN", "HUAWEICLOUD_SDK_SECURITY_TOKEN"))
	if projectID != "" {
		builder = builder.WithProjectId(projectID)
	}
	credential, err := builder.SafeBuild()
	if err != nil {
		return nil, &provider.Error{Code: "authentication_error", Operation: operation, Message: err.Error()}
	}
	return credential, nil
}

func (a *huaweiAdapter) huaweiGlobalCredential(operation string) (*global.Credentials, error) {
	ak := envValue(a.profile, "HUAWEICLOUD_SDK_AK", "HUAWEICLOUD_SDK_AK")
	sk := envValue(a.profile, "HUAWEICLOUD_SDK_SK", "HUAWEICLOUD_SDK_SK")
	if ak == "" || sk == "" {
		return nil, &provider.Error{Code: "missing_credentials", Operation: operation, Message: "Huawei Cloud credential environment variables are not set"}
	}
	builder := global.NewCredentialsBuilder().WithAk(ak).WithSk(sk).WithSecurityToken(envValue(a.profile, "HUAWEICLOUD_SDK_SECURITY_TOKEN", "HUAWEICLOUD_SDK_SECURITY_TOKEN"))
	if domain := envValue(a.profile, "HUAWEICLOUD_DOMAIN_ID", "HUAWEICLOUD_DOMAIN_ID"); domain != "" {
		builder = builder.WithDomainId(domain)
	}
	credential, err := builder.SafeBuild()
	if err != nil {
		return nil, &provider.Error{Code: "authentication_error", Operation: operation, Message: err.Error()}
	}
	return credential, nil
}

func huaweiHTTPConfig(ctx context.Context) *httpconfig.HttpConfig {
	timeout := 120 * time.Second
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) > 0 {
		timeout = time.Until(deadline)
	}
	return httpconfig.DefaultHttpConfig().WithTimeout(timeout).WithRetries(0)
}

func huaweiMetricPeriod(step time.Duration) (cesmodel.BatchPeriod, int64, time.Duration) {
	seconds := int64(step.Seconds())
	values := cesmodel.GetBatchPeriodEnum()
	switch {
	case seconds <= 60:
		return values.E_60, 60, 24 * time.Hour
	case seconds <= 300:
		return values.E_300, 300, 24 * time.Hour
	case seconds <= 1200:
		return values.E_1200, 1200, 3 * 24 * time.Hour
	case seconds <= 3600:
		return values.E_3600, 3600, 10 * 24 * time.Hour
	case seconds <= 14400:
		return values.E_14400, 14400, 30 * 24 * time.Hour
	default:
		return values.E_86400, 86400, 180 * 24 * time.Hour
	}
}

func huaweiMetricValue(point cesmodel.DatapointForBatchMetric) (float64, string, bool) {
	for index, value := range []*float64{point.Average, point.Sum, point.Max, point.Min, point.Variance} {
		if value != nil {
			return *value, []string{"average", "sum", "max", "min", "variance"}[index], true
		}
	}
	return 0, "", false
}

func huaweiCostNext(offset, limit int, total *int32) string {
	if total != nil && offset+limit < int(*total) {
		return encodePeriodCursor(periodCursor{Offset: offset + limit})
	}
	return ""
}

func huaweiSourceError(operation string, err error) error {
	message := err.Error()
	return &provider.Error{Code: "huawei_api_error", Operation: operation, Message: message, Retryable: containsAny(strings.ToLower(message), "throttl", "timeout", "429")}
}
