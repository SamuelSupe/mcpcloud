package providers

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/volcengine/volcengine-go-sdk/service/resourcecenter"
	volc "github.com/volcengine/volcengine-go-sdk/volcengine"
	"github.com/volcengine/volcengine-go-sdk/volcengine/credentials"
	"github.com/volcengine/volcengine-go-sdk/volcengine/session"

	"mcpcloud/internal/config"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

const (
	volcengineResourcesOperation = "volcengine.resourcecenter.search_resources"
	volcengineMetricsOperation   = "volcengine.cloudmonitor.get_metric_data"
	volcengineCostsOperation     = "volcengine.billing.list_bill_detail"
)

type volcengineAdapter struct {
	name    string
	profile config.Profile
}

func init() {
	provider.RegisterFactory(model.ProviderVolcengine, func(name string, p config.Profile) (provider.Adapter, error) {
		return &volcengineAdapter{name: name, profile: p}, nil
	})
}
func (a *volcengineAdapter) Provider() model.Provider { return model.ProviderVolcengine }
func (a *volcengineAdapter) Profile() string          { return a.name }
func (a *volcengineAdapter) Capabilities() []provider.Capability {
	c := capabilities(model.ProviderVolcengine, volcengineResourcesOperation, volcengineMetricsOperation, volcengineCostsOperation)
	for i := range c {
		if c[i].Source == model.SourceMetrics && len(a.profile.Scopes.Accounts) != 1 {
			c[i].Status = "not_configured"
			c[i].Notes = "requires exactly one scopes.accounts entry because CloudMonitor does not select a target account"
		}
	}
	return c
}
func (a *volcengineAdapter) Operations() []provider.Operation {
	operations := []provider.Operation{operation(volcengineResourcesOperation, model.ProviderVolcengine, "resourcecenter", "Search Volcengine Resource Center", map[string]any{"resource_type": map[string]any{"type": "string"}, "resource_id": map[string]any{"type": "string"}, "region": map[string]any{"type": "string"}, "service": map[string]any{"type": "string"}, "project_name": map[string]any{"type": "string"}})}
	operations = append(operations, nativeProductOperations(model.ProviderVolcengine, "resourcecenter")...)
	operations = append(operations, instanceDetailOperations(model.ProviderVolcengine)...)
	return append(operations, deepDetailOperations(model.ProviderVolcengine)...)
}
func (a *volcengineAdapter) Readiness(context.Context) model.ProfileStatus {
	return readiness(a.name, model.ProviderVolcengine, a.profile, []string{"VOLCENGINE_ACCESS_KEY_ID", "VOLCENGINE_SECRET_ACCESS_KEY"})
}
func (a *volcengineAdapter) Query(ctx context.Context, req provider.QueryRequest) (provider.Page, error) {
	if req.Source == model.SourceMetrics {
		return a.queryMetrics(ctx, req)
	}
	if req.Source == model.SourceCosts {
		return a.queryCosts(ctx, req)
	}
	if req.Source != model.SourceResources && req.Source != model.SourceIAM {
		return provider.Page{}, unsupportedSource(req.Source, volcengineResourcesOperation)
	}
	if _, err := restrictValues(a.profile.Scopes.Accounts, req.Accounts, volcengineResourcesOperation, "account"); err != nil {
		return provider.Page{}, err
	}
	filters := map[string][]string{}
	if req.Region != "" && req.Region != "*" {
		filters["Region"] = []string{req.Region}
	}
	page, err := a.search(ctx, filters, req.PageToken, req.Limit)
	if req.Source == model.SourceIAM {
		page = domainPage(page, "iam")
	}
	return page, err
}
func (a *volcengineAdapter) NativeRead(ctx context.Context, req provider.NativeRequest) (provider.Page, error) {
	if detail, ok := instanceDetailFor(model.ProviderVolcengine, req.Operation); ok {
		return a.describeECSInstance(ctx, req, detail)
	}
	if detail, ok := deepDetailFor(model.ProviderVolcengine, req.Operation); ok {
		return a.readDeepDetail(ctx, req, detail)
	}
	product, productRead := nativeProductFor(model.ProviderVolcengine, req.Operation)
	if productRead {
		if err := validateNativeProductRequest(model.ProviderVolcengine, req.Operation, req.Params); err != nil {
			return provider.Page{}, err
		}
		baseRequest := req
		baseRequest.Operation = volcengineResourcesOperation
		page, err := a.NativeRead(ctx, baseRequest)
		return completeNativeProduct(page, err, product, req.Operation)
	}
	if req.Operation != volcengineResourcesOperation {
		return provider.Page{}, &provider.Error{Code: "operation_not_allowed", Operation: req.Operation, Message: "operation is not registered"}
	}
	filters := map[string][]string{}
	for param, key := range map[string]string{"resource_type": "ResourceType", "resource_id": "ResourceID", "region": "Region", "service": "Service", "project_name": "ProjectName"} {
		value, err := nativeString(req.Params, param)
		if err != nil {
			return provider.Page{}, err
		}
		if value != "" {
			filters[key] = []string{value}
		}
	}
	return a.search(ctx, filters, req.PageToken, req.Limit)
}

func (a *volcengineAdapter) search(ctx context.Context, filters map[string][]string, pageToken string, limit int) (provider.Page, error) {
	region := "cn-beijing"
	if len(a.profile.Regions) > 0 && a.profile.Regions[0] != "*" {
		region = a.profile.Regions[0]
	}
	sess, err := a.session(region, volcengineResourcesOperation)
	if err != nil {
		return provider.Page{}, err
	}
	client := resourcecenter.New(sess)
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	max := int32(limit)
	input := &resourcecenter.SearchResourcesInput{MaxResults: &max}
	if pageToken != "" {
		input.NextToken = &pageToken
	}
	for key, values := range filters {
		k, m := key, "Equals"
		pointers := make([]*string, 0, len(values))
		for _, value := range values {
			value := value
			pointers = append(pointers, &value)
		}
		input.Filter = append(input.Filter, &resourcecenter.FilterForSearchResourcesInput{Key: &k, MatchType: &m, Values: pointers})
	}
	output, err := client.SearchResourcesWithContext(ctx, input)
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "volcengine_api_error", Operation: volcengineResourcesOperation, Message: err.Error(), Retryable: containsAny(strings.ToLower(err.Error()), "throttl", "timeout")}
	}
	rows := make([]map[string]any, 0, len(output.Resources))
	now := time.Now().UTC()
	for _, resource := range output.Resources {
		if resource == nil {
			continue
		}
		rows = append(rows, a.row(resource, now))
	}
	next := ""
	if output.NextToken != nil {
		next = *output.NextToken
	}
	return provider.Page{Rows: rows, NextToken: next, Scanned: len(rows), Requests: 1}, nil
}

func (a *volcengineAdapter) session(region, operation string) (*session.Session, error) {
	cfg := volc.NewConfig().WithRegion(region).WithMaxRetries(0)
	if a.profile.Credential.Source == "env" {
		access := envValue(a.profile, "VOLCENGINE_ACCESS_KEY_ID", "VOLCENGINE_ACCESS_KEY_ID")
		secret := envValue(a.profile, "VOLCENGINE_SECRET_ACCESS_KEY", "VOLCENGINE_SECRET_ACCESS_KEY")
		token := envValue(a.profile, "VOLCENGINE_SESSION_TOKEN", "VOLCENGINE_SESSION_TOKEN")
		if access == "" || secret == "" {
			return nil, &provider.Error{Code: "missing_credentials", Operation: operation, Message: "Volcengine credential environment variables are not set"}
		}
		cfg = cfg.WithCredentials(credentials.NewStaticCredentials(access, secret, token))
	}
	sess, err := session.NewSession(cfg)
	if err != nil {
		return nil, &provider.Error{Code: "authentication_error", Operation: operation, Message: err.Error()}
	}
	return sess, nil
}

func (a *volcengineAdapter) row(resource *resourcecenter.ResourceForSearchResourcesOutput, observed time.Time) map[string]any {
	id, name, service, nativeType, region, account := "", "", "", "", "", ""
	if resource.ResourceID != nil {
		id = *resource.ResourceID
	}
	if resource.ResourceName != nil {
		name = *resource.ResourceName
	}
	if resource.Service != nil {
		service = *resource.Service
	}
	if resource.TypeName != nil {
		nativeType = *resource.TypeName
	} else if resource.ResourceType != nil {
		nativeType = *resource.ResourceType
	}
	if resource.Region != nil {
		region = *resource.Region
	}
	if resource.AccountID != nil {
		account = strconv.FormatInt(*resource.AccountID, 10)
	}
	row := baseRow(model.ProviderVolcengine, a.name, id, name, service, nativeType, region, account, observed)
	tags := map[string]any{}
	for _, tag := range resource.Tags {
		if tag != nil && tag.Key != nil && tag.Value != nil {
			tags[*tag.Key] = *tag.Value
		}
	}
	row["tags"] = tags
	attrs := row["attributes"].(map[string]any)
	attrs["project_name"] = deref(resource.ProjectName)
	attrs["public_ip_addresses"] = stringPointers(resource.PublicIpAddress)
	attrs["private_ip_addresses"] = stringPointers(resource.PrivateIpAddress)
	if resource.CreateTime != nil {
		for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05"} {
			if parsed, err := time.Parse(layout, *resource.CreateTime); err == nil {
				row["created_at"] = parsed.UTC().Format(time.RFC3339Nano)
				break
			}
		}
	}
	return row
}
func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
func stringPointers(values []*string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != nil {
			out = append(out, *value)
		}
	}
	return out
}
