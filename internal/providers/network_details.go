package providers

import (
	"context"
	"fmt"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var azureNetworkTypes = map[string]string{
	"azure.network.security_groups.get":  "networkSecurityGroups",
	"azure.network.load_balancers.get":   "loadBalancers",
	"azure.network.virtual_networks.get": "virtualNetworks",
	"azure.network.nat_gateways.get":     "natGateways",
}
var gcpNetworkTypes = map[string]string{
	"gcp.compute.firewalls.get":               "global/firewalls",
	"gcp.compute.networks.get":                "global/networks",
	"gcp.compute.subnetworks.get":             "subnetworks",
	"gcp.compute.routers.get":                 "routers",
	"gcp.compute.backend_services.get":        "backendServices",
	"gcp.compute.global_backend_services.get": "global/backendServices",
	"gcp.compute.forwarding_rules.get":        "forwardingRules",
}

func init() {
	for op := range azureNetworkTypes {
		deepDetailCatalog[model.ProviderAzure] = append(deepDetailCatalog[model.ProviderAzure], deepDetailSpec{operation: op, service: "network", domain: "network", kind: "network", description: "Read fixed Azure network resource topology without secrets", parameters: detailParameters("subscription_id", "resource_group", "name"), required: []string{"subscription_id", "resource_group", "name"}})
	}
	for op, typ := range gcpNetworkTypes {
		keys := []string{"project_id", "name"}
		if !strings.HasPrefix(typ, "global/") {
			keys = append(keys, "location")
		}
		deepDetailCatalog[model.ProviderGCP] = append(deepDetailCatalog[model.ProviderGCP], deepDetailSpec{operation: op, service: "compute", domain: "network", kind: "network", description: "Read fixed GCP network resource topology without secrets", parameters: detailParameters(keys...), required: keys})
	}
}

// Explicit field allowlist; do not forward descriptions, metadata, credentials,
// arbitrary properties or object bodies from cloud responses.
var topologyFields = map[string]bool{}

func init() {
	topologyFields["networkInterfaces"] = true
	for _, key := range strings.Fields("securityRules defaultSecurityRules priority direction access sourcePortRange destinationPortRange sourceAddressPrefix destinationAddressPrefix sourcePortRanges destinationPortRanges sourceAddressPrefixes destinationAddressPrefixes sourceApplicationSecurityGroups destinationApplicationSecurityGroups allowed denied sourceTags targetTags sourceServiceAccounts targetServiceAccounts disabled") {
		topologyFields[key] = true
	}
	for _, key := range strings.Fields("id name location region selfLink kind properties provisioningState frontendIPConfigurations backendAddressPools loadBalancingRules probes inboundNatRules backendIPConfigurations loadBalancerBackendAddresses frontendIPConfiguration backendAddressPool probe publicIPAddress privateIPAddress privateIPAllocationMethod subnet networkSecurityGroup routeTable natGateway addressSpace addressPrefixes addressPrefix subnets publicIpAddresses publicIpPrefixes idleTimeoutInMinutes protocol frontendPort backendPort port enableFloatingIP loadDistribution requestPath intervalInSeconds numberOfProbes ipAddress virtualNetwork backendAddressPools network subnetwork sourceIpRanges destinationIpRanges sourceRanges destinationRanges IPAddress IPProtocol ports portRange backendService healthChecks backends group balancingMode capacityScaler maxUtilization sessionAffinity loadBalancingScheme timeoutSec namedPorts nats natIpAllocateOption natIps sourceSubnetworkIpRangesToNat subnetworks sourceIpRangesToNat secondaryIpRangeNames enableDynamicPortAllocation minPortsPerVm maxPortsPerVm enableEndpointIndependentMapping logConfig enable filter routerInterfaces interfaces ipCidrRange secondaryIpRanges rangeName enableFlowLogs privateIpGoogleAccess routingConfig routingMode autoCreateSubnetworks mtu BGP bgp asn advertisedIpRanges range advertisedGroups advertiseMode") {
		topologyFields[key] = true
	}
}

func safeTopology(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for key, item := range v {
			if topologyFields[key] {
				out[key] = safeTopology(item)
			}
		}
		return out
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			out = append(out, safeTopology(item))
		}
		return out
	case string, bool, float64, nil:
		return v
	default:
		return nil
	}
}

func networkDetailRow(cloud model.Provider, profile, scope, region, typ string, detail map[string]any) map[string]any {
	id := stringValue(detail["id"])
	if cloud == model.ProviderGCP {
		id = stringValue(detail["selfLink"])
	}
	row := baseRow(cloud, profile, id, stringValue(detail["name"]), "network", typ, region, scope, time.Now())
	row["domain"] = "network"
	row["kind"] = "network"
	if strings.Contains(strings.ToLower(typ), "securitygroups") || strings.Contains(strings.ToLower(typ), "firewalls") {
		row["kind"] = "security_group"
	}
	if strings.Contains(strings.ToLower(typ), "subnetwork") {
		row["kind"] = "subnet"
	}
	if strings.Contains(strings.ToLower(typ), "loadbalancer") || strings.Contains(strings.ToLower(typ), "backendservice") || strings.Contains(strings.ToLower(typ), "forwardingrule") {
		row["kind"] = "load_balancer"
	}
	row["attributes"].(map[string]any)["topology"] = safeTopology(detail)
	return row
}

func (a *azureAdapter) readNetworkDetail(ctx context.Context, req provider.NativeRequest, spec deepDetailSpec) (provider.Page, error) {
	if err := validateDeepDetailRequest(spec, req); err != nil {
		return provider.Page{}, err
	}
	subRaw, err := nativeIdentifier(req.Params, "subscription_id")
	if err != nil {
		return provider.Page{}, err
	}
	sub, err := exactDetailScope(a.profile.Scopes.Subscriptions, subRaw, spec.operation, "subscription_id")
	if err != nil {
		return provider.Page{}, err
	}
	rg, err := nativeIdentifier(req.Params, "resource_group")
	if err != nil {
		return provider.Page{}, err
	}
	name, err := nativeIdentifier(req.Params, "name")
	if err != nil {
		return provider.Page{}, err
	}
	typ, ok := azureNetworkTypes[spec.operation]
	if !ok {
		return provider.Page{}, fmt.Errorf("unregistered network operation")
	}
	token, err := a.accessToken(ctx, spec.operation)
	if err != nil {
		return provider.Page{}, err
	}
	endpoint := "https://management.azure.com/subscriptions/" + url.PathEscape(sub) + "/resourceGroups/" + url.PathEscape(rg) + "/providers/Microsoft.Network/" + typ + "/" + url.PathEscape(name) + "?api-version=2024-05-01"
	detail := map[string]any{}
	if err := azureDetailGET(ctx, endpoint, token, spec.operation, &detail); err != nil {
		return provider.Page{}, err
	}
	if stringValue(detail["id"]) == "" {
		return provider.Page{}, deepNotFound(spec.operation, spec.kind)
	}
	return oneDeepDetailPage(networkDetailRow(a.Provider(), a.name, sub, stringValue(detail["location"]), "Microsoft.Network/"+typ, detail)), nil
}

func (a *gcpAdapter) readNetworkDetail(ctx context.Context, req provider.NativeRequest, spec deepDetailSpec) (provider.Page, error) {
	if err := validateDeepDetailRequest(spec, req); err != nil {
		return provider.Page{}, err
	}
	projectRaw, err := nativeIdentifier(req.Params, "project_id")
	if err != nil {
		return provider.Page{}, err
	}
	configured := append([]string(nil), a.profile.Scopes.Projects...)
	if scope := a.profile.Options["asset_scope"]; strings.HasPrefix(scope, "projects/") {
		configured = append(configured, scopeTail(scope))
	}
	project, err := exactDetailScope(configured, projectRaw, spec.operation, "project_id")
	if err != nil {
		return provider.Page{}, err
	}
	name, err := nativeIdentifier(req.Params, "name")
	if err != nil {
		return provider.Page{}, err
	}
	typ, ok := gcpNetworkTypes[spec.operation]
	if !ok {
		return provider.Page{}, fmt.Errorf("unregistered network operation")
	}
	region := "global"
	path := typ
	if !strings.HasPrefix(typ, "global/") {
		region, err = nativeIdentifier(req.Params, "location")
		if err != nil {
			return provider.Page{}, err
		}
		if !cloudRegionPattern.MatchString(region) {
			return provider.Page{}, deepParameterError(spec.operation, fmt.Errorf("invalid location"))
		}
		path = "regions/" + url.PathEscape(region) + "/" + typ
	}
	client, err := a.httpClient(ctx)
	if err != nil {
		return provider.Page{}, err
	}
	detail := map[string]any{}
	endpoint := "https://compute.googleapis.com/compute/v1/projects/" + url.PathEscape(project) + "/" + path + "/" + url.PathEscape(name)
	if err := gcpJSONRequest(ctx, client, http.MethodGet, endpoint, nil, spec.operation, &detail); err != nil {
		return provider.Page{}, err
	}
	if stringValue(detail["name"]) == "" {
		return provider.Page{}, deepNotFound(spec.operation, spec.kind)
	}
	return oneDeepDetailPage(networkDetailRow(a.Provider(), a.name, project, region, "compute.googleapis.com/"+lastName(typ), detail)), nil
}
