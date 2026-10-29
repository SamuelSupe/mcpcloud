package providers

import (
	"context"
	"errors"
	"fmt"
	ces "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/ces/v1"
	cesmodel "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/ces/v1/model"
	"testing"

	"mcpcloud/internal/provider"
)

func TestHuaweiDCSOffset(t *testing.T) {
	offset, err := huaweiDCSOffset(huaweiDCSListOperation, "42")
	if err != nil || offset != 42 {
		t.Fatalf("huaweiDCSOffset() = (%d, %v), want (42, nil)", offset, err)
	}
	for _, token := range []string{"-1", "bad"} {
		_, err := huaweiDCSOffset(huaweiDCSListOperation, token)
		var providerErr *provider.Error
		if !errors.As(err, &providerErr) || providerErr.Code != "invalid_page_token" {
			t.Fatalf("huaweiDCSOffset(%q) = %#v, want invalid_page_token", token, err)
		}
	}
}

func TestHuaweiMarkerNext(t *testing.T) {
	rows := []map[string]any{{"id": "first"}, {"id": "second"}}
	if got := huaweiMarkerNext(rows, 2); got != "second" {
		t.Fatalf("huaweiMarkerNext() = %q, want second", got)
	}
	if got := huaweiMarkerNext(rows, 3); got != "" {
		t.Fatalf("huaweiMarkerNext(short page) = %q, want empty", got)
	}
}

func TestNativeHuaweiListLimit(t *testing.T) {
	for _, test := range []struct {
		requested int
		maximum   int
		want      int32
	}{
		{requested: 0, maximum: 200, want: 200},
		{requested: 10, maximum: 200, want: 10},
		{requested: 201, maximum: 200, want: 200},
	} {
		if got := nativeHuaweiListLimit(test.requested, test.maximum); got != test.want {
			t.Fatalf("nativeHuaweiListLimit(%d, %d) = %d, want %d", test.requested, test.maximum, got, test.want)
		}
	}
}

func TestHuaweiCESPaginationUsesMarkerAtFinalPage(t *testing.T) {
	t.Setenv("HUAWEICLOUD_SDK_AK", "test")
	t.Setenv("HUAWEICLOUD_SDK_SK", "test")
	t.Setenv("HUAWEICLOUD_SDK_SECURITY_TOKEN", "")
	old := queryHuaweiCESMetrics
	t.Cleanup(func() { queryHuaweiCESMetrics = old })
	a := &huaweiAdapter{name: "ces", profile: envCredentialProfile(nil)}
	a.profile.Scopes.Projects = []string{"project"}
	a.profile.Regions = []string{"cn-north-4"}
	for _, lastCount := range []int{0, 1, 2} {
		t.Run(fmt.Sprintf("last_count_%d", lastCount), func(t *testing.T) {
			calls := 0
			queryHuaweiCESMetrics = func(_ *ces.CesClient, req *cesmodel.ListMetricsRequest) (*cesmodel.ListMetricsResponse, error) {
				count, marker := 2, "next"
				if calls > 0 {
					if req.Start == nil || *req.Start != "next" {
						t.Fatal("lost CES continuation")
					}
					count, marker = lastCount, ""
				}
				calls++
				metrics := make([]cesmodel.MetricInfoList, count)
				for i := range metrics {
					metrics[i].Namespace = "SYS.ECS"
					metrics[i].MetricName = fmt.Sprintf("metric-%d-%d", calls, i)
				}
				return &cesmodel.ListMetricsResponse{Metrics: &metrics, MetaData: &cesmodel.MetricListMetaDataResp{Count: int32(count), Total: int32(2 + lastCount), Marker: marker}}, nil
			}
			req := provider.NativeRequest{Operation: huaweiCESListMetricsOperation, Limit: 2, Params: map[string]any{"namespace": "SYS.ECS"}}
			first, err := a.NativeRead(context.Background(), req)
			if err != nil || len(first.Rows) != 2 || first.NextToken != "next" {
				t.Fatalf("first page: %+v %v", first, err)
			}
			req.PageToken = first.NextToken
			last, err := a.NativeRead(context.Background(), req)
			if err != nil || len(last.Rows) != lastCount || last.NextToken != "" || calls != 2 {
				t.Fatalf("last page: %+v %v calls=%d", last, err, calls)
			}
		})
	}
}
