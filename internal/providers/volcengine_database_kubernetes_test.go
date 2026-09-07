package providers

import (
	"testing"

	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

func TestVolcengineDatabaseAndKubernetesInventoryKinds(t *testing.T) {
	for _, tc := range []struct{ nativeType, domain, kind string }{
		{"Volcengine::RDSMySQL::Instance", "database", "database"},
		{"Volcengine::Redis::Instance", "database", "cache"},
		{"Volcengine::VKE::Cluster", "kubernetes", "cluster"},
		{"Volcengine::VKE::NodePool", "kubernetes", "node_pool"},
		{"Volcengine::RDSMySQL::AllowList", "other", "resource"},
		{"Volcengine::RDSMySQL::Endpoint", "other", "resource"},
		{"Volcengine::Redis::AllowList", "other", "resource"},
		{"Volcengine::VKE::Kubeconfig", "other", "resource"},
		{"Volcengine::VKE::Permission", "other", "resource"},
		{"Volcengine::VKE::Node", "other", "resource"},
		{"Volcengine::VKE::Addon", "other", "resource"},
		{"Volcengine::VKE::NodePoolAddon", "other", "resource"},
	} {
		t.Run(tc.nativeType, func(t *testing.T) {
			domain, kind := classifyVolcengine("", tc.nativeType)
			if domain != tc.domain || kind != tc.kind {
				t.Fatalf("got %s/%s, want %s/%s", domain, kind, tc.domain, tc.kind)
			}
			for _, product := range []string{"database", "kubernetes"} {
				op := "volcengine." + product + ".list_resources"
				spec, ok := nativeProductFor(model.ProviderVolcengine, op)
				if !ok {
					t.Fatal("missing product")
				}
				row := map[string]any{"domain": domain, "kind": kind, "native": map[string]any{"resource_type": tc.nativeType}}
				page, err := completeNativeProduct(provider.Page{Rows: []map[string]any{row}, NextToken: "next", Scanned: 1, Requests: 1}, nil, spec, op)
				want := 0
				if tc.domain == product {
					want = 1
				}
				if err != nil || len(page.Rows) != want || page.NextToken != "next" || page.Scanned != 1 {
					t.Fatalf("product %s: page=%+v err=%v", product, page, err)
				}
			}
		})
	}
}
