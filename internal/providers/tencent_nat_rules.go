package providers

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	tchttp "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/http"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

const tencentSNATOperation = "tencent.vpc.describe_snat_rules"
const tencentDNATOperation = "tencent.vpc.describe_dnat_rules"

func tencentNATRuleOperations() []provider.Operation {
	ops := []provider.Operation{}
	for _, name := range []string{tencentSNATOperation, tencentDNATOperation} {
		ops = append(ops, provider.Operation{Name: name, Provider: model.ProviderTencent, Service: "vpc", Description: "List NAT rule metadata with upstream pagination; excludes IPs and descriptions; DNAT IDs are process-local opaque references", Parameters: detailParameters("nat_gateway_id")})
	}
	return ops
}

type tencentNATCursor struct {
	Profile, Account, Region, Gateway, Operation string
	Offset                                       int
}

func decodeTencentNATCursor(token string, s tencentNATCursor) (int, error) {
	if token == "" {
		return 0, nil
	}
	var c tencentNATCursor
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || json.Unmarshal(b, &c) != nil || c.Offset <= 0 || c.Offset > 10000000 || c.Profile != s.Profile || c.Account != s.Account || c.Region != s.Region || c.Gateway != s.Gateway || c.Operation != s.Operation {
		return 0, &provider.Error{Code: "invalid_cursor", Operation: s.Operation, Message: "NAT cursor does not match query scope"}
	}
	return c.Offset, nil
}

var tencentNATRuleKey = func() []byte {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic(err)
	}
	return k
}()

type tencentNATRule struct {
	NatGatewayId, VpcId, NatGatewaySnatId, ResourceId, ResourceType, IpProtocol, PublicIpAddress, PrivateIpAddress string
	PublicPort, PrivatePort                                                                                        *int
}

func (a *tencentAdapter) readNATRules(ctx context.Context, req provider.NativeRequest) (provider.Page, error) {
	op := provider.Operation{Name: req.Operation, Parameters: detailParameters("nat_gateway_id")}
	if err := op.ValidateParams(req.Params); err != nil {
		return provider.Page{}, err
	}
	id, err := nativeIdentifier(req.Params, "nat_gateway_id")
	if err != nil {
		return provider.Page{}, deepParameterError(op.Name, err)
	}
	if id == "" {
		return provider.Page{}, &provider.Error{Code: "missing_parameter", Operation: op.Name, Message: "nat_gateway_id is required"}
	}
	account, err := singleDetailAccount(a.profile, op.Name)
	if err != nil {
		return provider.Page{}, err
	}
	region, err := exactDetailRegion(req.Region, a.profile.Regions, op.Name)
	if err != nil {
		return provider.Page{}, err
	}
	scope := tencentNATCursor{Profile: a.name, Account: account, Region: region, Gateway: id, Operation: op.Name}
	offset, err := decodeTencentNATCursor(req.PageToken, scope)
	if err != nil {
		return provider.Page{}, err
	}
	scope.Offset = offset
	limit := req.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	action := "DescribeNatGatewaySourceIpTranslationNatRules"
	params := map[string]any{"NatGatewayId": id, "Offset": offset, "Limit": limit}
	if op.Name == tencentDNATOperation {
		action = "DescribeNatGatewayDestinationIpPortTranslationNatRules"
		delete(params, "NatGatewayId")
		params["NatGatewayIds"] = []string{id}
	}
	client, err := a.tencentClient("vpc.tencentcloudapi.com", region, op.Name)
	if err != nil {
		return provider.Page{}, err
	}
	request := tchttp.NewCommonRequest("vpc", "2017-03-12", action)
	request.SetContext(ctx)
	if err := request.SetActionParameters(params); err != nil {
		return provider.Page{}, err
	}
	response := tchttp.NewCommonResponse()
	if err := client.Send(request, response); err != nil {
		return provider.Page{}, tencentSourceError(op.Name, err)
	}
	return a.tencentNATRulePage(response.GetBody(), scope, limit)
}
func (a *tencentAdapter) tencentNATRulePage(body []byte, scope tencentNATCursor, limit int) (provider.Page, error) {
	bad := func(msg string) (provider.Page, error) {
		return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: scope.Operation, Message: msg}
	}
	var payload struct{ Response map[string]json.RawMessage }
	if json.Unmarshal(body, &payload) != nil || payload.Response == nil {
		return bad("invalid NAT response")
	}
	if raw := payload.Response["Error"]; len(raw) > 0 && string(raw) != "null" {
		var e struct{ Code string }
		if json.Unmarshal(raw, &e) != nil || e.Code == "" {
			return bad("invalid NAT error")
		}
		return provider.Page{}, &provider.Error{Code: e.Code, Operation: scope.Operation, Message: "Tencent NAT rule request failed"}
	}
	var total *int
	if json.Unmarshal(payload.Response["TotalCount"], &total) != nil || total == nil || *total < 0 {
		return bad("missing or invalid NAT total")
	}
	list := "SourceIpTranslationNatRuleSet"
	kind := "snat_rule"
	if scope.Operation == tencentDNATOperation {
		list = "NatGatewayDestinationIpPortTranslationNatRuleSet"
		kind = "dnat_rule"
	}
	var rules []tencentNATRule
	if json.Unmarshal(payload.Response[list], &rules) != nil {
		return bad("missing or invalid NAT rules")
	}
	if len(rules) > limit || scope.Offset+len(rules) > *total || (len(rules) == 0 && scope.Offset < *total) {
		return bad("inconsistent NAT pagination")
	}
	page := provider.Page{Rows: []map[string]any{}, Requests: 1, Scanned: len(rules)}
	seen := map[string]bool{}
	for _, d := range rules {
		if d.NatGatewayId != scope.Gateway {
			return bad("NAT rule is outside requested gateway")
		}
		id := d.NatGatewaySnatId
		if scope.Operation == tencentDNATOperation {
			if d.PublicIpAddress == "" || d.PrivateIpAddress == "" || d.IpProtocol == "" || d.PublicPort == nil || d.PrivatePort == nil || *d.PublicPort < 1 || *d.PublicPort > 65535 || *d.PrivatePort < 1 || *d.PrivatePort > 65535 {
				return bad("incomplete DNAT identity")
			}
			b, _ := json.Marshal([]any{scope.Account, scope.Region, scope.Gateway, d.IpProtocol, d.PublicIpAddress, *d.PublicPort, d.PrivateIpAddress, *d.PrivatePort})
			mac := hmac.New(sha256.New, tencentNATRuleKey)
			mac.Write(b)
			id = "dnat-ref-" + hex.EncodeToString(mac.Sum(nil))
		}
		if id == "" || seen[id] {
			return bad("missing or duplicate NAT rule ID")
		}
		seen[id] = true
		row := newDeepDetailRow(a.Provider(), a.name, id, "", "vpc", "QCS::VPC::NATRule", "network", kind, scope.Region, scope.Account)
		attrs := row["attributes"].(map[string]any)
		attrs["nat_gateway_id"] = scope.Gateway
		if d.VpcId != "" {
			attrs["vpc_id"] = d.VpcId
		}
		if scope.Operation == tencentSNATOperation {
			if d.ResourceId != "" {
				attrs["resource_id"] = d.ResourceId
			}
			if d.ResourceType != "" {
				attrs["resource_type"] = d.ResourceType
			}
		} else {
			attrs["protocol"] = d.IpProtocol
			attrs["public_port"] = *d.PublicPort
			attrs["private_port"] = *d.PrivatePort
		}
		page.Rows = append(page.Rows, row)
	}
	if scope.Offset+len(rules) < *total {
		scope.Offset += len(rules)
		b, _ := json.Marshal(scope)
		page.NextToken = base64.RawURLEncoding.EncodeToString(b)
	}
	return page, nil
}
