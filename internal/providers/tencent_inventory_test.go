package providers

import (
	"context"
	"errors"
	"mcpcloud/internal/config"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
	"testing"
)

func inventoryTestAdapter() *tencentAdapter {
	return &tencentAdapter{name: "test", profile: config.Profile{Regions: []string{"ap-jakarta", "ap-shanghai"}, Scopes: config.Scopes{Accounts: []string{"123"}}, Options: map[string]string{"resource_mode": "direct"}}}
}
func TestTencentInventoryProductPagination(t *testing.T) {
	a := inventoryTestAdapter()
	req := provider.QueryRequest{Source: model.SourceResources, Region: "ap-jakarta", Limit: 1}
	calls := 0
	seen := []string{}
	read := func(_ context.Context, n provider.NativeRequest) (provider.Page, error) {
		calls++
		if n.Region != req.Region || n.Limit != 1 {
			t.Fatal("scope or limit lost")
		}
		seen = append(seen, n.Operation)
		if n.Operation == tencentDirectResourceOperations[0] && n.PageToken == "" {
			return provider.Page{Rows: []map[string]any{{"id": "first"}}, NextToken: "provider-page-2", Requests: 1}, nil
		}
		if n.Operation == tencentDirectResourceOperations[0] && n.PageToken != "provider-page-2" {
			t.Fatal("nested cursor lost")
		}
		return provider.Page{Rows: []map[string]any{}, Requests: 1}, nil
	}
	for i := 0; i < 20; i++ {
		before := calls
		p, err := a.queryDirectResourcesWithReader(context.Background(), req, read)
		if err != nil {
			t.Fatal(err)
		}
		if calls-before != 1 || p.Requests != 1 {
			t.Fatal("request budget not preserved")
		}
		req.PageToken = p.NextToken
		if req.PageToken == "" {
			break
		}
	}
	if calls != 9 || seen[0] != seen[1] || seen[len(seen)-1] != tencentCOSListOperation {
		t.Fatalf("products not completed: %v", seen)
	}
}
func TestTencentInventoryScopesAndFailure(t *testing.T) {
	a := inventoryTestAdapter()
	req := provider.QueryRequest{Source: model.SourceResources, Region: "ap-jakarta", Limit: 1}
	read := func(context.Context, provider.NativeRequest) (provider.Page, error) {
		return provider.Page{Requests: 1}, nil
	}
	first, err := a.queryDirectResourcesWithReader(context.Background(), req, read)
	if err != nil || first.NextToken == "" {
		t.Fatal("empty product must advance")
	}
	req.PageToken = first.NextToken
	failure := &provider.Error{Code: "AccessDenied", Operation: tencentDirectResourceOperations[1], Message: "denied"}
	p, err := a.queryDirectResourcesWithReader(context.Background(), req, func(context.Context, provider.NativeRequest) (provider.Page, error) { return provider.Page{}, failure })
	if !errors.Is(err, failure) || len(p.Rows) != 0 {
		t.Fatal("failure swallowed")
	}
	deniedReader := func(context.Context, provider.NativeRequest) (provider.Page, error) {
		t.Fatal("invalid scope reached provider")
		return provider.Page{}, nil
	}
	changed := req
	changed.Region = "ap-shanghai"
	if _, err := a.queryDirectResourcesWithReader(context.Background(), changed, deniedReader); err == nil {
		t.Fatal("region cursor mismatch accepted")
	}
	changed = req
	changed.Accounts = []string{"456"}
	if _, err := a.queryDirectResourcesWithReader(context.Background(), changed, deniedReader); err == nil {
		t.Fatal("foreign account accepted")
	}
	changed = req
	changed.PageToken = "bad"
	if _, err := a.queryDirectResourcesWithReader(context.Background(), changed, deniedReader); err == nil {
		t.Fatal("invalid cursor accepted")
	}
	a.profile.Options["resource_view_id"] = "view"
	if _, err := a.queryDirectResourcesWithReader(context.Background(), req, deniedReader); err == nil {
		t.Fatal("view bypass accepted")
	}
}
func TestTencentInventoryCapabilities(t *testing.T) {
	a := inventoryTestAdapter()
	c := a.Capabilities()[0]
	if c.Operations[0] != tencentDirectResourcesOperation || len(c.Operations) != 9 {
		t.Fatal("direct capabilities missing")
	}
	delete(a.profile.Options, "resource_mode")
	if a.Capabilities()[0].Operations[0] != tencentResourcesOperation {
		t.Fatal("default changed")
	}
}
