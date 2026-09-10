package providers

import (
	"context"
	"encoding/json"
	tchttp "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/http"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
	"strings"
)

const tencentRedisOperationName = "tencent.redis.describe_instance"

func tencentRedisOperation() provider.Operation {
	return provider.Operation{Name: tencentRedisOperationName, Provider: model.ProviderTencent, Service: "redis", Description: "Get one Redis instance through an ID-filtered DescribeInstances call; excludes connection addresses and secrets", Parameters: detailParameters("instance_id")}
}

type tencentRedisDetail struct {
	ID       string   `json:"InstanceId"`
	Name     string   `json:"InstanceName"`
	Status   *int     `json:"Status"`
	Size     *float64 `json:"Size"`
	Shards   *int     `json:"RedisShardNum"`
	Replicas *int     `json:"RedisReplicasNum"`
	Engine   string   `json:"Engine"`
	Version  string   `json:"CurrentRedisVersion"`
	VPC      string   `json:"UniqVpcId"`
	Subnet   string   `json:"UniqSubnetId"`
	Project  *int64   `json:"ProjectId"`
	Billing  *int     `json:"BillingMode"`
	Tags     []struct {
		Key   string `json:"TagKey"`
		Value string `json:"TagValue"`
	} `json:"InstanceTags"`
}

func (a *tencentAdapter) readRedisDetail(ctx context.Context, req provider.NativeRequest) (provider.Page, error) {
	op := tencentRedisOperation()
	if err := op.ValidateParams(req.Params); err != nil {
		return provider.Page{}, err
	}
	if req.PageToken != "" {
		return provider.Page{}, &provider.Error{Code: "cursor_not_supported", Operation: op.Name, Message: "single Redis detail does not accept a cursor"}
	}
	id, err := nativeIdentifier(req.Params, "instance_id")
	if err != nil {
		return provider.Page{}, deepParameterError(op.Name, err)
	}
	if id == "" {
		return provider.Page{}, &provider.Error{Code: "missing_parameter", Operation: op.Name, Message: "instance_id is required"}
	}
	account, err := singleDetailAccount(a.profile, op.Name)
	if err != nil {
		return provider.Page{}, err
	}
	region, err := exactDetailRegion(req.Region, a.profile.Regions, op.Name)
	if err != nil {
		return provider.Page{}, err
	}
	client, err := a.tencentClient("redis.tencentcloudapi.com", region, op.Name)
	if err != nil {
		return provider.Page{}, err
	}
	request := tchttp.NewCommonRequest("redis", "2018-04-12", "DescribeInstances")
	request.SetContext(ctx)
	if err := request.SetActionParameters(map[string]any{"InstanceId": id, "Limit": 1}); err != nil {
		return provider.Page{}, err
	}
	response := tchttp.NewCommonResponse()
	if err := client.Send(request, response); err != nil {
		return provider.Page{}, tencentSourceError(op.Name, err)
	}
	var payload struct {
		Response struct {
			InstanceSet []tencentRedisDetail `json:"InstanceSet"`
			Error       *struct {
				Code    string `json:"Code"`
				Message string `json:"Message"`
			} `json:"Error"`
		} `json:"Response"`
	}
	if err := json.Unmarshal(response.GetBody(), &payload); err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: op.Name, Message: "invalid Redis detail response"}
	}
	if err := tencentEnvelopeError(op.Name, payload.Response.Error); err != nil {
		return provider.Page{}, err
	}
	for _, d := range payload.Response.InstanceSet {
		if d.ID == id {
			return oneDeepDetailPage(a.tencentRedisRow(d, region, account)), nil
		}
	}
	return provider.Page{Requests: 1}, deepNotFound(op.Name, "Redis instance")
}
func (a *tencentAdapter) tencentRedisRow(d tencentRedisDetail, region, account string) map[string]any {
	row := newDeepDetailRow(a.Provider(), a.name, d.ID, d.Name, "redis", "QCS::Redis::Instance", "database", "cache", region, account)
	if d.Status != nil {
		states := map[int]string{0: "initializing", 1: "processing", 2: "running", -2: "isolated", -3: "pending_deletion"}
		state, ok := states[*d.Status]
		if !ok {
			state = "unknown"
		}
		row["state"] = state
		row["native"].(map[string]any)["status_code"] = *d.Status
	}
	attrs := row["attributes"].(map[string]any)
	attrs["engine"], attrs["engine_version"] = strings.ToLower(d.Engine), d.Version
	attrs["vpc_id"], attrs["subnet_id"] = d.VPC, d.Subnet
	if d.Size != nil {
		attrs["memory_mb"] = *d.Size
	}
	if d.Shards != nil {
		attrs["shard_count"] = *d.Shards
	}
	if d.Replicas != nil {
		attrs["replica_count"] = *d.Replicas
	}
	if d.Project != nil {
		attrs["project_id"] = *d.Project
	}
	if d.Billing != nil {
		attrs["billing_mode"] = *d.Billing
	}
	tags := map[string]any{}
	for _, tag := range d.Tags {
		if tag.Key != "" {
			tags[tag.Key] = tag.Value
		}
	}
	row["tags"] = tags
	return row
}
