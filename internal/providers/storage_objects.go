package providers

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

func init() {
	deepDetailCatalog[model.ProviderAzure] = append(deepDetailCatalog[model.ProviderAzure], deepDetailSpec{operation: "azure.storage.blobs.list", service: "storage", domain: "objects", kind: "object", description: "List current Blob identities and size metadata; never read bodies or arbitrary metadata", parameters: detailParameters("subscription_id", "resource_group", "account", "container"), required: []string{"subscription_id", "resource_group", "account", "container"}})
	deepDetailCatalog[model.ProviderGCP] = append(deepDetailCatalog[model.ProviderGCP], deepDetailSpec{operation: "gcp.storage.objects.list", service: "storage", domain: "objects", kind: "object", description: "List current object identities and size metadata after verifying bucket project; no bodies or arbitrary metadata", parameters: detailParameters("project_id", "bucket"), required: []string{"project_id", "bucket"}})
}

type blobList struct {
	Blobs []struct {
		Name       string `xml:"Name"`
		Properties struct {
			Size     int64  `xml:"Content-Length"`
			Type     string `xml:"BlobType"`
			Modified string `xml:"Last-Modified"`
			ETag     string `xml:"Etag"`
			Tier     string `xml:"AccessTier"`
		} `xml:"Properties"`
	} `xml:"Blobs>Blob"`
	Next string `xml:"NextMarker"`
}

func objectRow(cloud model.Provider, profile, scope, region, bucket, name string, attrs map[string]any) map[string]any {
	row := baseRow(cloud, profile, bucket+"/objects/"+url.PathEscape(name), name, "storage", "object", region, scope, time.Now())
	row["domain"] = "storage"
	row["kind"] = "object"
	attrs["bucket"] = bucket
	attrs["data_source"] = "storage_data_plane_metadata"
	row["attributes"] = attrs
	return row
}

func validateObjectList(req provider.NativeRequest, spec deepDetailSpec) error {
	copy := req
	copy.PageToken = ""
	if err := validateDeepDetailRequest(spec, copy); err != nil {
		return err
	}
	if len(req.PageToken) > 8192 || strings.ContainsAny(req.PageToken, "\r\n\x00") {
		return fmt.Errorf("invalid object continuation")
	}
	return nil
}

func objectPageSize(limit int) int {
	if limit < 1 {
		return 100
	}
	return min(limit, 100)
}

var storageAccountPattern = regexp.MustCompile(`^[a-z0-9]{3,24}$`)
var storageContainerPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)

func (a *azureAdapter) readObjects(ctx context.Context, req provider.NativeRequest, spec deepDetailSpec) (provider.Page, error) {
	if err := validateObjectList(req, spec); err != nil {
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
	rg, err := nativeIdentifier(req.Params, "resource_group")
	if err != nil {
		return provider.Page{}, err
	}
	account, err := nativeIdentifier(req.Params, "account")
	if err != nil {
		return provider.Page{}, err
	}
	container, err := nativeIdentifier(req.Params, "container")
	if err != nil {
		return provider.Page{}, err
	}
	if !storageAccountPattern.MatchString(account) || !storageContainerPattern.MatchString(container) || strings.Contains(container, "--") {
		return provider.Page{}, fmt.Errorf("invalid storage account or container")
	}
	armToken, err := a.accessToken(ctx, spec.operation)
	if err != nil {
		return provider.Page{}, err
	}
	accountID := "/subscriptions/" + url.PathEscape(sub) + "/resourceGroups/" + url.PathEscape(rg) + "/providers/Microsoft.Storage/storageAccounts/" + account
	detail := map[string]any{}
	if err = azureDetailGET(ctx, "https://management.azure.com"+accountID+"?api-version=2023-05-01", armToken, spec.operation, &detail); err != nil {
		return provider.Page{}, err
	}
	if !strings.EqualFold(stringValue(detail["id"]), accountID) {
		return provider.Page{}, fmt.Errorf("storage account scope mismatch")
	}
	def, secret, err := a.credential()
	if err != nil {
		return provider.Page{}, fmt.Errorf("storage credential unavailable")
	}
	opts := policy.TokenRequestOptions{Scopes: []string{"https://storage.azure.com/.default"}}
	token := ""
	if def != nil {
		t, e := def.GetToken(ctx, opts)
		err = e
		token = t.Token
	} else {
		t, e := secret.GetToken(ctx, opts)
		err = e
		token = t.Token
	}
	if err != nil {
		return provider.Page{}, fmt.Errorf("storage token request failed")
	}
	query := url.Values{"restype": {"container"}, "comp": {"list"}, "maxresults": {strconv.Itoa(objectPageSize(req.Limit))}}
	if req.PageToken != "" {
		query.Set("marker", req.PageToken)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+account+".blob.core.windows.net/"+container+"?"+query.Encode(), nil)
	if err != nil {
		return provider.Page{}, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("x-ms-version", "2023-11-03")
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return provider.Page{}, fmt.Errorf("Blob metadata request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return provider.Page{}, &provider.Error{Code: "storage_api_error", Operation: spec.operation, Message: fmt.Sprintf("Blob list HTTP %d", response.StatusCode)}
	}
	var data blobList
	if err = xml.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&data); err != nil {
		return provider.Page{}, fmt.Errorf("invalid Blob list response")
	}
	if len(data.Blobs) > objectPageSize(req.Limit) || (data.Next != "" && data.Next == req.PageToken) {
		return provider.Page{}, fmt.Errorf("invalid Blob list pagination")
	}
	page := provider.Page{Requests: 2, Scanned: len(data.Blobs), NextToken: data.Next}
	for _, b := range data.Blobs {
		page.Rows = append(page.Rows, objectRow(a.Provider(), a.name, sub, stringValue(detail["location"]), accountID+"/blobServices/default/containers/"+container, b.Name, map[string]any{"size_bytes": b.Properties.Size, "blob_type": b.Properties.Type, "last_modified": b.Properties.Modified, "etag": b.Properties.ETag, "access_tier": b.Properties.Tier}))
	}
	return page, nil
}

func (a *gcpAdapter) readObjects(ctx context.Context, req provider.NativeRequest, spec deepDetailSpec) (provider.Page, error) {
	if err := validateObjectList(req, spec); err != nil {
		return provider.Page{}, err
	}
	// Bucket metadata verifies the configured project's ownership before data-plane access.
	bucketSpec, _ := deepDetailFor(a.Provider(), "gcp.storage.buckets.get")
	copy := req
	copy.PageToken = ""
	bucketPage, err := a.readStorageDetail(ctx, copy, bucketSpec)
	if err != nil {
		return provider.Page{}, err
	}
	bucket, _ := nativeIdentifier(req.Params, "bucket")
	project, _ := nativeIdentifier(req.Params, "project_id")
	client, err := a.httpClient(ctx)
	if err != nil {
		return provider.Page{}, fmt.Errorf("storage credential unavailable")
	}
	clientCopy := *client
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	query := url.Values{"projection": {"noAcl"}, "maxResults": {strconv.Itoa(objectPageSize(req.Limit))}, "fields": {"nextPageToken,items(name,bucket,size,generation,metageneration,storageClass,timeCreated,updated,etag)"}}
	if req.PageToken != "" {
		query.Set("pageToken", req.PageToken)
	}
	var data struct {
		Items []struct {
			Name           string `json:"name"`
			Bucket         string `json:"bucket"`
			Size           string `json:"size"`
			Generation     string `json:"generation"`
			Metageneration string `json:"metageneration"`
			StorageClass   string `json:"storageClass"`
			Created        string `json:"timeCreated"`
			Updated        string `json:"updated"`
			ETag           string `json:"etag"`
		} `json:"items"`
		Next string `json:"nextPageToken"`
	}
	if err = gcpJSONRequest(ctx, &clientCopy, http.MethodGet, "https://storage.googleapis.com/storage/v1/b/"+url.PathEscape(bucket)+"/o?"+query.Encode(), nil, spec.operation, &data); err != nil {
		return provider.Page{}, &provider.Error{Code: "storage_api_error", Operation: spec.operation, Message: "GCS object metadata request failed"}
	}
	if len(data.Items) > objectPageSize(req.Limit) || (data.Next != "" && data.Next == req.PageToken) {
		return provider.Page{}, fmt.Errorf("invalid object pagination")
	}
	page := provider.Page{Requests: 3, Scanned: len(data.Items), NextToken: data.Next}
	region, _ := bucketPage.Rows[0]["region"].(string)
	for _, o := range data.Items {
		size, e := strconv.ParseUint(o.Size, 10, 64)
		if e != nil || o.Bucket != bucket {
			return provider.Page{}, fmt.Errorf("invalid object size or bucket")
		}
		page.Rows = append(page.Rows, objectRow(a.Provider(), a.name, project, region, "//storage.googleapis.com/"+bucket, o.Name, map[string]any{"size_bytes": size, "generation": o.Generation, "metageneration": o.Metageneration, "storage_class": o.StorageClass, "created_at": o.Created, "updated_at": o.Updated, "etag": o.ETag}))
	}
	return page, nil
}
