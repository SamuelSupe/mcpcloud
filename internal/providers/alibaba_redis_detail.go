package providers

import (
	"context"
	kv "github.com/aliyun/alibaba-cloud-sdk-go/services/r_kvstore"
	"mcpcloud/internal/config"
	"mcpcloud/internal/provider"
	"strings"
	"time"
)

type alibabaRedisDetailAPI interface {
	DescribeInstanceAttribute(*kv.DescribeInstanceAttributeRequest) (*kv.DescribeInstanceAttributeResponse, error)
}

var newAlibabaRedisDetailClient = func(p config.Profile, region string) (alibabaRedisDetailAPI, error) {
	if p.Credential.Source != "env" {
		return kv.NewClient()
	}
	access := envValue(p, "ALIBABA_CLOUD_ACCESS_KEY_ID", "ALIBABA_CLOUD_ACCESS_KEY_ID")
	secret := envValue(p, "ALIBABA_CLOUD_ACCESS_KEY_SECRET", "ALIBABA_CLOUD_ACCESS_KEY_SECRET")
	token := envValue(p, "ALIBABA_CLOUD_SECURITY_TOKEN", "ALIBABA_CLOUD_SECURITY_TOKEN")
	if access == "" || secret == "" {
		return nil, &provider.Error{Code: "missing_credentials", Operation: "alibaba.redis.describe_instance_attribute", Message: "Alibaba Cloud credential environment variables are not set"}
	}
	if token != "" {
		return kv.NewClientWithStsToken(region, access, secret, token)
	}
	return kv.NewClientWithAccessKey(region, access, secret)
}

func (a *alibabaAdapter) alibabaRedisDetail(ctx context.Context, spec deepDetailSpec, req provider.NativeRequest, account, region string) (provider.Page, error) {
	id, e := nativeIdentifier(req.Params, "instance_id")
	if e != nil {
		return provider.Page{}, deepParameterError(spec.operation, e)
	}
	client, e := newAlibabaRedisDetailClient(a.profile, region)
	if e != nil {
		return provider.Page{}, attributeInstanceDetailError(e, spec.operation)
	}
	r := kv.CreateDescribeInstanceAttributeRequest()
	r.InstanceId = id
	setAlibabaTimeout(ctx, r.RpcRequest)
	resp, e := client.DescribeInstanceAttribute(r)
	if e != nil {
		return provider.Page{}, alibabaDeepError(spec.operation, e)
	}
	if e = ctx.Err(); e != nil {
		return provider.Page{}, e
	}
	if resp != nil {
		for _, x := range resp.Instances.DBInstanceAttribute {
			if x.InstanceId == id && x.RegionId == region {
				return oneDeepDetailPage(a.alibabaRedisDetailRow(x, account)), nil
			}
		}
	}
	return provider.Page{Requests: 1}, deepNotFound(spec.operation, spec.kind)
}
func (a *alibabaAdapter) alibabaRedisDetailRow(x kv.DBInstanceAttribute, account string) map[string]any {
	name := x.InstanceName
	if name == "" {
		name = x.InstanceId
	}
	r := newDeepDetailRow(a.Provider(), a.name, x.InstanceId, name, "redis", "ACS::Redis::DBInstance", "database", "cache", x.RegionId, account)
	r["zone"] = x.ZoneId
	r["state"] = strings.ToLower(x.InstanceStatus)
	setDetailTime(r, "created_at", x.CreateTime, time.RFC3339, time.RFC3339Nano)
	attrs := r["attributes"].(map[string]any)
	attrs["engine"] = x.Engine
	attrs["engine_version"] = x.EngineVersion
	attrs["memory_mb"] = x.Capacity
	attrs["instance_type"] = x.InstanceClass
	attrs["architecture_type"] = x.ArchitectureType
	attrs["node_type"] = x.NodeType
	attrs["billing_mode"] = x.ChargeType
	return r
}
