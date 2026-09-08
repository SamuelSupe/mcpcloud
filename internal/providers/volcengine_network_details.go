package providers

import (
	"context"
	"strconv"
	"strings"
	"time"

	volctos "github.com/volcengine/ve-tos-golang-sdk/v2/tos"
	"github.com/volcengine/ve-tos-golang-sdk/v2/tos/codes"
	"github.com/volcengine/volcengine-go-sdk/service/clb"
	"github.com/volcengine/volcengine-go-sdk/service/natgateway"
	"github.com/volcengine/volcengine-go-sdk/service/vpc"
	volc "github.com/volcengine/volcengine-go-sdk/volcengine"
	"github.com/volcengine/volcengine-go-sdk/volcengine/request"
	"github.com/volcengine/volcengine-go-sdk/volcengine/session"

	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

const (
	volcengineCLBDetailOperation        = "volcengine.clb.describe_load_balancer_attributes"
	volcengineCLBListenersOperation     = "volcengine.clb.list_listeners"
	volcengineCLBHealthOperation        = "volcengine.clb.list_listener_health"
	volcengineCLBServerGroupOperation   = "volcengine.clb.describe_server_group_attributes"
	volcengineEIPDetailOperation        = "volcengine.vpc.describe_eip_address_attributes"
	volcengineNATListOperation          = "volcengine.nat.list_nat_gateways"
	volcengineNATDetailOperation        = "volcengine.nat.describe_nat_gateway_attributes"
	volcengineNATListSNATOperation      = "volcengine.nat.list_snat_entries"
	volcengineNATListDNATOperation      = "volcengine.nat.list_dnat_entries"
	volcengineTOSConfigurationOperation = "volcengine.tos.get_bucket_configuration"
)

type volcengineCLBReadAPI interface {
	DescribeLoadBalancerAttributesWithContext(volc.Context, *clb.DescribeLoadBalancerAttributesInput, ...request.Option) (*clb.DescribeLoadBalancerAttributesOutput, error)
	DescribeListenersWithContext(volc.Context, *clb.DescribeListenersInput, ...request.Option) (*clb.DescribeListenersOutput, error)
	DescribeListenerHealthWithContext(volc.Context, *clb.DescribeListenerHealthInput, ...request.Option) (*clb.DescribeListenerHealthOutput, error)
	DescribeServerGroupAttributesWithContext(volc.Context, *clb.DescribeServerGroupAttributesInput, ...request.Option) (*clb.DescribeServerGroupAttributesOutput, error)
}

type volcengineVPCReadAPI interface {
	DescribeEipAddressAttributesWithContext(volc.Context, *vpc.DescribeEipAddressAttributesInput, ...request.Option) (*vpc.DescribeEipAddressAttributesOutput, error)
}

type volcengineNATReadAPI interface {
	DescribeNatGatewaysWithContext(volc.Context, *natgateway.DescribeNatGatewaysInput, ...request.Option) (*natgateway.DescribeNatGatewaysOutput, error)
	DescribeNatGatewayAttributesWithContext(volc.Context, *natgateway.DescribeNatGatewayAttributesInput, ...request.Option) (*natgateway.DescribeNatGatewayAttributesOutput, error)
	DescribeSnatEntriesWithContext(volc.Context, *natgateway.DescribeSnatEntriesInput, ...request.Option) (*natgateway.DescribeSnatEntriesOutput, error)
	DescribeDnatEntriesWithContext(volc.Context, *natgateway.DescribeDnatEntriesInput, ...request.Option) (*natgateway.DescribeDnatEntriesOutput, error)
}

var newVolcengineCLBReadClient = func(s *session.Session) volcengineCLBReadAPI { return clb.New(s) }
var newVolcengineVPCReadClient = func(s *session.Session) volcengineVPCReadAPI { return vpc.New(s) }
var newVolcengineNATReadClient = func(s *session.Session) volcengineNATReadAPI { return natgateway.New(s) }

func volcengineNetworkDetailOperations() []provider.Operation {
	return []provider.Operation{
		operation(volcengineCLBDetailOperation, model.ProviderVolcengine, "clb", "Describe one CLB instance", cloneNativeSchema(detailParameters("load_balancer_id"))),
		operation(volcengineCLBListenersOperation, model.ProviderVolcengine, "clb", "List listeners for one CLB instance", cloneNativeSchema(detailParameters("load_balancer_id"))),
		operation(volcengineCLBHealthOperation, model.ProviderVolcengine, "clb", "List backend health for one CLB listener", cloneNativeSchema(detailParameters("listener_id"))),
		operation(volcengineCLBServerGroupOperation, model.ProviderVolcengine, "clb", "Describe one CLB server group and its backend membership", cloneNativeSchema(detailParameters("server_group_id"))),
		operation(volcengineEIPDetailOperation, model.ProviderVolcengine, "vpc", "Describe one EIP", cloneNativeSchema(detailParameters("allocation_id"))),
		operation(volcengineNATListOperation, model.ProviderVolcengine, "natgateway", "List public or private NAT gateways in one exact project", cloneNativeSchema(volcengineNATListParameters())),
		operation(volcengineNATDetailOperation, model.ProviderVolcengine, "natgateway", "Describe one public NAT gateway", cloneNativeSchema(detailParameters("nat_gateway_id"))),
		operation(volcengineNATListSNATOperation, model.ProviderVolcengine, "natgateway", "List SNAT entries for one NAT gateway", cloneNativeSchema(detailParameters("nat_gateway_id"))),
		operation(volcengineNATListDNATOperation, model.ProviderVolcengine, "natgateway", "List DNAT entries for one NAT gateway", cloneNativeSchema(detailParameters("nat_gateway_id"))),
		operation(volcengineTOSConfigurationOperation, model.ProviderVolcengine, "tos", "Read one bucket's ACL, versioning, lifecycle summary, and delayed usage statistics", cloneNativeSchema(detailParameters("bucket_name"))),
	}
}

func isVolcengineNetworkDetailOperation(name string) bool {
	for _, op := range volcengineNetworkDetailOperations() {
		if op.Name == name {
			return true
		}
	}
	return false
}

func volcengineNetworkDetailOperationNames() []string {
	ops := volcengineNetworkDetailOperations()
	names := make([]string, 0, len(ops))
	for _, op := range ops {
		names = append(names, op.Name)
	}
	return names
}

func volcengineNATListParameters() map[string]any {
	return map[string]any{
		"project_name": map[string]any{"type": "string", "required": true},
		"network_type": map[string]any{"type": "string"},
	}
}

func validateVolcengineNetworkRequest(req provider.NativeRequest) (string, int64, int64, error) {
	key := "nat_gateway_id"
	switch req.Operation {
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
	case volcengineNATDetailOperation, volcengineNATListSNATOperation, volcengineNATListDNATOperation:
	default:
		return "", 0, 0, &provider.Error{Code: "operation_not_allowed", Operation: req.Operation, Message: "operation is not registered"}
	}
	var selected provider.Operation
	for _, op := range volcengineNetworkDetailOperations() {
		if op.Name == req.Operation {
			selected = op
			break
		}
	}
	if err := selected.ValidateParams(req.Params); err != nil {
		return "", 0, 0, &provider.Error{Code: "invalid_parameter", Operation: req.Operation, Message: err.Error()}
	}
	if req.Operation == volcengineNATListOperation {
		networkType, e := nativeString(req.Params, "network_type")
		if e != nil {
			return "", 0, 0, e
		}
		if networkType != "" && networkType != "internet" && networkType != "intranet" {
			return "", 0, 0, &provider.Error{Code: "invalid_parameter", Operation: req.Operation, Message: "network_type must be internet or intranet"}
		}
	}
	id, err := nativeIdentifier(req.Params, key)
	if err != nil {
		return "", 0, 0, deepParameterError(req.Operation, err)
	}
	page := int64(1)
	if req.PageToken != "" {
		parsed, e := strconv.ParseInt(req.PageToken, 10, 64)
		if e != nil || parsed < 2 || parsed > 1_000_000 {
			return "", 0, 0, &provider.Error{Code: "invalid_cursor", Operation: req.Operation, Message: "provider cursor is invalid"}
		}
		page = parsed
	}
	if req.Operation == volcengineCLBDetailOperation || req.Operation == volcengineCLBServerGroupOperation || req.Operation == volcengineEIPDetailOperation || req.Operation == volcengineNATDetailOperation || req.Operation == volcengineTOSConfigurationOperation {
		if req.PageToken != "" {
			return "", 0, 0, &provider.Error{Code: "invalid_cursor", Operation: req.Operation, Message: "detail operation does not paginate"}
		}
	}
	limit := int64(req.Limit)
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	return id, page, limit, nil
}

func (a *volcengineAdapter) readNetworkDetail(ctx context.Context, req provider.NativeRequest) (provider.Page, error) {
	id, page, size, err := validateVolcengineNetworkRequest(req)
	if err != nil {
		return provider.Page{}, err
	}
	account, err := singleDetailAccount(a.profile, req.Operation)
	if err != nil {
		return provider.Page{}, err
	}
	region, err := exactDetailRegion(req.Region, a.profile.Regions, req.Operation)
	if err != nil {
		return provider.Page{}, err
	}
	if req.Operation == volcengineTOSConfigurationOperation {
		client, e := newVolcengineTOSDetailClient(a.profile, region, req.Operation)
		if e != nil {
			return provider.Page{}, e
		}
		defer client.Close()
		acl, e := client.GetBucketACL(ctx, &volctos.GetBucketACLInput{Bucket: id})
		if e != nil {
			return provider.Page{}, volcengineDeepError(req.Operation, e)
		}
		version, e := client.GetBucketVersioning(ctx, &volctos.GetBucketVersioningInput{Bucket: id})
		if e != nil {
			return provider.Page{}, volcengineDeepError(req.Operation, e)
		}
		lifecycle, e := client.GetBucketLifecycle(ctx, &volctos.GetBucketLifecycleInput{Bucket: id})
		if e != nil && volctos.Code(e) != codes.NoSuchLifecycleConfiguration {
			return provider.Page{}, volcengineDeepError(req.Operation, e)
		}
		stats, e := client.GetBucketStat(ctx, &volctos.GetBucketStatInput{Bucket: id})
		if e != nil {
			return provider.Page{}, volcengineDeepError(req.Operation, e)
		}
		row := a.volcengineTOSConfigurationRow(id, acl, version, lifecycle, stats, region, account)
		return provider.Page{Rows: []map[string]any{row}, Scanned: 1, Requests: 4}, nil
	}
	sess, err := a.session(region, req.Operation)
	if err != nil {
		return provider.Page{}, err
	}
	switch req.Operation {
	case volcengineCLBDetailOperation:
		o, e := newVolcengineCLBReadClient(sess).DescribeLoadBalancerAttributesWithContext(ctx, &clb.DescribeLoadBalancerAttributesInput{LoadBalancerId: volc.String(id)})
		if e != nil {
			return provider.Page{}, volcengineDeepError(req.Operation, e)
		}
		if o == nil || volc.StringValue(o.LoadBalancerId) != id {
			return provider.Page{Requests: 1}, deepNotFound(req.Operation, "load_balancer")
		}
		return oneDeepDetailPage(a.volcengineCLBRow(o, region, account)), nil
	case volcengineCLBListenersOperation:
		o, e := newVolcengineCLBReadClient(sess).DescribeListenersWithContext(ctx, &clb.DescribeListenersInput{LoadBalancerId: volc.String(id), PageNumber: &page, PageSize: &size})
		if e != nil {
			return provider.Page{}, volcengineDeepError(req.Operation, e)
		}
		if o == nil {
			return provider.Page{Requests: 1}, deepInvalidResponse(req.Operation)
		}
		rows := make([]map[string]any, 0, len(o.Listeners))
		for _, x := range o.Listeners {
			if x != nil && volc.StringValue(x.LoadBalancerId) == id {
				rows = append(rows, a.volcengineCLBListenerRow(x, region, account))
			}
		}
		return volcengineNetworkPage(rows, page, size, volc.Int64Value(o.TotalCount)), nil
	case volcengineCLBHealthOperation:
		o, e := newVolcengineCLBReadClient(sess).DescribeListenerHealthWithContext(ctx, &clb.DescribeListenerHealthInput{ListenerId: volc.String(id), PageNumber: &page, PageSize: &size})
		if e != nil {
			return provider.Page{}, volcengineDeepError(req.Operation, e)
		}
		if o == nil {
			return provider.Page{Requests: 1}, deepInvalidResponse(req.Operation)
		}
		rows := make([]map[string]any, 0, len(o.Results))
		for _, x := range o.Results {
			if x != nil {
				rows = append(rows, a.volcengineCLBHealthRow(x, id, region, account))
			}
		}
		return volcengineNetworkPage(rows, page, size, volc.Int64Value(o.TotalCount)), nil
	case volcengineCLBServerGroupOperation:
		o, e := newVolcengineCLBReadClient(sess).DescribeServerGroupAttributesWithContext(ctx, &clb.DescribeServerGroupAttributesInput{ServerGroupId: volc.String(id)})
		if e != nil {
			return provider.Page{}, volcengineDeepError(req.Operation, e)
		}
		if o == nil || volc.StringValue(o.ServerGroupId) != id {
			return provider.Page{Requests: 1}, deepNotFound(req.Operation, "server_group")
		}
		return oneDeepDetailPage(a.volcengineCLBServerGroupRow(o, region, account)), nil
	case volcengineEIPDetailOperation:
		o, e := newVolcengineVPCReadClient(sess).DescribeEipAddressAttributesWithContext(ctx, &vpc.DescribeEipAddressAttributesInput{AllocationId: volc.String(id)})
		if e != nil {
			return provider.Page{}, volcengineDeepError(req.Operation, e)
		}
		if o == nil || volc.StringValue(o.AllocationId) != id {
			return provider.Page{Requests: 1}, deepNotFound(req.Operation, "public_ip")
		}
		return oneDeepDetailPage(a.volcengineEIPRow(o, region, account)), nil
	case volcengineNATDetailOperation:
		o, e := newVolcengineNATReadClient(sess).DescribeNatGatewayAttributesWithContext(ctx, &natgateway.DescribeNatGatewayAttributesInput{NatGatewayId: volc.String(id)})
		if e != nil {
			return provider.Page{}, volcengineDeepError(req.Operation, e)
		}
		if o == nil || volc.StringValue(o.NatGatewayId) != id {
			return provider.Page{Requests: 1}, deepNotFound(req.Operation, "nat_gateway")
		}
		return oneDeepDetailPage(a.volcengineNATRow(o, region, account)), nil
	case volcengineNATListOperation:
		input := &natgateway.DescribeNatGatewaysInput{ProjectName: volc.String(id), PageNumber: &page, PageSize: &size}
		if networkType, _ := nativeString(req.Params, "network_type"); networkType != "" {
			input.NetworkType = volc.String(networkType)
		}
		o, e := newVolcengineNATReadClient(sess).DescribeNatGatewaysWithContext(ctx, input)
		if e != nil {
			return provider.Page{}, volcengineDeepError(req.Operation, e)
		}
		if o == nil {
			return provider.Page{Requests: 1}, deepInvalidResponse(req.Operation)
		}
		rows := make([]map[string]any, 0, len(o.NatGateways))
		for _, x := range o.NatGateways {
			if x != nil && volc.StringValue(x.ProjectName) == id {
				rows = append(rows, a.volcengineNATListRow(x, region, account))
			}
		}
		return volcengineNetworkPage(rows, page, size, volc.Int64Value(o.TotalCount)), nil
	case volcengineNATListSNATOperation:
		o, e := newVolcengineNATReadClient(sess).DescribeSnatEntriesWithContext(ctx, &natgateway.DescribeSnatEntriesInput{NatGatewayId: volc.String(id), PageNumber: &page, PageSize: &size})
		if e != nil {
			return provider.Page{}, volcengineDeepError(req.Operation, e)
		}
		if o == nil {
			return provider.Page{Requests: 1}, deepInvalidResponse(req.Operation)
		}
		rows := make([]map[string]any, 0, len(o.SnatEntries))
		for _, x := range o.SnatEntries {
			if x != nil && volc.StringValue(x.NatGatewayId) == id {
				rows = append(rows, a.volcengineSNATRow(x, region, account))
			}
		}
		return volcengineNetworkPage(rows, page, size, volc.Int64Value(o.TotalCount)), nil
	default:
		o, e := newVolcengineNATReadClient(sess).DescribeDnatEntriesWithContext(ctx, &natgateway.DescribeDnatEntriesInput{NatGatewayId: volc.String(id), PageNumber: &page, PageSize: &size})
		if e != nil {
			return provider.Page{}, volcengineDeepError(req.Operation, e)
		}
		if o == nil {
			return provider.Page{Requests: 1}, deepInvalidResponse(req.Operation)
		}
		rows := make([]map[string]any, 0, len(o.DnatEntries))
		for _, x := range o.DnatEntries {
			if x != nil && volc.StringValue(x.NatGatewayId) == id {
				rows = append(rows, a.volcengineDNATRow(x, region, account))
			}
		}
		return volcengineNetworkPage(rows, page, size, volc.Int64Value(o.TotalCount)), nil
	}
}

func volcengineNetworkPage(rows []map[string]any, page, size, total int64) provider.Page {
	next := ""
	if page*size < total {
		next = strconv.FormatInt(page+1, 10)
	}
	return provider.Page{Rows: rows, NextToken: next, Scanned: len(rows), Requests: 1}
}

func deepInvalidResponse(operation string) error {
	return &provider.Error{Code: "invalid_provider_response", Operation: operation, Message: "Volcengine API returned no response"}
}

func (a *volcengineAdapter) volcengineTOSConfigurationRow(name string, acl *volctos.GetBucketACLOutput, version *volctos.GetBucketVersioningOutputV2, lifecycle *volctos.GetBucketLifecycleOutput, stats *volctos.GetBucketStatOutput, region, account string) map[string]any {
	r := newDeepDetailRow(a.Provider(), a.name, name, name, "tos", "TOS::BucketConfiguration", "storage", "bucket_configuration", region, account)
	r["state"] = "available"
	at := r["attributes"].(map[string]any)
	if acl != nil {
		at["acl_grant_count"] = len(acl.Grants)
		setPosture(r, "bucket_acl_delivered", acl.BucketAclDelivered)
		publicRead, publicWrite := false, false
		for _, grant := range acl.Grants {
			who, permission := strings.ToLower(string(grant.GranteeV2.Canned)), strings.ToLower(string(grant.Permission))
			if strings.Contains(who, "allusers") {
				publicRead = publicRead || strings.Contains(permission, "read")
				publicWrite = publicWrite || strings.Contains(permission, "write")
			}
		}
		setPosture(r, "public_read", publicRead)
		setPosture(r, "public_write", publicWrite)
	}
	if version != nil {
		at["versioning_status"] = string(version.Status)
		setPosture(r, "versioning_enabled", strings.EqualFold(string(version.Status), "Enabled"))
	}
	if lifecycle == nil {
		at["lifecycle_rule_count"], at["enabled_lifecycle_rule_count"] = 0, 0
	} else {
		enabled := 0
		for _, rule := range lifecycle.Rules {
			if strings.EqualFold(string(rule.Status), "Enabled") {
				enabled++
			}
		}
		at["lifecycle_rule_count"], at["enabled_lifecycle_rule_count"] = len(lifecycle.Rules), enabled
	}
	if stats != nil && stats.TotalStorageStat != nil {
		at["object_count"] = stats.TotalStorageStat.ObjectCount
		at["storage_bytes"] = stats.TotalStorageStat.Storage
		at["charge_storage_bytes"] = stats.TotalStorageStat.ChargeStorage
	}
	return r
}

func (a *volcengineAdapter) volcengineCLBRow(x *clb.DescribeLoadBalancerAttributesOutput, region, account string) map[string]any {
	r := newDeepDetailRow(a.Provider(), a.name, volc.StringValue(x.LoadBalancerId), volc.StringValue(x.LoadBalancerName), "clb", "CLB::CLB", "network", "load_balancer", region, account)
	r["state"] = strings.ToLower(volc.StringValue(x.Status))
	setDetailTime(r, "created_at", volc.StringValue(x.CreateTime), time.RFC3339)
	at := r["attributes"].(map[string]any)
	at["type"] = volc.StringValue(x.Type)
	at["spec"] = volc.StringValue(x.LoadBalancerSpec)
	at["billing_type"] = volc.Int64Value(x.LoadBalancerBillingType)
	at["project_name"] = volc.StringValue(x.ProjectName)
	at["vpc_id"] = volc.StringValue(x.VpcId)
	at["subnet_id"] = volc.StringValue(x.SubnetId)
	at["public_ip_address"] = volc.StringValue(x.EipAddress)
	at["private_ip_address"] = volc.StringValue(x.EniAddress)
	setRelated(r, "listener_ids", volcengineCLBListenerIDs(x.Listeners))
	setRelated(r, "server_group_ids", volcengineCLBServerGroupIDs(x.ServerGroups))
	setRelated(r, "availability_zones", []string{volc.StringValue(x.MasterZoneId), volc.StringValue(x.SlaveZoneId)})
	setPosture(r, "modification_protection_enabled", volcengineFlagEnabled(volc.StringValue(x.ModificationProtectionStatus)))
	return r
}
func volcengineCLBListenerIDs(xs []*clb.ListenerForDescribeLoadBalancerAttributesOutput) []string {
	r := []string{}
	for _, x := range xs {
		if x != nil {
			r = append(r, volc.StringValue(x.ListenerId))
		}
	}
	return r
}
func volcengineCLBServerGroupIDs(xs []*clb.ServerGroupForDescribeLoadBalancerAttributesOutput) []string {
	r := []string{}
	for _, x := range xs {
		if x != nil {
			r = append(r, volc.StringValue(x.ServerGroupId))
		}
	}
	return r
}
func (a *volcengineAdapter) volcengineCLBListenerRow(x *clb.ListenerForDescribeListenersOutput, region, account string) map[string]any {
	r := newDeepDetailRow(a.Provider(), a.name, volc.StringValue(x.ListenerId), volc.StringValue(x.ListenerName), "clb", "CLB::Listener", "network", "listener", region, account)
	r["state"] = strings.ToLower(volc.StringValue(x.Status))
	at := r["attributes"].(map[string]any)
	at["load_balancer_id"] = volc.StringValue(x.LoadBalancerId)
	at["protocol"] = volc.StringValue(x.Protocol)
	at["port"] = volc.Int64Value(x.Port)
	at["server_group_id"] = volc.StringValue(x.ServerGroupId)
	at["scheduler"] = volc.StringValue(x.Scheduler)
	if x.HealthCheck != nil {
		at["health_check_enabled"] = volcengineFlagEnabled(volc.StringValue(x.HealthCheck.Enabled))
		at["health_check_interval_seconds"] = volc.Int64Value(x.HealthCheck.Interval)
		at["health_check_timeout_seconds"] = volc.Int64Value(x.HealthCheck.Timeout)
	}
	return r
}
func (a *volcengineAdapter) volcengineCLBHealthRow(x *clb.ResultForDescribeListenerHealthOutput, listenerID, region, account string) map[string]any {
	id := volc.StringValue(x.ServerId)
	if id == "" {
		id = volc.StringValue(x.InstanceId)
	}
	r := newDeepDetailRow(a.Provider(), a.name, id, id, "clb", "CLB::BackendHealth", "network", "backend_health", region, account)
	r["state"] = strings.ToLower(volc.StringValue(x.Status))
	at := r["attributes"].(map[string]any)
	at["listener_id"] = listenerID
	at["server_group_id"] = volc.StringValue(x.ServerGroupId)
	at["instance_id"] = volc.StringValue(x.InstanceId)
	at["backend_type"] = volc.StringValue(x.Type)
	return r
}

func (a *volcengineAdapter) volcengineCLBServerGroupRow(x *clb.DescribeServerGroupAttributesOutput, region, account string) map[string]any {
	r := newDeepDetailRow(a.Provider(), a.name, volc.StringValue(x.ServerGroupId), volc.StringValue(x.ServerGroupName), "clb", "CLB::ServerGroup", "network", "server_group", region, account)
	r["state"] = "available"
	at := r["attributes"].(map[string]any)
	at["load_balancer_id"] = volc.StringValue(x.LoadBalancerId)
	at["type"] = volc.StringValue(x.Type)
	at["address_ip_version"] = volc.StringValue(x.AddressIpVersion)
	at["server_count"] = len(x.Servers)
	serverIDs, instanceIDs := []string{}, []string{}
	for _, server := range x.Servers {
		if server != nil {
			serverIDs = append(serverIDs, volc.StringValue(server.ServerId))
			instanceIDs = append(instanceIDs, volc.StringValue(server.InstanceId))
		}
	}
	setRelated(r, "listener_ids", volcStringValues(x.Listeners))
	setRelated(r, "backend_server_ids", serverIDs)
	setRelated(r, "backend_instance_ids", instanceIDs)
	setPosture(r, "any_port_enabled", volcengineFlagEnabled(volc.StringValue(x.AnyPortEnabled)))
	return r
}
func (a *volcengineAdapter) volcengineEIPRow(x *vpc.DescribeEipAddressAttributesOutput, region, account string) map[string]any {
	r := newDeepDetailRow(a.Provider(), a.name, volc.StringValue(x.AllocationId), volc.StringValue(x.Name), "vpc", "VPC::EIP", "network", "public_ip", region, account)
	r["state"] = strings.ToLower(volc.StringValue(x.Status))
	setDetailTime(r, "created_at", volc.StringValue(x.AllocationTime), time.RFC3339)
	at := r["attributes"].(map[string]any)
	at["public_ip_address"] = volc.StringValue(x.EipAddress)
	at["bandwidth_mbps"] = volc.Int64Value(x.Bandwidth)
	at["billing_type"] = volc.Int64Value(x.BillingType)
	at["isp"] = volc.StringValue(x.ISP)
	at["project_name"] = volc.StringValue(x.ProjectName)
	at["associated_instance_id"] = volc.StringValue(x.InstanceId)
	at["associated_instance_type"] = volc.StringValue(x.InstanceType)
	at["bandwidth_package_id"] = volc.StringValue(x.BandwidthPackageId)
	setPosture(r, "blocked", volc.BoolValue(x.IsBlocked))
	setPosture(r, "release_with_instance", volc.BoolValue(x.ReleaseWithInstance))
	return r
}
func (a *volcengineAdapter) volcengineNATRow(x *natgateway.DescribeNatGatewayAttributesOutput, region, account string) map[string]any {
	r := newDeepDetailRow(a.Provider(), a.name, volc.StringValue(x.NatGatewayId), volc.StringValue(x.NatGatewayName), "natgateway", "NATGateway::NatGateway", "network", "nat_gateway", region, account)
	r["state"] = strings.ToLower(volc.StringValue(x.Status))
	r["zone"] = volc.StringValue(x.ZoneId)
	setDetailTime(r, "created_at", volc.StringValue(x.CreationTime), time.RFC3339)
	at := r["attributes"].(map[string]any)
	at["network_type"] = volc.StringValue(x.NetworkType)
	at["spec"] = volc.StringValue(x.Spec)
	at["billing_type"] = volc.Int64Value(x.BillingType)
	at["project_name"] = volc.StringValue(x.ProjectName)
	at["vpc_id"] = volc.StringValue(x.VpcId)
	at["subnet_id"] = volc.StringValue(x.SubnetId)
	setRelated(r, "snat_entry_ids", volcStringValues(x.SnatEntryIds))
	setRelated(r, "dnat_entry_ids", volcStringValues(x.DnatEntryIds))
	setPosture(r, "smart_schedule_enabled", volc.BoolValue(x.SmartScheduleEnabled))
	return r
}
func (a *volcengineAdapter) volcengineNATListRow(x *natgateway.NatGatewayForDescribeNatGatewaysOutput, region, account string) map[string]any {
	r := newDeepDetailRow(a.Provider(), a.name, volc.StringValue(x.NatGatewayId), volc.StringValue(x.NatGatewayName), "natgateway", "NATGateway::NatGateway", "network", "nat_gateway", region, account)
	r["state"] = strings.ToLower(volc.StringValue(x.Status))
	r["zone"] = volc.StringValue(x.ZoneId)
	setDetailTime(r, "created_at", volc.StringValue(x.CreationTime), time.RFC3339)
	at := r["attributes"].(map[string]any)
	at["network_type"] = volc.StringValue(x.NetworkType)
	at["spec"] = volc.StringValue(x.Spec)
	at["billing_type"] = volc.Int64Value(x.BillingType)
	at["project_name"] = volc.StringValue(x.ProjectName)
	at["vpc_id"] = volc.StringValue(x.VpcId)
	at["subnet_id"] = volc.StringValue(x.SubnetId)
	setRelated(r, "snat_entry_ids", volcStringValues(x.SnatEntryIds))
	setRelated(r, "dnat_entry_ids", volcStringValues(x.DnatEntryIds))
	setPosture(r, "smart_schedule_enabled", volc.BoolValue(x.SmartScheduleEnabled))
	return r
}
func (a *volcengineAdapter) volcengineSNATRow(x *natgateway.SnatEntryForDescribeSnatEntriesOutput, region, account string) map[string]any {
	r := newDeepDetailRow(a.Provider(), a.name, volc.StringValue(x.SnatEntryId), volc.StringValue(x.SnatEntryName), "natgateway", "NATGateway::SnatEntry", "network", "snat_entry", region, account)
	r["state"] = strings.ToLower(volc.StringValue(x.Status))
	at := r["attributes"].(map[string]any)
	at["nat_gateway_id"] = volc.StringValue(x.NatGatewayId)
	at["eip_id"] = volc.StringValue(x.EipId)
	at["nat_ip_id"] = volc.StringValue(x.NatIpId)
	at["subnet_id"] = volc.StringValue(x.SubnetId)
	return r
}
func (a *volcengineAdapter) volcengineDNATRow(x *natgateway.DnatEntryForDescribeDnatEntriesOutput, region, account string) map[string]any {
	r := newDeepDetailRow(a.Provider(), a.name, volc.StringValue(x.DnatEntryId), volc.StringValue(x.DnatEntryName), "natgateway", "NATGateway::DnatEntry", "network", "dnat_entry", region, account)
	r["state"] = strings.ToLower(volc.StringValue(x.Status))
	at := r["attributes"].(map[string]any)
	at["nat_gateway_id"] = volc.StringValue(x.NatGatewayId)
	at["protocol"] = volc.StringValue(x.Protocol)
	at["port_type"] = volc.StringValue(x.PortType)
	return r
}
