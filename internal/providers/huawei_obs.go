package providers

import (
	"bytes"
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
	"strconv"
	"strings"
	"time"
)

const huaweiOBSEndpoint = "https://obs.cn-south-1.myhuaweicloud.com/"

func huaweiOBSOperations() []provider.Operation {
	return []provider.Operation{
		operation(huaweiOBSListOperation, model.ProviderHuawei, "obs", "List account-level standard OBS buckets", map[string]any{}),
		operation(huaweiOBSPFSListOperation, model.ProviderHuawei, "obs", "List account-level OBS POSIX parallel file systems", map[string]any{}),
		operation(huaweiOBSStorageInfoOperation, model.ProviderHuawei, "obs", "Get summarized OBS bucket/PFS storage statistics without listing objects", map[string]any{"bucket_name": map[string]any{"type": "string", "required": true}}),
	}
}

type huaweiOBSBucket struct {
	Name         string
	CreationDate string
	Location     string
	BucketType   string
}

// parseHuaweiOBSBuckets deliberately decodes Bucket elements rather than
// assuming one particular ListAllMyBuckets XML root layout. OBS responses have
// differed between standard-bucket and POSIX listings over time.
func parseHuaweiOBSBuckets(body []byte) ([]huaweiOBSBucket, error) {
	decoder := xml.NewDecoder(bytes.NewReader(body))
	buckets := []huaweiOBSBucket{}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return buckets, nil
		}
		if err != nil {
			return nil, err
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "Bucket" {
			continue
		}
		var bucket huaweiOBSBucket
		for {
			inner, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			switch value := inner.(type) {
			case xml.EndElement:
				if value.Name.Local == "Bucket" {
					buckets = append(buckets, bucket)
					goto nextToken
				}
			case xml.StartElement:
				var text string
				if err := decoder.DecodeElement(&text, &value); err != nil {
					return nil, err
				}
				switch value.Name.Local {
				case "Name":
					bucket.Name = text
				case "CreationDate":
					bucket.CreationDate = text
				case "Location":
					bucket.Location = text
				case "BucketType":
					bucket.BucketType = text
				}
			}
		}
	nextToken:
	}
}

func (a *huaweiAdapter) listHuaweiOBSBuckets(ctx context.Context, r provider.NativeRequest) (provider.Page, error) {
	if err := validateHuaweiOBSRequest(r); err != nil {
		return provider.Page{}, err
	}
	kind := "OBJECT"
	if r.Operation == huaweiOBSPFSListOperation {
		kind = "POSIX"
	}
	buckets, err := a.fetchHuaweiOBSBuckets(ctx, r.Operation, kind)
	if err != nil {
		return provider.Page{}, err
	}
	rows := a.huaweiOBSBucketRows(buckets, kind)
	return provider.Page{Rows: rows, Scanned: len(rows), Requests: 1}, nil
}

func (a *huaweiAdapter) fetchHuaweiOBSBuckets(ctx context.Context, operation, kind string) ([]huaweiOBSBucket, error) {
	ak := envValue(a.profile, "HUAWEICLOUD_SDK_AK", "HUAWEICLOUD_SDK_AK")
	sk := envValue(a.profile, "HUAWEICLOUD_SDK_SK", "HUAWEICLOUD_SDK_SK")
	if ak == "" || sk == "" {
		return nil, &provider.Error{Code: "missing_credentials", Operation: operation, Message: "Huawei Cloud credential environment variables are not set"}
	}
	date := time.Now().UTC().Format(http.TimeFormat)
	mac := hmac.New(sha1.New, []byte(sk))
	_, _ = mac.Write([]byte(fmt.Sprintf("GET\n\n\n%s\nx-obs-bucket-type:%s\n/", date, kind)))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, huaweiOBSEndpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Date", date)
	req.Header.Set("Authorization", "OBS "+ak+":"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	req.Header.Set("x-obs-bucket-type", kind)
	resp, err := huaweiOBSHTTPClient(ctx).Do(req)
	if err != nil {
		return nil, huaweiDeepError(operation, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, &provider.Error{Code: "huawei_api_error", Operation: operation, Message: fmt.Sprintf("OBS list returned HTTP %d", resp.StatusCode)}
	}
	buckets, err := parseHuaweiOBSBuckets(body)
	if err != nil {
		return nil, &provider.Error{Code: "invalid_provider_response", Operation: operation, Message: "OBS list XML could not be parsed"}
	}
	return buckets, nil
}

func (a *huaweiAdapter) huaweiOBSBucketRows(buckets []huaweiOBSBucket, kind string) []map[string]any {
	rows := []map[string]any{}
	// OBS bucket/PFS enumeration is account-level. Associate it with the
	// configured project solely for profile authorization; account_scoped marks
	// that this is not a claim that the bucket belongs to that project.
	scopeID := ""
	if len(a.profile.Scopes.Projects) > 0 {
		scopeID = a.profile.Scopes.Projects[0]
	} else if len(a.profile.Scopes.Accounts) > 0 {
		scopeID = a.profile.Scopes.Accounts[0]
	}
	for _, b := range buckets {
		bt := strings.ToUpper(b.BucketType)
		if bt == "" {
			bt = kind
		}
		// The request header is the authoritative type filter. Older OBS XML
		// responses omit BucketType, and some responses use a non-enum label.
		if b.Name == "" {
			continue
		}
		k := "bucket"
		if kind == "POSIX" {
			k = "parallel_file_system"
		}
		row := newDeepDetailRow(a.Provider(), a.name, b.Name, b.Name, "obs", "OBS::Bucket", "storage", k, b.Location, scopeID)
		row["account_scoped"] = true
		at := row["attributes"].(map[string]any)
		at["bucket_type"] = bt
		at["creation_date"] = b.CreationDate
		rows = append(rows, row)
	}
	return rows
}

func validateHuaweiOBSRequest(r provider.NativeRequest) error {
	if len(r.Params) > 0 {
		return &provider.Error{Code: "invalid_parameter", Operation: r.Operation, Message: "OBS account list accepts no parameters"}
	}
	if r.PageToken != "" {
		return &provider.Error{Code: "cursor_not_supported", Operation: r.Operation, Message: "OBS account list is not paginated"}
	}
	return nil
}

func (a *huaweiAdapter) getHuaweiOBSStorageInfo(ctx context.Context, r provider.NativeRequest) (provider.Page, error) {
	if r.PageToken != "" {
		return provider.Page{}, &provider.Error{Code: "cursor_not_supported", Operation: r.Operation, Message: "OBS bucket storage statistics are not paginated"}
	}
	bucketName, err := nativeString(r.Params, "bucket_name")
	if err != nil || bucketName == "" || len(r.Params) != 1 {
		return provider.Page{}, &provider.Error{Code: "invalid_parameter", Operation: r.Operation, Message: "OBS storage statistics require only bucket_name"}
	}
	if strings.ContainsAny(bucketName, "/?\\") {
		return provider.Page{}, &provider.Error{Code: "invalid_parameter", Operation: r.Operation, Message: "bucket_name is invalid"}
	}
	var bucket huaweiOBSBucket
	found := false
	for _, kind := range []string{"OBJECT", "POSIX"} {
		items, err := a.fetchHuaweiOBSBuckets(ctx, r.Operation, kind)
		if err != nil {
			return provider.Page{}, err
		}
		for _, item := range items {
			if item.Name == bucketName {
				bucket, found = item, true
				break
			}
		}
		if found {
			break
		}
	}
	if !found || bucket.Location == "" {
		return provider.Page{}, &provider.Error{Code: "not_found", Operation: r.Operation, Message: "OBS bucket was not found in the authorized account inventory"}
	}
	storage, err := a.fetchHuaweiOBSStorageInfo(ctx, r.Operation, bucket.Name, bucket.Location)
	if err != nil {
		return provider.Page{}, err
	}
	scopeID := ""
	if len(a.profile.Scopes.Projects) > 0 {
		scopeID = a.profile.Scopes.Projects[0]
	} else if len(a.profile.Scopes.Accounts) > 0 {
		scopeID = a.profile.Scopes.Accounts[0]
	}
	kind := "bucket_storage"
	if strings.EqualFold(bucket.BucketType, "POSIX") {
		kind = "parallel_file_system_storage"
	}
	row := newDeepDetailRow(a.Provider(), a.name, bucket.Name, bucket.Name, "obs", "OBS::BucketStorageInfo", "storage", kind, bucket.Location, scopeID)
	row["account_scoped"] = true
	attrs := row["attributes"].(map[string]any)
	attrs["bucket_type"] = strings.ToUpper(bucket.BucketType)
	attrs["size_bytes"] = storage.Size
	attrs["object_count"] = storage.ObjectNumber
	attrs["statistics_source"] = "GetBucketStorageInfo"
	return provider.Page{Rows: []map[string]any{row}, Scanned: 1, Requests: 3}, nil
}

type huaweiOBSStorageInfo struct {
	Size         int64
	ObjectNumber int64
}

func (a *huaweiAdapter) fetchHuaweiOBSStorageInfo(ctx context.Context, operation, bucketName, region string) (huaweiOBSStorageInfo, error) {
	ak := envValue(a.profile, "HUAWEICLOUD_SDK_AK", "HUAWEICLOUD_SDK_AK")
	sk := envValue(a.profile, "HUAWEICLOUD_SDK_SK", "HUAWEICLOUD_SDK_SK")
	if ak == "" || sk == "" {
		return huaweiOBSStorageInfo{}, &provider.Error{Code: "missing_credentials", Operation: operation, Message: "Huawei Cloud credential environment variables are not set"}
	}
	date := time.Now().UTC().Format(http.TimeFormat)
	canonicalResource := "/" + bucketName + "/?storageinfo"
	mac := hmac.New(sha1.New, []byte(sk))
	_, _ = mac.Write([]byte("GET\n\n\n" + date + "\n" + canonicalResource))
	endpoint := "https://" + bucketName + ".obs." + region + ".myhuaweicloud.com/?storageinfo"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return huaweiOBSStorageInfo{}, err
	}
	req.Header.Set("Date", date)
	req.Header.Set("Authorization", "OBS "+ak+":"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	response, err := huaweiOBSHTTPClient(ctx).Do(req)
	if err != nil {
		return huaweiOBSStorageInfo{}, huaweiDeepError(operation, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return huaweiOBSStorageInfo{}, err
	}
	if response.StatusCode/100 != 2 {
		return huaweiOBSStorageInfo{}, &provider.Error{Code: "huawei_api_error", Operation: operation, Message: fmt.Sprintf("OBS storage statistics returned HTTP %d", response.StatusCode)}
	}
	return parseHuaweiOBSStorageInfo(body)
}

func parseHuaweiOBSStorageInfo(body []byte) (huaweiOBSStorageInfo, error) {
	decoder := xml.NewDecoder(bytes.NewReader(body))
	info := huaweiOBSStorageInfo{}
	seenSize, seenCount := false, false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return huaweiOBSStorageInfo{}, err
		}
		start, ok := token.(xml.StartElement)
		if !ok || (start.Name.Local != "Size" && start.Name.Local != "ObjectNumber") {
			continue
		}
		var text string
		if err := decoder.DecodeElement(&text, &start); err != nil {
			return huaweiOBSStorageInfo{}, err
		}
		value, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
		if err != nil || value < 0 {
			return huaweiOBSStorageInfo{}, fmt.Errorf("invalid OBS storage statistic")
		}
		if start.Name.Local == "Size" {
			info.Size, seenSize = value, true
		} else {
			info.ObjectNumber, seenCount = value, true
		}
	}
	if !seenSize || !seenCount {
		return huaweiOBSStorageInfo{}, fmt.Errorf("OBS storage statistics response is incomplete")
	}
	return info, nil
}

func huaweiOBSHTTPClient(ctx context.Context) *http.Client {
	timeout := 120 * time.Second
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) > 0 {
		timeout = time.Until(deadline)
	}
	return &http.Client{Timeout: timeout}
}
