package providers

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"mcpcloud/internal/config"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

type instanceDetailSpec struct {
	operation   string
	service     string
	description string
	parameters  map[string]any
	required    []string
}

var instanceDetailCatalog = map[model.Provider]instanceDetailSpec{
	model.ProviderAWS: {
		operation: "aws.ec2.describe_instance", service: "ec2",
		description: "Get one EC2 instance through DescribeInstances",
		parameters:  detailParameters("instance_id"), required: []string{"instance_id"},
	},
	model.ProviderGCP: {
		operation: "gcp.compute.instances.get", service: "compute",
		description: "Get one Compute Engine instance through instances.get",
		parameters:  detailParameters("project_id", "zone", "instance"), required: []string{"project_id", "zone", "instance"},
	},
	model.ProviderAzure: {
		operation: "azure.compute.virtual_machines.get", service: "compute",
		description: "Get one Azure virtual machine through the Compute resource API",
		parameters:  detailParameters("subscription_id", "resource_group", "name"), required: []string{"subscription_id", "resource_group", "name"},
	},
	model.ProviderAlibaba: {
		operation: "alibaba.ecs.describe_instance_attribute", service: "ecs",
		description: "Get one Alibaba Cloud ECS instance through DescribeInstanceAttribute",
		parameters:  detailParameters("instance_id"), required: []string{"instance_id"},
	},
	model.ProviderHuawei: {
		operation: "huawei.ecs.show_server", service: "ecs",
		description: "Get one Huawei Cloud ECS server through ShowServer",
		parameters:  detailParameters("project_id", "server_id"), required: []string{"project_id", "server_id"},
	},
	model.ProviderTencent: {
		operation: "tencent.cvm.describe_instances", service: "cvm",
		description: "Get one Tencent Cloud CVM instance through DescribeInstances",
		parameters:  detailParameters("instance_id"), required: []string{"instance_id"},
	},
	model.ProviderVolcengine: {
		operation: "volcengine.ecs.describe_instances", service: "ecs",
		description: "Get one Volcengine ECS instance through DescribeInstances",
		parameters:  detailParameters("instance_id"), required: []string{"instance_id"},
	},
}

func detailParameters(names ...string) map[string]any {
	params := make(map[string]any, len(names))
	for _, name := range names {
		params[name] = map[string]any{"type": "string", "required": true}
	}
	return params
}

func instanceDetailOperations(cloud model.Provider) []provider.Operation {
	spec, ok := instanceDetailCatalog[cloud]
	if !ok {
		return nil
	}
	return []provider.Operation{{
		Name: spec.operation, Provider: cloud, Service: spec.service,
		Description: spec.description + "; returns allow-listed normalized metadata",
		Parameters:  cloneNativeSchema(spec.parameters),
	}}
}

func instanceDetailFor(cloud model.Provider, operation string) (instanceDetailSpec, bool) {
	spec, ok := instanceDetailCatalog[cloud]
	return spec, ok && spec.operation == operation
}

func instanceDetailOperationNames(cloud model.Provider) []string {
	if spec, ok := instanceDetailCatalog[cloud]; ok {
		return []string{spec.operation}
	}
	return nil
}

func validateInstanceDetailRequest(spec instanceDetailSpec, request provider.NativeRequest) error {
	operation := provider.Operation{Name: spec.operation, Parameters: spec.parameters}
	if err := operation.ValidateParams(request.Params); err != nil {
		return err
	}
	if request.PageToken != "" {
		return &provider.Error{Code: "cursor_not_supported", Operation: spec.operation, Message: "instance detail operations do not accept a cursor"}
	}
	for _, name := range spec.required {
		value, err := nativeLiteral(request.Params, name)
		if err != nil {
			return &provider.Error{Code: "invalid_parameter", Operation: spec.operation, Message: err.Error()}
		}
		if strings.TrimSpace(value) == "" {
			return &provider.Error{Code: "missing_parameter", Operation: spec.operation, Message: fmt.Sprintf("parameter %s is required", name)}
		}
	}
	return nil
}

func exactDetailRegion(requested string, configured []string, operation string) (string, error) {
	if requested != "" && requested != "*" {
		if !cloudRegionPattern.MatchString(requested) {
			return "", &provider.Error{Code: "invalid_region", Operation: operation, Message: "an exact valid region is required"}
		}
		if len(configured) > 0 && !stringIn(configured, "*") && !stringIn(configured, requested) {
			return "", &provider.Error{Code: "scope_not_allowed", Operation: operation, Message: "region is outside the profile allowlist"}
		}
		return requested, nil
	}
	exact := make([]string, 0, len(configured))
	for _, region := range configured {
		if region != "" && region != "*" {
			exact = append(exact, region)
		}
	}
	if len(exact) == 1 {
		return exact[0], nil
	}
	return "", &provider.Error{Code: "missing_region", Operation: operation, Message: "an exact region is required when the profile does not configure exactly one region"}
}

func exactDetailScope(configured []string, requested, operation, label string) (string, error) {
	if strings.TrimSpace(requested) == "" {
		return "", &provider.Error{Code: "missing_parameter", Operation: operation, Message: fmt.Sprintf("parameter %s is required", label)}
	}
	if len(configured) == 0 {
		return "", &provider.Error{Code: "capability_unavailable", Operation: operation, Message: fmt.Sprintf("resource detail requires a configured %s scope", label)}
	}
	wanted := scopeTail(requested)
	for _, candidate := range configured {
		if candidate == requested || scopeTail(candidate) == wanted {
			return wanted, nil
		}
	}
	return "", &provider.Error{Code: "scope_not_allowed", Operation: operation, Message: fmt.Sprintf("%s is outside the profile allowlist", label)}
}

func singleDetailAccount(profile config.Profile, operation string) (string, error) {
	if len(profile.Scopes.Accounts) != 1 {
		return "", &provider.Error{Code: "capability_unavailable", Operation: operation, Message: "resource detail requires exactly one configured account scope"}
	}
	return profile.Scopes.Accounts[0], nil
}

func newInstanceDetailRow(cloud model.Provider, profile, id, name, service, nativeType, region, scopeID string) map[string]any {
	row := baseRow(cloud, profile, id, name, service, nativeType, region, scopeID, time.Now().UTC())
	row["domain"] = "compute"
	row["kind"] = "instance"
	return row
}

func setDetailTime(row map[string]any, key, value string, layouts ...string) {
	if value == "" {
		return
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			row[key] = parsed.UTC().Format(time.RFC3339Nano)
			return
		}
	}
}

func appendUniqueText(values []string, value string) []string {
	if value == "" || stringIn(values, value) {
		return values
	}
	return append(values, value)
}

func attributeInstanceDetailError(err error, operation string) error {
	if err == nil {
		return nil
	}
	var providerErr *provider.Error
	if errors.As(err, &providerErr) {
		copy := *providerErr
		copy.Operation = operation
		return &copy
	}
	return &provider.Error{Code: "provider_api_error", Operation: operation, Message: err.Error()}
}
