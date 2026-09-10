package providers

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net"
	"sort"

	tchttp "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/http"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

const tencentTargetsOperationName = "tencent.clb.describe_targets"
const tencentTargetHealthOperationName = "tencent.clb.describe_target_health"

func tencentTargetOperations() []provider.Operation {
	ops := []provider.Operation{}
	for _, name := range []string{tencentTargetsOperationName, tencentTargetHealthOperationName} {
		ops = append(ops, provider.Operation{Name: name, Provider: model.ProviderTencent, Service: "clb", Description: "List CLB target bindings or health; locally paginated; address-only targets use process-local opaque references; excludes addresses, domains and URLs; rejects unsupported function and Polaris targets", Parameters: detailParameters("load_balancer_id")})
	}
	return ops
}

// Address references remain stable within this server process, but change on restart.
// A random HMAC key prevents exposing raw addresses or plain address hashes.
var tencentTargetAddressKey = func() []byte {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	return key
}()

func tencentTargetIdentity(target tencentCLBTarget, health bool, scope tencentTargetCursor) (id, ref string) {
	id = target.InstanceId
	if health {
		id = target.TargetId
	}
	if id != "" && net.ParseIP(id) == nil {
		return id, id
	}
	addresses := []string{}
	if net.ParseIP(id) != nil {
		addresses = append(addresses, net.ParseIP(id).String())
	} else if health && net.ParseIP(target.IP) != nil {
		addresses = append(addresses, net.ParseIP(target.IP).String())
	} else {
		for _, ip := range target.PrivateIpAddresses {
			if parsed := net.ParseIP(ip); parsed != nil {
				addresses = append(addresses, parsed.String())
			}
		}
	}
	if len(addresses) == 0 {
		return "", ""
	}
	sort.Strings(addresses)
	data, _ := json.Marshal([]any{scope.Account, scope.Region, scope.LoadBalancer, addresses})
	mac := hmac.New(sha256.New, tencentTargetAddressKey)
	mac.Write(data)
	return "", "address-ref-" + hex.EncodeToString(mac.Sum(nil))
}

type tencentTargetCursor struct{ Profile, Account, Region, LoadBalancer, Operation, LastID string }

func decodeTencentTargetCursor(token string, scope tencentTargetCursor) (string, error) {
	if token == "" {
		return "", nil
	}
	data, err := base64.RawURLEncoding.DecodeString(token)
	var c tencentTargetCursor
	if err != nil || json.Unmarshal(data, &c) != nil || c.LastID == "" || c.Profile != scope.Profile || c.Account != scope.Account || c.Region != scope.Region || c.LoadBalancer != scope.LoadBalancer || c.Operation != scope.Operation {
		return "", &provider.Error{Code: "invalid_cursor", Operation: scope.Operation, Message: "target cursor does not match query scope"}
	}
	return c.LastID, nil
}

type tencentCLBTarget struct {
	IP                                        string
	PrivateIpAddresses                        []string
	InstanceId, TargetId, Type, TargetGroupId string
	Port, Weight                              *int
	HealthStatus                              *bool
	HealthStatusDetail, HealthStatusDetial    string
}
type tencentCLBTargetRule struct {
	LocationId                      string
	Targets                         []tencentCLBTarget
	FunctionTargets, PolarisTargets []json.RawMessage
}
type tencentCLBTargetListener struct {
	ListenerId, Protocol            string
	Port                            *int
	Targets                         []tencentCLBTarget
	Rules                           []tencentCLBTargetRule
	FunctionTargets, PolarisTargets []json.RawMessage
}

func (a *tencentAdapter) readCLBTargets(ctx context.Context, req provider.NativeRequest) (provider.Page, error) {
	op := provider.Operation{Name: req.Operation, Parameters: detailParameters("load_balancer_id")}
	if err := op.ValidateParams(req.Params); err != nil {
		return provider.Page{}, err
	}
	id, err := nativeIdentifier(req.Params, "load_balancer_id")
	if err != nil {
		return provider.Page{}, deepParameterError(op.Name, err)
	}
	if id == "" {
		return provider.Page{}, &provider.Error{Code: "missing_parameter", Operation: op.Name, Message: "load_balancer_id is required"}
	}
	account, err := singleDetailAccount(a.profile, op.Name)
	if err != nil {
		return provider.Page{}, err
	}
	region, err := exactDetailRegion(req.Region, a.profile.Regions, op.Name)
	if err != nil {
		return provider.Page{}, err
	}
	scope := tencentTargetCursor{Profile: a.name, Account: account, Region: region, LoadBalancer: id, Operation: op.Name}
	if _, err := decodeTencentTargetCursor(req.PageToken, scope); err != nil {
		return provider.Page{}, err
	}
	action := "DescribeTargets"
	params := map[string]any{"LoadBalancerId": id}
	if op.Name == tencentTargetHealthOperationName {
		action = "DescribeTargetHealth"
		params = map[string]any{"LoadBalancerIds": []string{id}}
	}
	client, err := a.tencentClient("clb.tencentcloudapi.com", region, op.Name)
	if err != nil {
		return provider.Page{}, err
	}
	request := tchttp.NewCommonRequest("clb", "2018-03-17", action)
	request.SetContext(ctx)
	if err := request.SetActionParameters(params); err != nil {
		return provider.Page{}, err
	}
	response := tchttp.NewCommonResponse()
	if err := client.Send(request, response); err != nil {
		return provider.Page{}, tencentSourceError(op.Name, err)
	}
	listeners, err := parseTencentCLBTargets(response.GetBody(), id, op.Name)
	if err != nil {
		return provider.Page{}, err
	}
	return a.tencentTargetPage(listeners, req, scope)
}

func parseTencentCLBTargets(body []byte, id, operation string) ([]tencentCLBTargetListener, error) {
	bad := func(message string) ([]tencentCLBTargetListener, error) {
		return nil, &provider.Error{Code: "invalid_provider_response", Operation: operation, Message: message}
	}
	var envelope struct{ Response map[string]json.RawMessage }
	if json.Unmarshal(body, &envelope) != nil || envelope.Response == nil {
		return bad("invalid CLB target response")
	}
	if raw := envelope.Response["Error"]; len(raw) > 0 && string(raw) != "null" {
		var e struct{ Code string }
		if json.Unmarshal(raw, &e) != nil || e.Code == "" {
			return bad("invalid CLB target error")
		}
		return nil, &provider.Error{Code: e.Code, Operation: operation, Message: "Tencent CLB target request failed"}
	}
	if operation == tencentTargetHealthOperationName {
		var lbs []struct {
			LoadBalancerId string
			Listeners      []tencentCLBTargetListener
		}
		raw := envelope.Response["LoadBalancers"]
		if len(raw) == 0 || json.Unmarshal(raw, &lbs) != nil {
			return bad("missing or invalid CLB health list")
		}
		if len(lbs) == 0 {
			return nil, deepNotFound(operation, "load_balancer")
		}
		if len(lbs) != 1 || lbs[0].LoadBalancerId != id {
			return bad("CLB health response does not match requested instance")
		}
		return lbs[0].Listeners, nil
	}
	var listeners []tencentCLBTargetListener
	if json.Unmarshal(envelope.Response["Listeners"], &listeners) != nil {
		return bad("missing or invalid CLB target list")
	}
	return listeners, nil
}

func (a *tencentAdapter) tencentTargetPage(listeners []tencentCLBTargetListener, req provider.NativeRequest, scope tencentTargetCursor) (provider.Page, error) {
	last, err := decodeTencentTargetCursor(req.PageToken, scope)
	if err != nil {
		return provider.Page{}, err
	}
	rows := []map[string]any{}
	seen := map[string]bool{}
	bad := func(code, message string) error {
		return &provider.Error{Code: code, Operation: req.Operation, Message: message}
	}
	add := func(l tencentCLBTargetListener, location string, targets []tencentCLBTarget) error {
		for _, target := range targets {
			id, ref := tencentTargetIdentity(target, req.Operation == tencentTargetHealthOperationName, scope)
			if ref == "" {
				return bad("invalid_provider_response", "target identity missing")
			}
			if target.Port == nil || *target.Port < 1 || *target.Port > 65535 {
				return bad("invalid_provider_response", "target port missing or invalid")
			}
			keyData, _ := json.Marshal([]any{scope.LoadBalancer, l.ListenerId, location, ref, *target.Port, target.TargetGroupId})
			digest := sha256.Sum256(keyData)
			key := "clb-binding-" + hex.EncodeToString(digest[:])
			if seen[key] {
				return bad("invalid_provider_response", "duplicate target binding")
			}
			seen[key] = true
			row := newDeepDetailRow(a.Provider(), a.name, key, "", "clb", "QCS::CLB::TargetBinding", "network", "target_binding", scope.Region, scope.Account)
			attrs := row["attributes"].(map[string]any)
			attrs["load_balancer_id"] = scope.LoadBalancer
			attrs["listener_id"] = l.ListenerId
			if id != "" {
				attrs["target_id"] = id
			} else {
				attrs["target_ref"] = ref
			}
			attrs["target_port"] = *target.Port
			if location != "" {
				attrs["location_id"] = location
			}
			if l.Protocol != "" {
				attrs["protocol"] = l.Protocol
			}
			if l.Port != nil {
				attrs["listener_port"] = *l.Port
			}
			if target.Weight != nil {
				attrs["weight"] = *target.Weight
			}
			if target.Type != "" {
				attrs["target_type"] = target.Type
			}
			if target.TargetGroupId != "" {
				attrs["target_group_id"] = target.TargetGroupId
			}
			if req.Operation == tencentTargetHealthOperationName {
				row["state"] = "unknown"
				if target.HealthStatus != nil {
					attrs["healthy"] = *target.HealthStatus
					if *target.HealthStatus {
						row["state"] = "healthy"
					} else {
						row["state"] = "unhealthy"
					}
				}
				detail := target.HealthStatusDetail
				if detail == "" {
					detail = target.HealthStatusDetial
				}
				switch detail {
				case "Alive", "Dead", "Unknown", "Close":
					attrs["health_status_detail"] = detail
				}
			}
			rows = append(rows, row)
		}
		return nil
	}
	for _, l := range listeners {
		if l.ListenerId == "" {
			return provider.Page{}, bad("invalid_provider_response", "listener ID missing")
		}
		if len(l.FunctionTargets)+len(l.PolarisTargets) > 0 {
			return provider.Page{}, bad("unsupported_target_type", "function or Polaris targets are not supported")
		}
		if err := add(l, "", l.Targets); err != nil {
			return provider.Page{}, err
		}
		for _, rule := range l.Rules {
			if len(rule.FunctionTargets)+len(rule.PolarisTargets) > 0 {
				return provider.Page{}, bad("unsupported_target_type", "function or Polaris targets are not supported")
			}
			if err := add(l, rule.LocationId, rule.Targets); err != nil {
				return provider.Page{}, err
			}
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i]["id"].(string) < rows[j]["id"].(string) })
	limit := req.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	page := provider.Page{Rows: []map[string]any{}, Scanned: len(rows), Requests: 1}
	for _, row := range rows {
		if row["id"].(string) <= last {
			continue
		}
		if len(page.Rows) == limit {
			scope.LastID = page.Rows[len(page.Rows)-1]["id"].(string)
			data, _ := json.Marshal(scope)
			page.NextToken = base64.RawURLEncoding.EncodeToString(data)
			break
		}
		page.Rows = append(page.Rows, row)
	}
	return page, nil
}
