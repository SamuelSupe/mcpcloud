package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	cos "github.com/tencentyun/cos-go-sdk-v5"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
	"net/url"
	"strings"
	"time"
	"unicode"
)

const tencentCOSListOperation = "tencent.cos.list_buckets"

func tencentCOSListOp() provider.Operation {
	return provider.Operation{Name: tencentCOSListOperation, Provider: model.ProviderTencent, Service: "cos", Description: "List bucket names, region and creation time directly via COS GetService; no objects or bucket configuration; marker pagination is not a snapshot", Parameters: map[string]any{}}
}

type tencentCOSListCursor struct{ Profile, Account, Region, Operation, Marker string }

func decodeTencentCOSListCursor(token string, scope tencentCOSListCursor) (tencentCOSListCursor, error) {
	if token == "" {
		return scope, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	var c tencentCOSListCursor
	if err != nil || json.Unmarshal(raw, &c) != nil || c.Profile != scope.Profile || c.Account != scope.Account || c.Region != scope.Region || c.Operation != scope.Operation || !validTencentCOSMarker(c.Marker) {
		return scope, &provider.Error{Code: "invalid_cursor", Operation: tencentCOSListOperation, Message: "COS list cursor does not match scope or has invalid marker"}
	}
	return c, nil
}
func (a *tencentAdapter) readCOSList(ctx context.Context, req provider.NativeRequest) (provider.Page, error) {
	if err := tencentCOSListOp().ValidateParams(req.Params); err != nil {
		return provider.Page{}, err
	}
	account, err := singleDetailAccount(a.profile, req.Operation)
	if err != nil {
		return provider.Page{}, err
	}
	region, err := exactDetailRegion(req.Region, a.profile.Regions, req.Operation)
	if err != nil {
		return provider.Page{}, err
	}
	scope, err := decodeTencentCOSListCursor(req.PageToken, tencentCOSListCursor{Profile: a.name, Account: account, Region: region, Operation: req.Operation})
	if err != nil {
		return provider.Page{}, err
	}
	credential, err := a.credential()
	if err != nil {
		return provider.Page{}, err
	}
	client := newTencentCOSClient("unused", region, credential)
	client.BaseURL.ServiceURL = &url.URL{Scheme: "https", Host: "service.cos.myqcloud.com"}
	limit := req.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	result, _, err := client.Service.Get(ctx, &cos.ServiceGetOptions{Region: region, Marker: scope.Marker, MaxKeys: int64(limit)})
	if err != nil {
		e := tencentCOSError(err).(*provider.Error)
		e.Operation = req.Operation
		e.Message = "COS bucket list request failed"
		return provider.Page{}, e
	}
	return a.tencentCOSListPage(result, scope, limit)
}
func (a *tencentAdapter) tencentCOSListPage(result *cos.ServiceGetResult, scope tencentCOSListCursor, limit int) (provider.Page, error) {
	bad := func(message string) (provider.Page, error) {
		return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: tencentCOSListOperation, Message: message}
	}
	if result == nil || result.Owner == nil {
		return bad("missing COS list or owner")
	}
	owner := "qcs::cam::uin/" + scope.Account + ":uin/" + scope.Account
	if result.Owner.ID != owner {
		return provider.Page{}, &provider.Error{Code: "scope_not_allowed", Operation: tencentCOSListOperation, Message: "COS owner does not match configured account"}
	}
	if len(result.Buckets) > limit {
		return bad("COS list exceeds requested limit")
	}
	page := provider.Page{Rows: []map[string]any{}, Scanned: len(result.Buckets), Requests: 1}
	seen := map[string]bool{}
	for _, b := range result.Buckets {
		if !tencentCOSBucketPattern.MatchString(b.Name) || len(b.Name) > 63 || seen[b.Name] {
			return bad("invalid, duplicate or unordered COS bucket")
		}
		if b.Region != scope.Region {
			return provider.Page{}, &provider.Error{Code: "scope_not_allowed", Operation: tencentCOSListOperation, Message: "COS bucket region does not match requested region"}
		}
		seen[b.Name] = true
		row := newDeepDetailRow(a.Provider(), a.name, b.Name, b.Name, "cos", "QCS::COS::Bucket", "storage", "bucket", scope.Region, scope.Account)
		setDetailTime(row, "created_at", b.CreationDate, time.RFC3339, time.RFC3339Nano)
		page.Rows = append(page.Rows, row)
	}
	if result.NextMarker != "" {
		if len(result.Buckets) == 0 || !validTencentCOSMarker(result.NextMarker) || result.NextMarker == scope.Marker {
			return bad("COS pagination marker is invalid or repeated")
		}
		scope.Marker = result.NextMarker
		raw, _ := json.Marshal(scope)
		page.NextToken = base64.RawURLEncoding.EncodeToString(raw)
	} else if result.IsTruncated {
		return bad("COS truncated list is missing next marker")
	}
	return page, nil
}

func validTencentCOSMarker(marker string) bool {
	return marker != "" && len(marker) <= 4096 && strings.IndexFunc(marker, unicode.IsControl) < 0
}
