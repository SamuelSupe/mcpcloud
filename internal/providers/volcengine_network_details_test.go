package providers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/volcengine/volcengine-go-sdk/service/natgateway"
	volc "github.com/volcengine/volcengine-go-sdk/volcengine"

	"mcpcloud/internal/provider"
)

func TestVolcengineNetworkDetailSchemasAndPagination(t *testing.T) {
	ops := volcengineNetworkDetailOperations()
	if len(ops) != 10 {
		t.Fatalf("network detail operation count = %d, want 10", len(ops))
	}
	for _, op := range ops {
		key := "nat_gateway_id"
		switch op.Name {
		case volcengineCLBDetailOperation, volcengineCLBListenersOperation:
			key = "load_balancer_id"
		case volcengineCLBHealthOperation:
			key = "listener_id"
		case volcengineCLBServerGroupOperation:
			key = "server_group_id"
		case volcengineEIPDetailOperation:
			key = "allocation_id"
		case volcengineTOSConfigurationOperation:
			key = "bucket_name"
		case volcengineNATListOperation:
			key = "project_name"
		}
		if err := op.ValidateParams(map[string]any{key: "resource-a"}); err != nil {
			t.Fatalf("%s valid schema: %v", op.Name, err)
		}
		if err := op.ValidateParams(map[string]any{key: "resource-a", "action": "Delete"}); err == nil {
			t.Fatalf("%s accepted undeclared parameter", op.Name)
		}
	}
	if _, _, _, _, _, err := validateVolcengineNetworkRequest(provider.NativeRequest{Operation: volcengineNATListOperation, Params: map[string]any{"project_name": "default", "network_type": "private"}}); !hasProviderErrorCode(err, "invalid_parameter") {
		t.Fatalf("invalid NAT network type error = %#v", err)
	}
	request := provider.NativeRequest{Operation: volcengineNATListOperation, Region: "cn-north-1", Params: map[string]any{"project_name": "default"}, Limit: 1}
	providerRows := []map[string]any{{"id": "one"}, {"id": "two"}}
	page, err := volcengineNetworkPage(providerRows, 1, 100, 0, 1, 101, request)
	if err != nil || len(page.Rows) != 1 || page.Rows[0]["id"] != "one" || page.Scanned != 1 || page.Requests != 1 {
		t.Fatalf("first page = %#v", page)
	}
	pageNumber, offset, err := decodeVolcenginePageCursor(page.NextToken, request.Operation, "default", request.Region)
	if err != nil || pageNumber != 1 || offset != 1 {
		t.Fatalf("first page cursor = %d/%d, err=%v", pageNumber, offset, err)
	}
	request.PageToken = page.NextToken
	page, err = volcengineNetworkPage(providerRows, pageNumber, 100, offset, 1, 101, request)
	if err != nil || len(page.Rows) != 1 || page.Rows[0]["id"] != "two" {
		t.Fatalf("second local slice = %#v, err=%v", page, err)
	}
	pageNumber, offset, err = decodeVolcenginePageCursor(page.NextToken, request.Operation, "default", request.Region)
	if err != nil || pageNumber != 2 || offset != 0 {
		t.Fatalf("next provider page cursor = %d/%d, err=%v", pageNumber, offset, err)
	}
	request.PageToken = page.NextToken
	page, err = volcengineNetworkPage([]map[string]any{{"id": "three"}}, pageNumber, 100, offset, 1, 101, request)
	if err != nil || page.NextToken != "" || len(page.Rows) != 1 || page.Rows[0]["id"] != "three" {
		t.Fatalf("last page = %#v, err=%v", page, err)
	}
	if _, _, _, _, _, err := validateVolcengineNetworkRequest(provider.NativeRequest{Operation: volcengineCLBListenersOperation, Params: map[string]any{"load_balancer_id": "clb-a"}, PageToken: "not-a-page"}); !hasProviderErrorCode(err, "invalid_cursor") {
		t.Fatalf("invalid cursor error = %#v", err)
	}
}

func TestVolcengineNATRowsExcludeAddressAndPortSecrets(t *testing.T) {
	a := &volcengineAdapter{name: "volcengine-prod"}
	snat := a.volcengineSNATRow(&natgateway.SnatEntryForDescribeSnatEntriesOutput{
		SnatEntryId: volc.String("snat-a"), NatGatewayId: volc.String("nat-a"),
		EipId: volc.String("eip-a"), EipAddress: volc.String("203.0.113.10"),
		SourceCidr: volc.String("10.0.0.0/8"), Status: volc.String("Available"),
	}, "cn-shanghai", "account-a")
	dnat := a.volcengineDNATRow(&natgateway.DnatEntryForDescribeDnatEntriesOutput{
		DnatEntryId: volc.String("dnat-a"), NatGatewayId: volc.String("nat-a"),
		ExternalIp: volc.String("203.0.113.10"), ExternalPort: volc.String("443"),
		InternalIp: volc.String("10.0.0.10"), InternalPort: volc.String("8443"),
		Protocol: volc.String("tcp"), Status: volc.String("Available"),
	}, "cn-shanghai", "account-a")
	raw, err := json.Marshal([]map[string]any{snat, dnat})
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, forbidden := range []string{"203.0.113.10", "10.0.0.0/8", "10.0.0.10", "8443", "source_cidr", "external_ip", "internal_ip"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("normalized NAT rows leaked %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, "snat-a") || !strings.Contains(text, "dnat-a") || !strings.Contains(text, "eip-a") {
		t.Fatalf("normalized NAT rows lost identifiers: %s", text)
	}
}
