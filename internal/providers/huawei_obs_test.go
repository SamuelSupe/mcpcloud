package providers

import (
	"context"
	"errors"
	"testing"
	"time"

	"mcpcloud/internal/provider"
)

func TestParseHuaweiOBSBuckets(t *testing.T) {
	body := []byte(`<?xml version="1.0"?><ListAllMyBucketsResult xmlns="http://obs.myhwclouds.com/doc/2015-06-30/"><Buckets><Bucket><Name>unit-bucket</Name><CreationDate>2026-09-14T00:00:00.000Z</CreationDate><Location>cn-south-1</Location><BucketType>POSIX</BucketType></Bucket></Buckets></ListAllMyBucketsResult>`)
	buckets, err := parseHuaweiOBSBuckets(body)
	if err != nil {
		t.Fatalf("parseHuaweiOBSBuckets: %v", err)
	}
	if len(buckets) != 1 {
		t.Fatalf("bucket count = %d, want 1", len(buckets))
	}
	if buckets[0].Name != "unit-bucket" || buckets[0].Location != "cn-south-1" || buckets[0].BucketType != "POSIX" {
		t.Fatalf("unexpected parsed bucket: %#v", buckets[0])
	}
}

func TestHuaweiOBSHTTPClientBoundsRequests(t *testing.T) {
	if got := huaweiOBSHTTPClient(context.Background()).Timeout; got != 120*time.Second {
		t.Fatalf("background timeout = %s, want 120s", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if got := huaweiOBSHTTPClient(ctx).Timeout; got <= 0 || got > time.Second {
		t.Fatalf("deadline timeout = %s, want a positive value up to 1s", got)
	}
}

func TestParseHuaweiOBSBucketsRejectsMalformedXML(t *testing.T) {
	if _, err := parseHuaweiOBSBuckets([]byte("<Buckets><Bucket>")); err == nil {
		t.Fatal("expected malformed XML error")
	}
}

func TestParseHuaweiOBSStorageInfo(t *testing.T) {
	body := []byte(`<GetBucketStorageInfoResult xmlns="http://obs.cn-south-1.myhuaweicloud.com/doc/2015-06-30/"><Size>244123456789</Size><ObjectNumber>50396455</ObjectNumber><StandardSize>1</StandardSize></GetBucketStorageInfoResult>`)
	info, err := parseHuaweiOBSStorageInfo(body)
	if err != nil {
		t.Fatalf("parseHuaweiOBSStorageInfo: %v", err)
	}
	if info.Size != 244123456789 || info.ObjectNumber != 50396455 {
		t.Fatalf("unexpected storage info: %#v", info)
	}
}

func TestParseHuaweiOBSStorageInfoRejectsIncompleteOrInvalidValues(t *testing.T) {
	for _, body := range [][]byte{
		[]byte(`<GetBucketStorageInfoResult><Size>1</Size></GetBucketStorageInfoResult>`),
		[]byte(`<GetBucketStorageInfoResult><Size>-1</Size><ObjectNumber>2</ObjectNumber></GetBucketStorageInfoResult>`),
	} {
		if _, err := parseHuaweiOBSStorageInfo(body); err == nil {
			t.Fatal("expected invalid storage info to fail")
		}
	}
}

func TestValidateHuaweiOBSRequestRejectsParametersAndCursor(t *testing.T) {
	tests := []struct {
		name    string
		request provider.NativeRequest
		want    string
	}{
		{name: "parameter", request: provider.NativeRequest{Operation: huaweiOBSListOperation, Params: map[string]any{"unexpected": "x"}}, want: "invalid_parameter"},
		{name: "cursor", request: provider.NativeRequest{Operation: huaweiOBSPFSListOperation, PageToken: "not-a-real-cursor"}, want: "cursor_not_supported"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateHuaweiOBSRequest(test.request)
			var providerErr *provider.Error
			if !errors.As(err, &providerErr) || providerErr.Code != test.want {
				t.Fatalf("validateHuaweiOBSRequest() = %#v, want code %q", err, test.want)
			}
		})
	}
}
