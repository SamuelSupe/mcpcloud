package providers

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var ossBucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
var alibabaOSSResources = map[string]string{
	"alibaba.oss.get_bucket_acl":        "acl",
	"alibaba.oss.get_bucket_versioning": "versioning",
	"alibaba.oss.get_bucket_lifecycle":  "lifecycle",
	"alibaba.oss.get_bucket_stat":       "stat",
}

func init() {
	for _, name := range []string{"alibaba.oss.get_bucket_acl", "alibaba.oss.get_bucket_versioning", "alibaba.oss.get_bucket_lifecycle", "alibaba.oss.get_bucket_stat"} {
		deepDetailCatalog[model.ProviderAlibaba] = append(deepDetailCatalog[model.ProviderAlibaba], deepDetailSpec{operation: name, service: "oss", domain: "oss_depth", kind: "bucket", description: "Read fixed OSS bucket configuration or aggregate statistics; no object listing, content, policy body or lifecycle prefix", parameters: detailParameters("bucket_name"), required: []string{"bucket_name"}})
	}
}

func (a *alibabaAdapter) readOSSDepth(ctx context.Context, req provider.NativeRequest, spec deepDetailSpec, account, region string) (provider.Page, error) {
	bucket, err := nativeIdentifier(req.Params, "bucket_name")
	if err != nil || !ossBucketPattern.MatchString(bucket) {
		return provider.Page{}, deepParameterError(spec.operation, fmt.Errorf("invalid bucket_name"))
	}
	if req.PageToken != "" {
		return provider.Page{}, &provider.Error{Code: "cursor_not_supported", Operation: spec.operation, Message: "OSS bucket configuration is not paginated"}
	}
	// Resolve ownership and region from scoped inventory before constructing any host.
	page, err := a.search(ctx, "", map[string][]string{"ResourceType": {"ACS::OSS::Bucket"}, "ResourceId": {bucket}, "RegionId": {region}}, "", 100)
	if err != nil {
		return provider.Page{}, err
	}
	found := false
	for _, r := range page.Rows {
		scope, _ := r["scope"].(map[string]any)
		if r["name"] == bucket && r["region"] == region && scope["account_id"] == account {
			found = true
		}
	}
	if !found {
		return provider.Page{}, &provider.Error{Code: "not_found", Operation: spec.operation, Message: "bucket is not present in the configured account and region inventory"}
	}
	access := envValue(a.profile, "ALIBABA_CLOUD_ACCESS_KEY_ID", "ALIBABA_CLOUD_ACCESS_KEY_ID")
	secret := envValue(a.profile, "ALIBABA_CLOUD_ACCESS_KEY_SECRET", "ALIBABA_CLOUD_ACCESS_KEY_SECRET")
	token := envValue(a.profile, "ALIBABA_CLOUD_SECURITY_TOKEN", "ALIBABA_CLOUD_SECURITY_TOKEN")
	if access == "" || secret == "" {
		return provider.Page{}, &provider.Error{Code: "missing_credentials", Operation: spec.operation, Message: "OSS requires the configured access credentials"}
	}
	resource := alibabaOSSResources[spec.operation]
	date := time.Now().UTC().Format(http.TimeFormat)
	headers := ""
	if token != "" {
		headers = "x-oss-security-token:" + token + "\n"
	}
	canonical := "GET\n\n\n" + date + "\n" + headers + "/" + bucket + "/?" + resource
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(canonical))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+bucket+".oss-"+region+".aliyuncs.com/?"+resource, nil)
	if err != nil {
		return provider.Page{}, fmt.Errorf("invalid OSS request")
	}
	request.Header.Set("Date", date)
	request.Header.Set("Authorization", "OSS "+access+":"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	if token != "" {
		request.Header.Set("x-oss-security-token", token)
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "oss_connection_error", Operation: spec.operation, Message: "OSS connection failed"}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return provider.Page{}, fmt.Errorf("invalid OSS response size")
	}
	attrs, err := decodeOSSDepth(resource, response.StatusCode, body)
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "oss_api_error", Operation: spec.operation, Message: err.Error()}
	}
	row := newDeepDetailRow(a.Provider(), a.name, bucket, bucket, "oss", "ACS::OSS::Bucket", "storage", "bucket", region, account)
	for key, value := range attrs {
		row["attributes"].(map[string]any)[key] = value
	}
	return provider.Page{Rows: []map[string]any{row}, Scanned: 1, Requests: page.Requests + 1}, nil
}

func decodeOSSDepth(resource string, status int, body []byte) (map[string]any, error) {
	attrs := map[string]any{"data_source": "oss_api"}
	if status != 200 {
		var failure struct {
			Code string `xml:"Code"`
		}
		_ = xml.Unmarshal(body, &failure)
		if resource == "lifecycle" && status == 404 && failure.Code == "NoSuchLifecycle" {
			attrs["configured"] = false
			attrs["rule_count"] = 0
			attrs["enabled_rule_count"] = 0
			return attrs, nil
		}
		return nil, fmt.Errorf("OSS bucket metadata HTTP %d", status)
	}
	switch resource {
	case "acl":
		var config struct {
			XMLName xml.Name `xml:"AccessControlPolicy"`
			Grant   string   `xml:"AccessControlList>Grant"`
		}
		if xml.Unmarshal(body, &config) != nil || !containsString([]string{"private", "public-read", "public-read-write"}, config.Grant) {
			return nil, fmt.Errorf("invalid OSS ACL response")
		}
		attrs["acl"] = config.Grant
	case "versioning":
		var config struct {
			XMLName xml.Name `xml:"VersioningConfiguration"`
			Status  string   `xml:"Status"`
		}
		if xml.Unmarshal(body, &config) != nil || !containsString([]string{"", "Enabled", "Suspended"}, config.Status) {
			return nil, fmt.Errorf("invalid OSS versioning response")
		}
		attrs["versioning_status"] = config.Status
		attrs["configured"] = config.Status != ""
	case "lifecycle":
		var config struct {
			XMLName xml.Name `xml:"LifecycleConfiguration"`
			Rules   []struct {
				Status string `xml:"Status"`
			} `xml:"Rule"`
		}
		if xml.Unmarshal(body, &config) != nil {
			return nil, fmt.Errorf("invalid OSS lifecycle response")
		}
		enabled := 0
		for _, rule := range config.Rules {
			if rule.Status == "Enabled" {
				enabled++
			}
		}
		attrs["configured"] = true
		attrs["rule_count"] = len(config.Rules)
		attrs["enabled_rule_count"] = enabled
	case "stat":
		var stats struct {
			XMLName  xml.Name `xml:"BucketStat"`
			Storage  *int64   `xml:"Storage"`
			Objects  *int64   `xml:"ObjectCount"`
			Modified *int64   `xml:"LastModifiedTime"`
		}
		if xml.Unmarshal(body, &stats) != nil || stats.Storage == nil || stats.Objects == nil || *stats.Storage < 0 || *stats.Objects < 0 {
			return nil, fmt.Errorf("invalid OSS statistics response")
		}
		attrs["storage_bytes"] = *stats.Storage
		attrs["object_count"] = *stats.Objects
		if stats.Modified != nil {
			attrs["statistics_modified_at"] = time.Unix(*stats.Modified, 0).UTC().Format(time.RFC3339)
		}
	default:
		return nil, fmt.Errorf("unsupported OSS metadata operation")
	}
	return attrs, nil
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if strings.EqualFold(candidate, value) {
			return true
		}
	}
	return false
}
