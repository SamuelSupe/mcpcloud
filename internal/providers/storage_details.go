package providers

import (
	"context"
	"fmt"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func init() {
	deepDetailCatalog[model.ProviderAzure] = append(deepDetailCatalog[model.ProviderAzure], deepDetailSpec{operation: "azure.storage.blob_containers.get", service: "storage", domain: "storage", kind: "container", description: "Read Blob container management metadata without keys or object bodies", parameters: detailParameters("subscription_id", "resource_group", "account", "container"), required: []string{"subscription_id", "resource_group", "account", "container"}})
	deepDetailCatalog[model.ProviderGCP] = append(deepDetailCatalog[model.ProviderGCP], deepDetailSpec{operation: "gcp.storage.buckets.get", service: "storage", domain: "storage", kind: "bucket", description: "Read bucket metadata after verifying project ownership", parameters: detailParameters("project_id", "bucket"), required: []string{"project_id", "bucket"}})
}

func storageMetadata(detail map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range strings.Fields("id name location projectNumber storageClass timeCreated updated publicAccess leaseStatus leaseState hasImmutabilityPolicy hasLegalHold iamConfiguration uniformBucketLevelAccess enabled publicAccessPrevention versioning retentionPolicy retentionPeriod effectiveTime isLocked properties") {
		if v, ok := detail[key]; ok {
			switch x := v.(type) {
			case map[string]any:
				out[key] = storageMetadata(x)
			case string, bool, float64:
				out[key] = x
			}
		}
	}
	return out
}

func storageRow(cloud model.Provider, profile, scope, region, id, name, kind string, detail map[string]any) provider.Page {
	row := baseRow(cloud, profile, id, name, "storage", kind, region, scope, time.Now())
	row["domain"] = "storage"
	row["kind"] = kind
	row["attributes"].(map[string]any)["storage_metadata"] = storageMetadata(detail)
	return oneDeepDetailPage(row)
}

func (a *azureAdapter) readStorageDetail(ctx context.Context, req provider.NativeRequest, spec deepDetailSpec) (provider.Page, error) {
	if err := validateDeepDetailRequest(spec, req); err != nil {
		return provider.Page{}, err
	}
	sub, err := nativeIdentifier(req.Params, "subscription_id")
	if err != nil {
		return provider.Page{}, err
	}
	sub, err = exactDetailScope(a.profile.Scopes.Subscriptions, sub, spec.operation, "subscription_id")
	if err != nil {
		return provider.Page{}, err
	}
	values := map[string]string{}
	for _, k := range []string{"resource_group", "account", "container"} {
		v, e := nativeIdentifier(req.Params, k)
		if e != nil {
			return provider.Page{}, e
		}
		values[k] = url.PathEscape(v)
	}
	token, err := a.accessToken(ctx, spec.operation)
	if err != nil {
		return provider.Page{}, err
	}
	endpoint := "https://management.azure.com/subscriptions/" + url.PathEscape(sub) + "/resourceGroups/" + values["resource_group"] + "/providers/Microsoft.Storage/storageAccounts/" + values["account"] + "/blobServices/default/containers/" + values["container"] + "?api-version=2023-05-01"
	detail := map[string]any{}
	if err = azureDetailGET(ctx, endpoint, token, spec.operation, &detail); err != nil {
		return provider.Page{}, err
	}
	if stringValue(detail["id"]) == "" {
		return provider.Page{}, deepNotFound(spec.operation, spec.kind)
	}
	return storageRow(a.Provider(), a.name, sub, req.Region, stringValue(detail["id"]), stringValue(detail["name"]), "container", detail), nil
}

func (a *gcpAdapter) readStorageDetail(ctx context.Context, req provider.NativeRequest, spec deepDetailSpec) (provider.Page, error) {
	if err := validateDeepDetailRequest(spec, req); err != nil {
		return provider.Page{}, err
	}
	project, err := nativeIdentifier(req.Params, "project_id")
	if err != nil {
		return provider.Page{}, err
	}
	project, err = exactDetailScope(a.profile.Scopes.Projects, project, spec.operation, "project_id")
	if err != nil {
		return provider.Page{}, err
	}
	bucket, err := nativeIdentifier(req.Params, "bucket")
	if err != nil {
		return provider.Page{}, err
	}
	client, err := a.httpClient(ctx)
	if err != nil {
		return provider.Page{}, err
	}
	projectDetail := map[string]any{}
	if err = gcpJSONRequest(ctx, client, http.MethodGet, "https://cloudresourcemanager.googleapis.com/v1/projects/"+url.PathEscape(project), nil, spec.operation, &projectDetail); err != nil {
		return provider.Page{}, err
	}
	detail := map[string]any{}
	if err = gcpJSONRequest(ctx, client, http.MethodGet, "https://storage.googleapis.com/storage/v1/b/"+url.PathEscape(bucket), nil, spec.operation, &detail); err != nil {
		return provider.Page{}, err
	}
	if fmt.Sprint(detail["projectNumber"]) != fmt.Sprint(projectDetail["projectNumber"]) || projectDetail["projectNumber"] == nil {
		return provider.Page{}, &provider.Error{Code: "invalid_scope", Operation: spec.operation, Message: "bucket does not belong to configured project"}
	}
	page := storageRow(a.Provider(), a.name, project, strings.ToLower(stringValue(detail["location"])), "//storage.googleapis.com/"+bucket, bucket, "bucket", detail)
	page.Requests = 2
	return page, nil
}
