package providers

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	awsbase "github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/resourceexplorer2"
	"github.com/aws/aws-sdk-go-v2/service/resourceexplorer2/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"mcpcloud/internal/config"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

const (
	awsInventoryOperation = "aws.resourceexplorer.search"
	awsMetricsOperation   = "aws.cloudwatch.get_metric_data"
	awsCostsOperation     = "aws.costexplorer.get_cost_and_usage"
)

var awsAccountPattern = regexp.MustCompile(`^[0-9]{12}$`)
var awsRegionPattern = regexp.MustCompile(`^[a-z0-9-]{3,64}$`)

type awsAdapter struct {
	name    string
	profile config.Profile
}

func init() {
	provider.RegisterFactory(model.ProviderAWS, func(name string, p config.Profile) (provider.Adapter, error) {
		for _, account := range p.Scopes.Accounts {
			if !awsAccountPattern.MatchString(account) {
				return nil, fmt.Errorf("AWS account scopes must be 12-digit account IDs")
			}
		}
		for _, region := range p.Regions {
			if region != "*" && !awsRegionPattern.MatchString(region) {
				return nil, fmt.Errorf("AWS regions must be exact region identifiers")
			}
		}
		return &awsAdapter{name: name, profile: p}, nil
	})
}

func (a *awsAdapter) Provider() model.Provider { return model.ProviderAWS }
func (a *awsAdapter) Profile() string          { return a.name }
func (a *awsAdapter) Capabilities() []provider.Capability {
	return capabilities(model.ProviderAWS, awsInventoryOperation, awsMetricsOperation, awsCostsOperation)
}
func (a *awsAdapter) Operations() []provider.Operation {
	operations := []provider.Operation{operation(awsInventoryOperation, model.ProviderAWS, "resourceexplorer2", "List indexed AWS resources using exact service and resource type filters", map[string]any{
		"service":       map[string]any{"type": "string"},
		"resource_type": map[string]any{"type": "string"},
	})}
	operations = append(operations, nativeProductOperations(model.ProviderAWS, "resourceexplorer2")...)
	operations = append(operations, instanceDetailOperations(model.ProviderAWS)...)
	return append(operations, deepDetailOperations(model.ProviderAWS)...)
}
func (a *awsAdapter) Readiness(context.Context) model.ProfileStatus {
	return readiness(a.name, model.ProviderAWS, a.profile, []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"})
}

func (a *awsAdapter) Query(ctx context.Context, req provider.QueryRequest) (provider.Page, error) {
	if req.Source == model.SourceMetrics {
		return a.queryMetrics(ctx, req)
	}
	if req.Source == model.SourceCosts {
		return a.queryCosts(ctx, req)
	}
	if req.Source != model.SourceResources && req.Source != model.SourceIAM {
		return provider.Page{}, unsupportedSource(req.Source, awsInventoryOperation)
	}
	accounts, err := restrictValues(a.profile.Scopes.Accounts, req.Accounts, awsInventoryOperation, "account")
	if err != nil {
		return provider.Page{}, err
	}
	for _, account := range accounts {
		if !awsAccountPattern.MatchString(account) {
			return provider.Page{}, &provider.Error{Code: "invalid_scope", Operation: awsInventoryOperation, Message: "AWS account scopes must be 12-digit account IDs"}
		}
	}
	query := "*"
	if req.Region != "" && req.Region != "*" {
		if !awsRegionPattern.MatchString(req.Region) {
			return provider.Page{}, &provider.Error{Code: "invalid_region", Operation: awsInventoryOperation, Message: "AWS region is invalid"}
		}
		query += " region:" + req.Region
	}
	if len(accounts) > 0 {
		query += " accountid:" + strings.Join(accounts, "|")
	}
	page, err := a.search(ctx, req.Region, query, req.PageToken, req.Limit)
	if req.Source == model.SourceIAM {
		page = domainPage(page, "iam")
	}
	return page, err
}

func (a *awsAdapter) NativeRead(ctx context.Context, req provider.NativeRequest) (provider.Page, error) {
	if detail, ok := instanceDetailFor(model.ProviderAWS, req.Operation); ok {
		return a.describeInstance(ctx, req, detail)
	}
	if detail, ok := deepDetailFor(model.ProviderAWS, req.Operation); ok {
		return a.readDeepDetail(ctx, req, detail)
	}
	product, productRead := nativeProductFor(model.ProviderAWS, req.Operation)
	if productRead {
		if err := validateNativeProductRequest(model.ProviderAWS, req.Operation, req.Params); err != nil {
			return provider.Page{}, err
		}
		baseRequest := req
		baseRequest.Operation = awsInventoryOperation
		page, err := a.NativeRead(ctx, baseRequest)
		return completeNativeProduct(page, err, product, req.Operation)
	}
	if req.Operation != awsInventoryOperation {
		return provider.Page{}, &provider.Error{Code: "operation_not_allowed", Operation: req.Operation, Message: "operation is not registered"}
	}
	service, err := nativeIdentifier(req.Params, "service")
	if err != nil {
		return provider.Page{}, err
	}
	resourceType, err := nativeIdentifier(req.Params, "resource_type")
	if err != nil {
		return provider.Page{}, err
	}
	query := "*"
	if service != "" {
		query += " service:" + service
	}
	if resourceType != "" {
		query += " resourcetype:" + resourceType
	}
	if req.Region != "" && req.Region != "*" {
		if !awsRegionPattern.MatchString(req.Region) {
			return provider.Page{}, &provider.Error{Code: "invalid_region", Operation: awsInventoryOperation, Message: "AWS region is invalid"}
		}
		query += " region:" + req.Region
	}
	if len(a.profile.Scopes.Accounts) > 0 {
		query += " accountid:" + strings.Join(a.profile.Scopes.Accounts, "|")
	}
	return a.search(ctx, req.Region, query, req.PageToken, req.Limit)
}

func (a *awsAdapter) search(ctx context.Context, region, query, pageToken string, limit int) (provider.Page, error) {
	if region == "" || region == "*" {
		if len(a.profile.Regions) > 0 && a.profile.Regions[0] != "*" {
			region = a.profile.Regions[0]
		} else {
			region = "us-east-1"
		}
	}
	cfg, err := a.sdkConfig(ctx, region, awsInventoryOperation)
	if err != nil {
		return provider.Page{}, err
	}
	client := resourceexplorer2.NewFromConfig(cfg)
	max := int32(limit)
	if max <= 0 || max > 1000 {
		max = 100
	}
	input := &resourceexplorer2.SearchInput{QueryString: &query, MaxResults: &max}
	if pageToken != "" {
		input.NextToken = &pageToken
	}
	output, err := client.Search(ctx, input)
	if err != nil {
		return provider.Page{}, classifyAWSError(err)
	}
	now := time.Now().UTC()
	rows := make([]map[string]any, 0, len(output.Resources))
	for _, resource := range output.Resources {
		rows = append(rows, a.awsRow(resource, now))
	}
	next := ""
	if output.NextToken != nil {
		next = *output.NextToken
	}
	return provider.Page{Rows: rows, NextToken: next, Scanned: len(rows), Requests: 1}, nil
}

func (a *awsAdapter) sdkConfig(ctx context.Context, region, operation string) (awsbase.Config, error) {
	options := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region), awsconfig.WithRetryMaxAttempts(1)}
	if a.profile.Credential.Source == "profile" && a.profile.Credential.Profile != "" {
		options = append(options, awsconfig.WithSharedConfigProfile(a.profile.Credential.Profile))
	}
	if a.profile.Credential.Source == "env" {
		access := envValue(a.profile, "AWS_ACCESS_KEY_ID", "AWS_ACCESS_KEY_ID")
		secret := envValue(a.profile, "AWS_SECRET_ACCESS_KEY", "AWS_SECRET_ACCESS_KEY")
		token := envValue(a.profile, "AWS_SESSION_TOKEN", "AWS_SESSION_TOKEN")
		if access == "" || secret == "" {
			return awsbase.Config{}, &provider.Error{Code: "missing_credentials", Operation: operation, Message: "AWS credential environment variables are not set"}
		}
		options = append(options, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(access, secret, token)))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return awsbase.Config{}, &provider.Error{Code: "authentication_error", Operation: operation, Message: err.Error()}
	}
	if a.profile.Credential.RoleARN != "" {
		assumeRole := stscreds.NewAssumeRoleProvider(sts.NewFromConfig(cfg), a.profile.Credential.RoleARN)
		cfg.Credentials = awsbase.NewCredentialsCache(assumeRole)
	}
	return cfg, nil
}

func (a *awsAdapter) awsRow(resource types.Resource, observed time.Time) map[string]any {
	id, name, service, nativeType, region, account := "", "", "", "", "", ""
	if resource.Arn != nil {
		id = *resource.Arn
		name = lastName(id)
	}
	if resource.Service != nil {
		service = *resource.Service
	}
	if resource.CfnResourceType != nil {
		nativeType = *resource.CfnResourceType
	} else if resource.ResourceType != nil {
		nativeType = *resource.ResourceType
	}
	if resource.Region != nil {
		region = *resource.Region
	}
	if resource.OwningAccountId != nil {
		account = *resource.OwningAccountId
	}
	if resource.LastReportedAt != nil {
		observed = *resource.LastReportedAt
	}
	row := baseRow(model.ProviderAWS, a.name, id, name, service, nativeType, region, account, observed)
	native := row["native"].(map[string]any)
	native["arn"] = id
	native["cfn_resource_type"] = nativeType
	return row
}

func classifyAWSError(err error) error {
	message := err.Error()
	retryable := strings.Contains(strings.ToLower(message), "throttl") || strings.Contains(strings.ToLower(message), "timeout")
	return &provider.Error{Code: "aws_api_error", Operation: awsInventoryOperation, Message: fmt.Sprint(message), Retryable: retryable}
}
