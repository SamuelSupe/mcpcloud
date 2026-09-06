package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"

	"mcpcloud/internal/config"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

const (
	azureResourcesOperation = "azure.resourcegraph.resources"
	azureMetricsOperation   = "azure.monitor.metrics.list_subscription_scope"
	azureCostsOperation     = "azure.costmanagement.query"
)

type azureAdapter struct {
	name    string
	profile config.Profile
}

var newAzureClientSecretCredential = azidentity.NewClientSecretCredential

func init() {
	provider.RegisterFactory(model.ProviderAzure, func(name string, p config.Profile) (provider.Adapter, error) {
		return &azureAdapter{name: name, profile: p}, nil
	})
}
func (a *azureAdapter) Provider() model.Provider { return model.ProviderAzure }
func (a *azureAdapter) Profile() string          { return a.name }
func (a *azureAdapter) Capabilities() []provider.Capability {
	c := capabilities(model.ProviderAzure, azureResourcesOperation, azureMetricsOperation, azureCostsOperation)
	for i := range c {
		if c[i].Source == model.SourceIAM {
			c[i].Status = "available"
		}
		if c[i].Source == model.SourceMetrics && !hasExactRegion(a.profile.Regions) {
			c[i].Status = "not_configured"
			c[i].Notes = "requires at least one exact profile region for the subscription-scope metrics API"
		}
	}
	return c
}
func (a *azureAdapter) Operations() []provider.Operation {
	operations := []provider.Operation{operation(azureResourcesOperation, model.ProviderAzure, "resourcegraph", "List Azure resources using exact typed filters", map[string]any{
		"subscriptions":  map[string]any{"type": "array", "items": "string"},
		"resource_type":  map[string]any{"type": "string"},
		"resource_group": map[string]any{"type": "string"},
		"name":           map[string]any{"type": "string"},
		"location":       map[string]any{"type": "string"},
	})}
	operations = append(operations, nativeProductOperations(model.ProviderAzure, "resourcegraph")...)
	operations = append(operations, instanceDetailOperations(model.ProviderAzure)...)
	return append(operations, deepDetailOperations(model.ProviderAzure)...)
}
func (a *azureAdapter) Readiness(context.Context) model.ProfileStatus {
	return readiness(a.name, model.ProviderAzure, a.profile, []string{"AZURE_TENANT_ID", "AZURE_CLIENT_ID", "AZURE_CLIENT_SECRET"})
}

func (a *azureAdapter) Query(ctx context.Context, req provider.QueryRequest) (provider.Page, error) {
	if req.Source == model.SourceMetrics {
		return a.queryMetrics(ctx, req)
	}
	if req.Source == model.SourceCosts {
		return a.queryCosts(ctx, req)
	}
	var query string
	switch req.Source {
	case model.SourceResources:
		query = "Resources"
	case model.SourceIAM:
		query = "AuthorizationResources"
	default:
		return provider.Page{}, unsupportedSource(req.Source, azureResourcesOperation)
	}
	if req.Region != "" && req.Region != "*" {
		query += fmt.Sprintf(" | where location =~ '%s'", escapeKQL(req.Region))
	}
	query += " | project id, name, type, location, subscriptionId, resourceGroup, tags, properties"
	subscriptions, err := restrictValues(a.profile.Scopes.Subscriptions, req.Accounts, azureResourcesOperation, "subscription")
	if err != nil {
		return provider.Page{}, err
	}
	return a.graph(ctx, query, subscriptions, req.PageToken, req.Limit)
}

func (a *azureAdapter) NativeRead(ctx context.Context, req provider.NativeRequest) (provider.Page, error) {
	if detail, ok := instanceDetailFor(model.ProviderAzure, req.Operation); ok {
		return a.getVirtualMachineDetail(ctx, req, detail)
	}
	if detail, ok := deepDetailFor(model.ProviderAzure, req.Operation); ok {
		return a.readDeepDetail(ctx, req, detail)
	}
	product, productRead := nativeProductFor(model.ProviderAzure, req.Operation)
	if req.Operation != azureResourcesOperation && !productRead {
		return provider.Page{}, &provider.Error{Code: "operation_not_allowed", Operation: req.Operation, Message: "operation is not registered"}
	}
	if productRead {
		if err := validateNativeProductRequest(model.ProviderAzure, req.Operation, req.Params); err != nil {
			return provider.Page{}, err
		}
	}
	subscriptions := a.profile.Scopes.Subscriptions
	if raw, ok := req.Params["subscriptions"]; ok {
		_ = raw
		requested, err := nativeStrings(req.Params, "subscriptions")
		if err != nil {
			if productRead {
				err = attributeNativeProductError(err, req.Operation)
			}
			return provider.Page{}, err
		}
		subscriptions, err = restrictValues(a.profile.Scopes.Subscriptions, requested, azureResourcesOperation, "subscription")
		if err != nil {
			if productRead {
				err = attributeNativeProductError(err, req.Operation)
			}
			return provider.Page{}, err
		}
	}
	query := "Resources"
	if productRead && product.source == model.SourceIAM {
		query = "AuthorizationResources"
	}
	for param, field := range map[string]string{
		"resource_type": "type",
		"location":      "location",
	} {
		value, err := nativeIdentifier(req.Params, param)
		if err != nil {
			if productRead {
				err = attributeNativeProductError(err, req.Operation)
			}
			return provider.Page{}, err
		}
		if value != "" {
			if param == "location" && len(a.profile.Regions) > 0 && !stringIn(a.profile.Regions, "*") && !stringIn(a.profile.Regions, value) {
				err := &provider.Error{Code: "scope_not_allowed", Operation: azureResourcesOperation, Message: "location is outside the profile allowlist"}
				if productRead {
					return provider.Page{}, attributeNativeProductError(err, req.Operation)
				}
				return provider.Page{}, err
			}
			query += fmt.Sprintf(" | where %s =~ '%s'", field, escapeKQL(value))
		}
	}
	for param, field := range map[string]string{"resource_group": "resourceGroup", "name": "name"} {
		value, err := nativeLiteral(req.Params, param)
		if err != nil {
			if productRead {
				err = attributeNativeProductError(err, req.Operation)
			}
			return provider.Page{}, err
		}
		if value != "" {
			query += fmt.Sprintf(" | where %s =~ '%s'", field, escapeKQL(value))
		}
	}
	query += " | project id, name, type, location, subscriptionId, resourceGroup, tags, properties"
	page, err := a.graph(ctx, query, subscriptions, req.PageToken, req.Limit)
	if productRead {
		return completeNativeProduct(page, err, product, req.Operation)
	}
	return page, err
}

func (a *azureAdapter) credential() (*azidentity.DefaultAzureCredential, *azidentity.ClientSecretCredential, error) {
	if a.profile.Credential.Source == "env" {
		tenant := envValue(a.profile, "AZURE_TENANT_ID", "AZURE_TENANT_ID")
		clientID := envValue(a.profile, "AZURE_CLIENT_ID", "AZURE_CLIENT_ID")
		secret := envValue(a.profile, "AZURE_CLIENT_SECRET", "AZURE_CLIENT_SECRET")
		if tenant == "" || clientID == "" || secret == "" {
			return nil, nil, &provider.Error{Code: "missing_credentials", Operation: azureResourcesOperation, Message: "Azure credential environment variables are not set"}
		}
		cred, err := newAzureClientSecretCredential(tenant, clientID, secret, nil)
		return nil, cred, err
	}
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	return cred, nil, err
}

func (a *azureAdapter) graph(ctx context.Context, query string, subscriptions []string, pageToken string, limit int) (provider.Page, error) {
	token, err := a.accessToken(ctx, azureResourcesOperation)
	if err != nil {
		return provider.Page{}, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	body := map[string]any{"query": query, "options": map[string]any{"$top": limit, "resultFormat": "objectArray"}}
	if len(subscriptions) > 0 {
		body["subscriptions"] = subscriptions
	}
	if pageToken != "" {
		body["options"].(map[string]any)["$skipToken"] = pageToken
	}
	encoded, _ := json.Marshal(body)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://management.azure.com/providers/Microsoft.ResourceGraph/resources?api-version=2022-10-01", bytes.NewReader(encoded))
	if err != nil {
		return provider.Page{}, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "azure_api_error", Operation: azureResourcesOperation, Message: err.Error(), Retryable: true}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return provider.Page{}, &provider.Error{Code: "azure_api_error", Operation: azureResourcesOperation, Message: fmt.Sprintf("HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(data))), Retryable: response.StatusCode == 429 || response.StatusCode >= 500}
	}
	var result struct {
		Data         []map[string]any `json:"data"`
		SkipToken    string           `json:"$skipToken"`
		TotalRecords int              `json:"totalRecords"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(&result); err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: azureResourcesOperation, Message: err.Error()}
	}
	rows := make([]map[string]any, 0, len(result.Data))
	now := time.Now().UTC()
	for _, item := range result.Data {
		rows = append(rows, a.azureRow(item, now))
	}
	return provider.Page{Rows: rows, NextToken: result.SkipToken, Scanned: len(rows), Requests: 1}, nil
}

func (a *azureAdapter) accessToken(ctx context.Context, operation string) (string, error) {
	defaultCred, secretCred, err := a.credential()
	if err != nil {
		return "", &provider.Error{Code: "authentication_error", Operation: operation, Message: err.Error()}
	}
	if defaultCred != nil {
		access, err := defaultCred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{"https://management.azure.com/.default"}})
		if err != nil {
			return "", &provider.Error{Code: "authentication_error", Operation: operation, Message: err.Error()}
		}
		return access.Token, nil
	}
	access, err := secretCred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{"https://management.azure.com/.default"}})
	if err != nil {
		return "", &provider.Error{Code: "authentication_error", Operation: operation, Message: err.Error()}
	}
	return access.Token, nil
}

func (a *azureAdapter) azureRow(item map[string]any, observed time.Time) map[string]any {
	id := stringValue(item["id"])
	nativeType := stringValue(item["type"])
	name := stringValue(item["name"])
	location := stringValue(item["location"])
	subscription := stringValue(item["subscriptionId"])
	service := strings.Split(nativeType, "/")[0]
	row := baseRow(model.ProviderAzure, a.name, id, name, service, nativeType, location, subscription, observed)
	scope := row["scope"].(map[string]any)
	scope["subscription_id"] = subscription
	scope["resource_group_id"] = stringValue(item["resourceGroup"])
	if tags, ok := item["tags"].(map[string]any); ok {
		row["tags"] = tags
	}
	if properties, ok := item["properties"].(map[string]any); ok {
		row["state"] = firstString(properties, "provisioningState", "status", "state")
		if strings.Contains(strings.ToLower(nativeType), "roleassignments") {
			row["domain"] = "iam"
			row["kind"] = "binding"
			row["principal"] = stringValue(properties["principalId"])
			row["principal_type"] = stringValue(properties["principalType"])
			row["roles"] = []string{stringValue(properties["roleDefinitionId"])}
			row["resource_id"] = stringValue(properties["scope"])
		}
	}
	native := row["native"].(map[string]any)
	native["type"] = nativeType
	native["resource_group"] = stringValue(item["resourceGroup"])
	return row
}

func escapeKQL(value string) string {
	return strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(value)
}
func stringValue(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}
func firstString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringValue(values[key]); value != "" {
			return value
		}
	}
	return ""
}
