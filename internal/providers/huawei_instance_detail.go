package providers

import (
	"context"
	"strconv"
	"strings"
	"time"

	ecs "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/ecs/v2"
	ecsmodel "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/ecs/v2/model"
	ecsregion "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/ecs/v2/region"

	"mcpcloud/internal/provider"
)

type huaweiECSDetailAPI interface {
	ShowServer(*ecsmodel.ShowServerRequest) (*ecsmodel.ShowServerResponse, error)
}

func (a *huaweiAdapter) showServerDetail(ctx context.Context, request provider.NativeRequest, spec instanceDetailSpec) (provider.Page, error) {
	if err := validateInstanceDetailRequest(spec, request); err != nil {
		return provider.Page{}, err
	}
	projectRaw, err := nativeIdentifier(request.Params, "project_id")
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_parameter", Operation: spec.operation, Message: err.Error()}
	}
	project, err := exactDetailScope(a.profile.Scopes.Projects, projectRaw, spec.operation, "project_id")
	if err != nil {
		return provider.Page{}, err
	}
	serverID, err := nativeIdentifier(request.Params, "server_id")
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_parameter", Operation: spec.operation, Message: err.Error()}
	}
	regionID, err := exactDetailRegion(request.Region, a.profile.Regions, spec.operation)
	if err != nil {
		return provider.Page{}, err
	}
	region, err := ecsregion.SafeValueOf(regionID)
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_region", Operation: spec.operation, Message: err.Error()}
	}
	credential, err := a.huaweiBasicCredential(spec.operation, project)
	if err != nil {
		return provider.Page{}, err
	}
	hc, err := ecs.EcsClientBuilder().WithRegion(region).WithCredential(credential).WithHttpConfig(huaweiHTTPConfig(ctx)).SafeBuild()
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "authentication_error", Operation: spec.operation, Message: err.Error()}
	}
	var client huaweiECSDetailAPI = ecs.NewEcsClient(hc)
	response, err := client.ShowServer(&ecsmodel.ShowServerRequest{ServerId: serverID})
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "huawei_api_error", Operation: spec.operation, Message: err.Error(), Retryable: containsAny(strings.ToLower(err.Error()), "throttl", "timeout")}
	}
	if response == nil || response.Server == nil || response.Server.Id == "" {
		return provider.Page{Requests: 1}, &provider.Error{Code: "not_found", Operation: spec.operation, Message: "ECS server was not returned"}
	}
	return provider.Page{Rows: []map[string]any{a.huaweiServerDetailRow(*response.Server, regionID, project)}, Scanned: 1, Requests: 1}, nil
}

func (a *huaweiAdapter) huaweiServerDetailRow(detail ecsmodel.ServerDetail, region, project string) map[string]any {
	row := newInstanceDetailRow(a.Provider(), a.name, detail.Id, detail.Name, "ecs", "SYS.ECS", region, project)
	row["state"] = strings.ToLower(detail.Status)
	setDetailTime(row, "created_at", detail.Created, time.RFC3339, time.RFC3339Nano)
	setDetailTime(row, "updated_at", detail.Updated, time.RFC3339, time.RFC3339Nano)
	privateIPs, publicIPs := []string{}, []string{}
	for _, addresses := range detail.Addresses {
		for _, address := range addresses {
			if address.OSEXTIPStype != nil && address.OSEXTIPStype.Value() == "floating" {
				publicIPs = appendUniqueText(publicIPs, address.Addr)
			} else {
				privateIPs = appendUniqueText(privateIPs, address.Addr)
			}
		}
	}
	securityGroups := make([]string, 0, len(detail.SecurityGroups))
	for _, group := range detail.SecurityGroups {
		securityGroups = appendUniqueText(securityGroups, group.Id)
	}
	instanceType, imageID, cpuCount, memoryMB := "", "", 0, 0
	if detail.Flavor != nil {
		instanceType = detail.Flavor.Id
		cpuCount, _ = strconv.Atoi(detail.Flavor.Vcpus)
		memoryMB, _ = strconv.Atoi(detail.Flavor.Ram)
	}
	if detail.Image != nil {
		imageID = detail.Image.Id
	}
	if imageID == "" {
		imageID = detail.Metadata["metering.image_id"]
	}
	attributes := row["attributes"].(map[string]any)
	attributes["instance_type"] = instanceType
	attributes["cpu_count"] = cpuCount
	attributes["memory_mb"] = memoryMB
	attributes["image_id"] = imageID
	attributes["vpc_id"] = detail.Metadata["vpc_id"]
	attributes["private_ip_addresses"] = privateIPs
	attributes["public_ip_addresses"] = publicIPs
	attributes["security_group_ids"] = securityGroups
	attributes["os_type"] = detail.Metadata["os_type"]
	attributes["deletion_protection"] = detail.Locked
	setRelated(row, "security_group_ids", securityGroups)
	row["native"].(map[string]any)["instance_type"] = instanceType
	return row
}
