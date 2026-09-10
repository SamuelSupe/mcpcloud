package providers

import (
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/requests"
	"testing"
)

func TestAlibabaBillUsesResourceOwnerFilter(t *testing.T) {
	for _, accounts := range [][]string{nil, {"123456789012"}, {"123", "456"}} {
		r, e := newAlibabaQueryBillRequest(accounts)
		if e != nil {
			t.Fatal(e)
		}
		if e = requests.InitParams(r); e != nil {
			t.Fatal(e)
		}
		q := r.GetQueryParams()
		if q["OwnerId"] != "" {
			t.Fatal("must not override caller OwnerId")
		}
		want := ""
		if len(accounts) == 1 {
			want = accounts[0]
		}
		if q["BillOwnerId"] != want {
			t.Errorf("BillOwnerId=%q, want %q", q["BillOwnerId"], want)
		}
	}
	if _, e := newAlibabaQueryBillRequest([]string{"invalid"}); !hasProviderErrorCode(e, "invalid_scope") {
		t.Fatalf("invalid account error=%v", e)
	}
}
