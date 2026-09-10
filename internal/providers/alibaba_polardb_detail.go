package providers

import (
	"context"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/polardb"
	"mcpcloud/internal/config"
	"mcpcloud/internal/provider"
	"strings"
	"time"
)

const alibabaPolarDBDetailOperation = "alibaba.polardb.describe_db_cluster_attribute"

type alibabaPolarDBDetailAPI interface {
	DescribeDBClusterAttribute(*polardb.DescribeDBClusterAttributeRequest) (*polardb.DescribeDBClusterAttributeResponse, error)
}

var newAlibabaPolarDBDetailClient = func(p config.Profile, region string) (alibabaPolarDBDetailAPI, error) {
	if p.Credential.Source != "env" {
		return polardb.NewClient()
	}
	access := envValue(p, "ALIBABA_CLOUD_ACCESS_KEY_ID", "ALIBABA_CLOUD_ACCESS_KEY_ID")
	secret := envValue(p, "ALIBABA_CLOUD_ACCESS_KEY_SECRET", "ALIBABA_CLOUD_ACCESS_KEY_SECRET")
	token := envValue(p, "ALIBABA_CLOUD_SECURITY_TOKEN", "ALIBABA_CLOUD_SECURITY_TOKEN")
	if access == "" || secret == "" {
		return nil, &provider.Error{Code: "missing_credentials", Operation: alibabaPolarDBDetailOperation, Message: "Alibaba Cloud credential environment variables are not set"}
	}
	if token != "" {
		return polardb.NewClientWithStsToken(region, access, secret, token)
	}
	return polardb.NewClientWithAccessKey(region, access, secret)
}

func (a *alibabaAdapter) alibabaPolarDBDetail(ctx context.Context, spec deepDetailSpec, req provider.NativeRequest, account, region string) (provider.Page, error) {
	id, err := nativeIdentifier(req.Params, "db_cluster_id")
	if err != nil {
		return provider.Page{}, deepParameterError(spec.operation, err)
	}
	if err := ctx.Err(); err != nil {
		return provider.Page{}, err
	}
	c, err := newAlibabaPolarDBDetailClient(a.profile, region)
	if err != nil {
		return provider.Page{}, attributeInstanceDetailError(err, spec.operation)
	}
	r := polardb.CreateDescribeDBClusterAttributeRequest()
	r.DBClusterId = id
	setAlibabaTimeout(ctx, r.RpcRequest)
	p, err := c.DescribeDBClusterAttribute(r)
	if err != nil {
		return provider.Page{}, alibabaDeepError(spec.operation, err)
	}
	if err := ctx.Err(); err != nil {
		return provider.Page{Requests: 1}, err
	}
	if p == nil || p.DBClusterId != id || p.RegionId != region {
		return provider.Page{Requests: 1}, deepNotFound(spec.operation, spec.kind)
	}
	nodes := []map[string]any{}
	seen := map[string]bool{}
	for _, n := range p.DBNodes {
		if n.DBNodeId == "" || seen[n.DBNodeId] || (n.RegionId != "" && n.RegionId != region) {
			return provider.Page{Requests: 1}, &provider.Error{Code: "invalid_provider_response", Operation: spec.operation, Message: "PolarDB node identity is invalid"}
		}
		seen[n.DBNodeId] = true
		nodes = append(nodes, map[string]any{"id": n.DBNodeId, "role": n.DBNodeRole, "state": strings.ToLower(n.DBNodeStatus), "instance_type": n.DBNodeClass})
	}
	name := p.DBClusterDescription
	if name == "" {
		name = id
	}
	row := newDeepDetailRow(a.Provider(), a.name, id, name, "polardb", "ACS::PolarDB::DBCluster", "database", "database", region, account)
	row["state"] = strings.ToLower(p.DBClusterStatus)
	setDetailTime(row, "created_at", p.CreationTime, time.RFC3339, time.RFC3339Nano)
	attrs := row["attributes"].(map[string]any)
	attrs["engine"] = strings.ToLower(p.DBType)
	attrs["engine_version"] = p.DBVersion
	attrs["billing_mode"] = p.PayType
	attrs["nodes"] = nodes
	return oneDeepDetailPage(row), nil
}
