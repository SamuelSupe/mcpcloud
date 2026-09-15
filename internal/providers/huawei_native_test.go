package providers

import (
	"errors"
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
