package providers

import (
	"context"
	"errors"
	tencent "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	cos "github.com/tencentyun/cos-go-sdk-v5"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const tencentCOSOperation = "tencent.cos.describe_bucket_config"

var tencentCOSBucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}-[0-9]{5,20}$`)
var tencentCOSRegionPattern = regexp.MustCompile(`^[a-z]{2}-[a-z0-9]+(?:-[a-z0-9]+)?$`)

func tencentCOSConfigOperation() provider.Operation {
	return provider.Operation{Name: tencentCOSOperation, Provider: model.ProviderTencent, Service: "cos", Description: "Read bucket owner, location, ACL public-group summary, versioning and lifecycle counts; no object reads or policy analysis", Parameters: detailParameters("bucket")}
}
func newTencentCOSClient(bucket, region string, credential tencent.CredentialIface) *cos.Client {
	u := &url.URL{Scheme: "https", Host: bucket + ".cos." + region + ".myqcloud.com"}
	c := cos.NewClient(&cos.BaseURL{BucketURL: u}, &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &cos.AuthorizationTransport{SecretID: credential.GetSecretId(), SecretKey: credential.GetSecretKey(), SessionToken: credential.GetToken(), Transport: http.DefaultTransport}})
	c.Conf.RetryOpt = cos.RetryOptions{Count: 1}
	return c
}
func tencentCOSError(err error) error {
	var e *cos.ErrorResponse
	code := "provider_error"
	if errors.As(err, &e) && e.Code != "" {
		code = e.Code
	}
	return &provider.Error{Code: code, Operation: tencentCOSOperation, Message: "COS bucket configuration request failed"}
}
func (a *tencentAdapter) readCOSConfig(ctx context.Context, req provider.NativeRequest) (provider.Page, error) {
	op := tencentCOSConfigOperation()
	if err := op.ValidateParams(req.Params); err != nil {
		return provider.Page{}, err
	}
	if req.PageToken != "" {
		return provider.Page{}, &provider.Error{Code: "cursor_not_supported", Operation: op.Name, Message: "bucket configuration does not accept a cursor"}
	}
	bucket, err := nativeIdentifier(req.Params, "bucket")
	if err != nil {
		return provider.Page{}, deepParameterError(op.Name, err)
	}
	if !tencentCOSBucketPattern.MatchString(bucket) || len(bucket) > 63 {
		return provider.Page{}, &provider.Error{Code: "invalid_parameter", Operation: op.Name, Message: "bucket must be a COS bucket name with APPID suffix"}
	}
	account, err := singleDetailAccount(a.profile, op.Name)
	if err != nil {
		return provider.Page{}, err
	}
	region, err := exactDetailRegion(req.Region, a.profile.Regions, op.Name)
	if err != nil {
		return provider.Page{}, err
	}
	if !tencentCOSRegionPattern.MatchString(region) {
		return provider.Page{}, &provider.Error{Code: "invalid_parameter", Operation: op.Name, Message: "invalid COS region"}
	}
	credential, err := a.credential()
	if err != nil {
		return provider.Page{}, err
	}
	client := newTencentCOSClient(bucket, region, credential)
	acl, _, err := client.Bucket.GetACL(ctx)
	if err != nil {
		return provider.Page{}, tencentCOSError(err)
	}
	owner := "qcs::cam::uin/" + account + ":uin/" + account
	if acl == nil || acl.Owner == nil || acl.Owner.ID != owner {
		return provider.Page{}, &provider.Error{Code: "scope_not_allowed", Operation: op.Name, Message: "bucket owner is outside the profile account"}
	}
	location, _, err := client.Bucket.GetLocation(ctx)
	if err != nil {
		return provider.Page{}, tencentCOSError(err)
	}
	if location == nil || strings.TrimSpace(location.Location) != region {
		return provider.Page{}, &provider.Error{Code: "scope_not_allowed", Operation: op.Name, Message: "bucket location does not match requested region"}
	}
	version, _, err := client.Bucket.GetVersioning(ctx)
	if err != nil {
		return provider.Page{}, tencentCOSError(err)
	}
	lifecycle, response, err := client.Bucket.GetLifecycle(ctx)
	absent := false
	if err != nil {
		var e *cos.ErrorResponse
		if errors.As(err, &e) && e.Code == "NoSuchLifecycleConfiguration" && response != nil && response.StatusCode == 404 {
			absent = true
		} else {
			return provider.Page{}, tencentCOSError(err)
		}
	}
	return a.tencentCOSConfigPage(bucket, region, account, acl, version, lifecycle, absent)
}
func (a *tencentAdapter) tencentCOSConfigPage(bucket, region, account string, acl *cos.BucketGetACLResult, version *cos.BucketGetVersionResult, lifecycle *cos.BucketGetLifecycleResult, absent bool) (provider.Page, error) {
	bad := func() (provider.Page, error) {
		return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: tencentCOSOperation, Message: "invalid COS configuration response"}
	}
	if acl == nil || version == nil || (!absent && lifecycle == nil) {
		return bad()
	}
	row := newDeepDetailRow(a.Provider(), a.name, bucket, bucket, "cos", "QCS::COS::Bucket", "storage", "bucket", region, account)
	attrs := row["attributes"].(map[string]any)
	status := version.Status
	switch status {
	case "":
		status = "not_enabled"
	case "Enabled", "Suspended":
	default:
		return bad()
	}
	attrs["versioning_status"] = status
	public, authenticated := false, false
	for _, grant := range acl.AccessControlList {
		if grant.Grantee == nil {
			return bad()
		}
		for _, value := range []string{grant.Grantee.URI, grant.Grantee.ID} {
			if strings.HasSuffix(value, "/AllUsers") {
				public = true
			}
			if strings.HasSuffix(value, "/AuthenticatedUsers") {
				authenticated = true
			}
		}
	}
	attrs["acl_all_users_grant"] = public
	attrs["acl_authenticated_users_grant"] = authenticated
	attrs["lifecycle_configured"] = !absent
	count, enabled, disabled := 0, 0, 0
	if !absent {
		count = len(lifecycle.Rules)
		for _, r := range lifecycle.Rules {
			switch r.Status {
			case "Enabled":
				enabled++
			case "Disabled":
				disabled++
			default:
				return bad()
			}
		}
	}
	attrs["lifecycle_rule_count"] = count
	attrs["lifecycle_enabled_rule_count"] = enabled
	attrs["lifecycle_disabled_rule_count"] = disabled
	return provider.Page{Rows: []map[string]any{row}, Scanned: 1, Requests: 4}, nil
}
