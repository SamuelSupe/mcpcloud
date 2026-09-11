package providers

import (
	"encoding/base64"
	"encoding/json"
	cos "github.com/tencentyun/cos-go-sdk-v5"
	"testing"
)

func TestTencentCOSListPaginationAndScope(t *testing.T) {
	a := &tencentAdapter{name: "test"}
	scope := tencentCOSListCursor{Profile: "test", Account: "123", Region: "ap-jakarta", Operation: tencentCOSListOperation}
	owner := &cos.Owner{ID: "qcs::cam::uin/123:uin/123"}
	r := &cos.ServiceGetResult{Owner: owner, Buckets: []cos.Bucket{{Name: "bucket-a-12345", Region: "ap-jakarta", CreationDate: "2026-01-01T00:00:00Z"}}, NextMarker: "1", IsTruncated: true}
	p, err := a.tencentCOSListPage(r, scope, 1)
	if err != nil || len(p.Rows) != 1 || p.NextToken == "" {
		t.Fatalf("first: %+v %v", p, err)
	}
	next, err := decodeTencentCOSListCursor(p.NextToken, scope)
	if err != nil || next.Marker != r.NextMarker {
		t.Fatal("cursor mismatch")
	}
	last, err := a.tencentCOSListPage(&cos.ServiceGetResult{Owner: owner, Buckets: []cos.Bucket{{Name: "bucket-b-12345", Region: "ap-jakarta"}}}, next, 1)
	if err != nil || last.NextToken != "" || len(last.Rows) != 1 {
		t.Fatal("last page")
	}
	for _, change := range []string{"profile", "account", "region", "operation"} {
		c := scope
		switch change {
		case "profile":
			c.Profile = "other"
		case "account":
			c.Account = "other"
		case "region":
			c.Region = "ap-shanghai"
		case "operation":
			c.Operation = "other"
		}
		if _, err := decodeTencentCOSListCursor(p.NextToken, c); err == nil {
			t.Fatal("scope change accepted")
		}
	}
	empty, err := a.tencentCOSListPage(&cos.ServiceGetResult{Owner: owner}, scope, 1)
	if err != nil || len(empty.Rows) != 0 {
		t.Fatal("empty list")
	}
	for _, r := range []*cos.ServiceGetResult{nil, {}, {Owner: &cos.Owner{ID: "wrong"}}, {Owner: owner, IsTruncated: true}, {Owner: owner, NextMarker: "1"}, {Owner: owner, Buckets: []cos.Bucket{{Name: "bucket-a-12345", Region: "ap-shanghai"}}}, {Owner: owner, Buckets: []cos.Bucket{{Name: "bucket-a-12345", Region: "ap-jakarta"}, {Name: "bucket-a-12345", Region: "ap-jakarta"}}}, {Owner: owner, Buckets: []cos.Bucket{{Name: "bucket-a-12345", Region: "ap-jakarta"}}, NextMarker: "\n"}} {
		if _, err := a.tencentCOSListPage(r, scope, 100); err == nil {
			t.Fatal("invalid response accepted")
		}
	}
	scope.Marker = "\n"
	raw, _ := json.Marshal(scope)
	if _, err := decodeTencentCOSListCursor(base64.RawURLEncoding.EncodeToString(raw), scope); err == nil {
		t.Fatal("invalid marker accepted")
	}
}
