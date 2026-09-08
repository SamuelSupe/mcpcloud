package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	alics "github.com/aliyun/alibaba-cloud-sdk-go/services/cs"
	aliecs "github.com/aliyun/alibaba-cloud-sdk-go/services/ecs"
	alirds "github.com/aliyun/alibaba-cloud-sdk-go/services/rds"
	awsbase "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	ccemodel "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/cce/v3/model"
	ecsmodel "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/ecs/v2/model"
	rdsmodel "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/rds/v3/model"
	tencent "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	tchttp "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/http"
	volctos "github.com/volcengine/ve-tos-golang-sdk/v2/tos"
	volctosenum "github.com/volcengine/ve-tos-golang-sdk/v2/tos/enum"
	volcecs "github.com/volcengine/volcengine-go-sdk/service/ecs"
	volcrds "github.com/volcengine/volcengine-go-sdk/service/rdsmysqlv2"
	volcredis "github.com/volcengine/volcengine-go-sdk/service/redis"
	volcresourcecenter "github.com/volcengine/volcengine-go-sdk/service/resourcecenter"
	volcvke "github.com/volcengine/volcengine-go-sdk/service/vke"
	volc "github.com/volcengine/volcengine-go-sdk/volcengine"
	volcrequest "github.com/volcengine/volcengine-go-sdk/volcengine/request"
	volcsession "github.com/volcengine/volcengine-go-sdk/volcengine/session"

	"mcpcloud/internal/config"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

func TestAWSNativeReadRejectsUnregisteredOperationsBeforeCredentialUse(t *testing.T) {
	adapter := &awsAdapter{name: "aws-prod", profile: config.Profile{Provider: model.ProviderAWS}}
	for _, operation := range []string{"aws.ec2.delete_instances", "https://169.254.169.254/latest/meta-data", "ListResources"} {
		_, err := adapter.NativeRead(context.Background(), provider.NativeRequest{Operation: operation})
		if err == nil || !strings.Contains(err.Error(), "operation is not registered") {
			t.Errorf("NativeRead(%q) error = %v, want operation allowlist rejection", operation, err)
		}
	}
}

func TestAWSNativeReadAllowsOnlyRegisteredOperationAndStopsAtCredentialBoundary(t *testing.T) {
	t.Setenv("MCPCLOUD_TEST_AWS_ACCESS", "")
	t.Setenv("MCPCLOUD_TEST_AWS_SECRET", "")
	adapter := &awsAdapter{
		name: "aws-prod",
		profile: config.Profile{
			Provider: model.ProviderAWS,
			Credential: config.Credential{
				Source: "env",
				Env: map[string]string{
					"AWS_ACCESS_KEY_ID":     "MCPCLOUD_TEST_AWS_ACCESS",
					"AWS_SECRET_ACCESS_KEY": "MCPCLOUD_TEST_AWS_SECRET",
				},
			},
		},
	}
	_, err := adapter.NativeRead(context.Background(), provider.NativeRequest{Operation: awsInventoryOperation})
	if err == nil || !strings.Contains(err.Error(), "AWS credential environment variables are not set") {
		t.Fatalf("registered NativeRead() error = %v, want credential readiness error before API access", err)
	}
	var providerErr *provider.Error
	if !asProviderError(err, &providerErr) || providerErr.Code != "missing_credentials" {
		t.Fatalf("registered NativeRead() error = %#v, want provider.Error{Code: missing_credentials}", err)
	}
}

func TestAWSMetadataExposesOnlyReadOnlyInventoryOperation(t *testing.T) {
	adapter := &awsAdapter{name: "aws-prod", profile: config.Profile{Provider: model.ProviderAWS}}
	operations := adapter.Operations()
	if len(operations) != len(nativeProductCatalog)+4 || operations[0].Name != awsInventoryOperation {
		t.Fatalf("Operations() = %#v, want inventory plus %d product operations", operations, len(nativeProductCatalog))
	}
	assertNativeProductNames(t, adapter, operations)
	for _, capability := range adapter.Capabilities() {
		for _, operation := range capability.Operations {
			if strings.Contains(strings.ToLower(operation), "delete") || strings.Contains(strings.ToLower(operation), "write") {
				t.Fatalf("capability exposes a write-like operation %q", operation)
			}
		}
	}
}

func TestGCPNativeReadRejectsUnregisteredOperationsBeforeCredentialUse(t *testing.T) {
	adapter := &gcpAdapter{
		name: "gcp-prod",
		profile: config.Profile{
			Provider: model.ProviderGCP,
			Options:  map[string]string{"asset_scope": "projects/example"},
		},
	}
	for _, operation := range []string{"gcp.compute.delete_instance", "https://metadata.google.internal/computeMetadata/v1", "SearchAllResources"} {
		_, err := adapter.NativeRead(context.Background(), provider.NativeRequest{Operation: operation})
		if err == nil || !strings.Contains(err.Error(), "operation is not registered") {
			t.Errorf("GCP NativeRead(%q) error = %v, want operation allowlist rejection", operation, err)
		}
	}
}

func TestGCPMetadataRegistersSeparateReadOnlyResourceAndIAMOperations(t *testing.T) {
	adapter := &gcpAdapter{name: "gcp-prod", profile: config.Profile{Provider: model.ProviderGCP}}
	operations := adapter.Operations()
	if len(operations) != len(nativeProductCatalog)+5 {
		t.Fatalf("GCP Operations() = %#v, want resource/IAM plus %d product operations", operations, len(nativeProductCatalog))
	}
	got := map[string]bool{}
	for _, operation := range operations {
		got[operation.Name] = true
		if strings.Contains(strings.ToLower(operation.Name), "delete") || strings.Contains(strings.ToLower(operation.Name), "write") {
			t.Fatalf("GCP metadata exposes a write-like operation %q", operation.Name)
		}
	}
	if !got[gcpResourcesOperation] || !got[gcpIAMOperation] {
		t.Fatalf("GCP operations = %#v, want %q and %q", got, gcpResourcesOperation, gcpIAMOperation)
	}
	assertNativeProductNames(t, adapter, operations)
}

func TestProviderOperationSchemasKeepTypedReadOnlyBoundaries(t *testing.T) {
	awsOperations := (&awsAdapter{name: "aws-prod", profile: config.Profile{Provider: model.ProviderAWS}}).Operations()
	awsOperation := requireOperation(t, awsOperations, awsInventoryOperation)
	if err := awsOperation.ValidateParams(map[string]any{"service": "ec2", "resource_type": "instance"}); err != nil {
		t.Fatalf("AWS typed parameters rejected: %v", err)
	}
	assertOperationRejectsParams(t, awsOperation, "query", "view_arn", "action", "url")

	gcpOperations := (&gcpAdapter{name: "gcp-prod", profile: config.Profile{Provider: model.ProviderGCP}}).Operations()
	for _, operationName := range []string{gcpResourcesOperation, gcpIAMOperation} {
		operation := requireOperation(t, gcpOperations, operationName)
		if err := operation.ValidateParams(map[string]any{"scope": "projects/example"}); err != nil {
			t.Fatalf("GCP %s scope parameter rejected: %v", operation.Name, err)
		}
		assertOperationRejectsParams(t, operation, "query")
	}

	azureOperations := (&azureAdapter{name: "azure-prod", profile: config.Profile{Provider: model.ProviderAzure}}).Operations()
	azureOperation := requireOperation(t, azureOperations, azureResourcesOperation)
	allowedAzure := map[string]any{
		"subscriptions":  []string{"sub-1"},
		"resource_type":  "Microsoft.Compute/virtualMachines",
		"resource_group": "rg-1",
		"name":           "vm-1",
		"location":       "eastasia",
	}
	if err := azureOperation.ValidateParams(allowedAzure); err != nil {
		t.Fatalf("Azure typed parameters rejected: %v", err)
	}
	assertOperationRejectsParams(t, azureOperation, "query", "raw_query", "url", "action", "view_arn")
}

func assertOperationRejectsParams(t *testing.T, operation provider.Operation, names ...string) {
	t.Helper()
	for _, name := range names {
		if err := operation.ValidateParams(map[string]any{name: "removed"}); err == nil {
			t.Errorf("%s schema accepted removed parameter %q", operation.Name, name)
		}
	}
}

func TestAzureNativeReadRejectsUnregisteredOperationsBeforeCredentialUse(t *testing.T) {
	adapter := &azureAdapter{name: "azure-prod", profile: config.Profile{Provider: model.ProviderAzure}}
	for _, operation := range []string{"azure.resourcegraph.delete", "https://management.azure.com/", "Resources"} {
		_, err := adapter.NativeRead(context.Background(), provider.NativeRequest{Operation: operation})
		if err == nil || !strings.Contains(err.Error(), "operation is not registered") {
			t.Errorf("Azure NativeRead(%q) error = %v, want operation allowlist rejection", operation, err)
		}
	}
}

func TestCommonResourceNormalizationKeepsStableFieldsAndClassifiesKinds(t *testing.T) {
	row := baseRow(model.ProviderAWS, "aws-prod", "arn:aws:ec2:region:account:instance/i-1", "web-1", "ec2", "AWS::EC2::Instance", "ap-southeast-1", "123456789012", stableObservedTime)
	if row["provider"] != "aws" || row["profile"] != "aws-prod" || row["domain"] != "compute" || row["kind"] != "instance" {
		t.Fatalf("baseRow() = %#v, want canonical compute instance fields", row)
	}
	scope, ok := row["scope"].(map[string]any)
	if !ok || scope["account_id"] != "123456789012" {
		t.Fatalf("baseRow() scope = %#v, want account id", row["scope"])
	}
	if row["native"].(map[string]any)["resource_type"] != "AWS::EC2::Instance" {
		t.Fatalf("baseRow() native = %#v, want stable resource type", row["native"])
	}
}

func TestCommonResourceNormalizationClassifiesBlockStorageBeforeGenericStorage(t *testing.T) {
	volume := baseRow(model.ProviderVolcengine, "volcengine-prod", "vol-1", "data", "storageebs", "Volcengine::StorageEBS::Volume", "cn-shanghai", "2118159236", stableObservedTime)
	if volume["domain"] != "compute" || volume["kind"] != "disk" {
		t.Fatalf("EBS volume classification = (%v, %v), want (compute, disk)", volume["domain"], volume["kind"])
	}

	bucket := baseRow(model.ProviderVolcengine, "volcengine-prod", "bucket-1", "objects", "tos", "Volcengine::TOS::Bucket", "cn-shanghai", "2118159236", stableObservedTime)
	if bucket["domain"] != "storage" || bucket["kind"] != "bucket" {
		t.Fatalf("TOS bucket classification = (%v, %v), want (storage, bucket)", bucket["domain"], bucket["kind"])
	}
}

func TestVolcengineResourceNormalizationRejectsLookalikeTypes(t *testing.T) {
	tests := []struct {
		name       string
		service    string
		nativeType string
		domain     string
		kind       string
	}{
		{name: "ecs instance", service: "ecs", nativeType: "Volcengine::ECS::Instance", domain: "compute", kind: "instance"},
		{name: "ecs invocation", service: "ecs", nativeType: "Volcengine::ECS::Invocation", domain: "other", kind: "resource"},
		{name: "file nas instance", service: "FileNAS", nativeType: "Volcengine::FileNAS::Instance", domain: "other", kind: "resource"},
		{name: "ebs volume", service: "storage_ebs", nativeType: "Volcengine::StorageEBS::Volume", domain: "compute", kind: "disk"},
		{name: "ebs snapshot policy", service: "storage_ebs", nativeType: "Volcengine::StorageEBS::SnapshotPolicy", domain: "other", kind: "resource"},
		{name: "short ebs snapshot", service: "storage_ebs", nativeType: "snapshot", domain: "other", kind: "resource"},
		{name: "tos bucket", service: "tos", nativeType: "Volcengine::TOS::Bucket", domain: "storage", kind: "bucket"},
		{name: "autoscaling hook", service: "auto_scaling", nativeType: "Volcengine::AutoScaling::ScalingLifecycleHook", domain: "other", kind: "resource"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := baseRow(model.ProviderVolcengine, "volcengine-prod", "id", "name", tt.service, tt.nativeType, "cn-shanghai", "2118159236", stableObservedTime)
			if row["domain"] != tt.domain || row["kind"] != tt.kind {
				t.Fatalf("classification = (%v, %v), want (%s, %s)", row["domain"], row["kind"], tt.domain, tt.kind)
			}
		})
	}
}

func TestSevenCloudCapabilityStatusesReflectBillingConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		adapter provider.Adapter
		want    map[model.Source]string
	}{
		{
			name:    "aws",
			adapter: &awsAdapter{name: "aws-prod", profile: config.Profile{}},
			want:    map[model.Source]string{model.SourceResources: "inventory", model.SourceIAM: "inventory", model.SourceMetrics: "available", model.SourceCosts: "available"},
		},
		{
			name:    "gcp without billing table",
			adapter: &gcpAdapter{name: "gcp-prod", profile: config.Profile{}},
			want:    map[model.Source]string{model.SourceResources: "inventory", model.SourceIAM: "available", model.SourceMetrics: "not_configured", model.SourceCosts: "not_configured"},
		},
		{
			name:    "azure",
			adapter: &azureAdapter{name: "azure-prod", profile: config.Profile{}},
			want:    map[model.Source]string{model.SourceResources: "inventory", model.SourceIAM: "available", model.SourceMetrics: "not_configured", model.SourceCosts: "available"},
		},
		{
			name:    "alibaba",
			adapter: &alibabaAdapter{name: "alibaba-prod", profile: config.Profile{}},
			want:    map[model.Source]string{model.SourceResources: "inventory", model.SourceIAM: "inventory", model.SourceMetrics: "not_configured", model.SourceCosts: "available"},
		},
		{
			name:    "huawei",
			adapter: &huaweiAdapter{name: "huawei-prod", profile: config.Profile{}},
			want:    map[model.Source]string{model.SourceResources: "inventory", model.SourceIAM: "inventory", model.SourceMetrics: "not_configured", model.SourceCosts: "not_configured"},
		},
		{
			name:    "tencent without billing currency",
			adapter: &tencentAdapter{name: "tencent-prod", profile: config.Profile{}},
			want:    map[model.Source]string{model.SourceResources: "inventory", model.SourceIAM: "inventory", model.SourceMetrics: "not_configured", model.SourceCosts: "not_configured"},
		},
		{
			name:    "volcengine",
			adapter: &volcengineAdapter{name: "volcengine-prod", profile: config.Profile{}},
			want:    map[model.Source]string{model.SourceResources: "inventory", model.SourceIAM: "inventory", model.SourceMetrics: "not_configured", model.SourceCosts: "available"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := map[model.Source]string{}
			for _, capability := range tt.adapter.Capabilities() {
				if capability.Provider != tt.adapter.Provider() {
					t.Errorf("capability provider = %q, want %q", capability.Provider, tt.adapter.Provider())
				}
				if _, exists := got[capability.Source]; exists {
					t.Fatalf("duplicate capability source %q", capability.Source)
				}
				got[capability.Source] = capability.Status
			}
			if len(got) != len(tt.want) {
				t.Fatalf("capability sources = %#v, want %#v", got, tt.want)
			}
			for source, wantStatus := range tt.want {
				if got[source] != wantStatus {
					t.Errorf("%s capability status = %q, want %q", source, got[source], wantStatus)
				}
			}
		})
	}
}

func TestConfiguredCostCapabilitiesBecomeAvailable(t *testing.T) {
	tests := []struct {
		name    string
		adapter provider.Adapter
	}{
		{
			name: "gcp billing export table",
			adapter: &gcpAdapter{
				name: "gcp-prod",
				profile: config.Profile{Options: map[string]string{
					"billing_table": "billing-project.billing_dataset.billing_export",
				}},
			},
		},
		{
			name: "tencent billing currency",
			adapter: &tencentAdapter{
				name:    "tencent-prod",
				profile: config.Profile{Options: map[string]string{"billing_currency": "USD"}, Scopes: config.Scopes{Accounts: []string{"1000000001"}}},
			},
		},
		{
			name: "huawei billing site",
			adapter: &huaweiAdapter{
				name:    "huawei-prod",
				profile: config.Profile{Options: map[string]string{"billing_site": "china"}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			found := false
			for _, capability := range tt.adapter.Capabilities() {
				if capability.Source == model.SourceCosts {
					found = true
					if capability.Status != "available" {
						t.Fatalf("cost capability = %#v, want available", capability)
					}
				}
			}
			if !found {
				t.Fatal("adapter omitted the costs capability")
			}
		})
	}
}

func TestConfiguredMetricCapabilitiesBecomeAvailableForScopedProviders(t *testing.T) {
	tests := []struct {
		name    string
		adapter provider.Adapter
	}{
		{
			name: "gcp project",
			adapter: &gcpAdapter{
				name:    "gcp-prod",
				profile: config.Profile{Scopes: config.Scopes{Projects: []string{"project-a"}}},
			},
		},
		{
			name: "azure exact region",
			adapter: &azureAdapter{
				name:    "azure-prod",
				profile: config.Profile{Regions: []string{"eastasia"}},
			},
		},
		{
			name: "alibaba account",
			adapter: &alibabaAdapter{
				name:    "alibaba-prod",
				profile: config.Profile{Scopes: config.Scopes{Accounts: []string{"123456789012"}}},
			},
		},
		{
			name: "huawei project",
			adapter: &huaweiAdapter{
				name:    "huawei-prod",
				profile: config.Profile{Scopes: config.Scopes{Projects: []string{"project-a"}}},
			},
		},
		{
			name: "tencent account and region",
			adapter: &tencentAdapter{
				name:    "tencent-prod",
				profile: config.Profile{Scopes: config.Scopes{Accounts: []string{"1000000001"}}, Regions: []string{"ap-singapore"}},
			},
		},
		{
			name: "volcengine account",
			adapter: &volcengineAdapter{
				name:    "volcengine-prod",
				profile: config.Profile{Scopes: config.Scopes{Accounts: []string{"2000000001"}}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, capability := range tt.adapter.Capabilities() {
				if capability.Source == model.SourceMetrics && capability.Status != "available" {
					t.Fatalf("metrics capability = %#v, want available", capability)
				}
			}
		})
	}
}

func TestMetricQueriesFailClosedWithoutAUniqueProviderScope(t *testing.T) {
	start := stableObservedTime.Add(-time.Hour)
	end := stableObservedTime
	request := func(metric string, accounts ...string) provider.QueryRequest {
		return provider.QueryRequest{
			Source:   model.SourceMetrics,
			Accounts: accounts,
			Region:   "ap-singapore",
			Metrics:  []string{metric},
			Start:    &start,
			End:      &end,
		}
	}
	tests := []struct {
		name    string
		adapter provider.Adapter
		request provider.QueryRequest
		code    string
	}{
		{
			name:    "alibaba no account",
			adapter: &alibabaAdapter{name: "alibaba-prod", profile: config.Profile{}},
			request: request("acs.ecs::CPUUtilization?instanceId=i-1"),
			code:    "capability_unavailable",
		},
		{
			name: "alibaba multiple accounts",
			adapter: &alibabaAdapter{
				name:    "alibaba-prod",
				profile: config.Profile{Scopes: config.Scopes{Accounts: []string{"123456789012", "123456789013"}}},
			},
			request: request("acs.ecs::CPUUtilization?instanceId=i-1"),
			code:    "capability_unavailable",
		},
		{
			name:    "huawei no project",
			adapter: &huaweiAdapter{name: "huawei-prod", profile: config.Profile{}},
			request: request("SYS.ECS::cpu_util?resource_id=i-1"),
			code:    "capability_unavailable",
		},
		{
			name: "huawei multiple projects",
			adapter: &huaweiAdapter{
				name:    "huawei-prod",
				profile: config.Profile{Scopes: config.Scopes{Projects: []string{"project-a", "project-b"}}},
			},
			request: request("SYS.ECS::cpu_util?resource_id=i-1"),
			code:    "invalid_scope",
		},
		{
			name:    "tencent no account",
			adapter: &tencentAdapter{name: "tencent-prod", profile: config.Profile{}},
			request: request("QCE/CVM::CPUUsage?InstanceId=i-1"),
			code:    "capability_unavailable",
		},
		{
			name: "tencent multiple accounts",
			adapter: &tencentAdapter{
				name:    "tencent-prod",
				profile: config.Profile{Scopes: config.Scopes{Accounts: []string{"1000000001", "1000000002"}}},
			},
			request: request("QCE/CVM::CPUUsage?InstanceId=i-1"),
			code:    "capability_unavailable",
		},
		{
			name: "tencent wildcard region",
			adapter: &tencentAdapter{
				name:    "tencent-prod",
				profile: config.Profile{Scopes: config.Scopes{Accounts: []string{"1000000001"}}, Regions: []string{"*"}},
			},
			request: func() provider.QueryRequest {
				req := request("QCE/CVM::CPUUsage?InstanceId=i-1")
				req.Region = "*"
				return req
			}(),
			code: "invalid_region",
		},
		{
			name:    "volcengine no account",
			adapter: &volcengineAdapter{name: "volcengine-prod", profile: config.Profile{}},
			request: request("VCM::ECS::CPUUtilization?resource_id=i-1"),
			code:    "capability_unavailable",
		},
		{
			name: "volcengine multiple accounts",
			adapter: &volcengineAdapter{
				name:    "volcengine-prod",
				profile: config.Profile{Scopes: config.Scopes{Accounts: []string{"2000000001", "2000000002"}}},
			},
			request: request("VCM::ECS::CPUUtilization?resource_id=i-1"),
			code:    "capability_unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.adapter.Query(context.Background(), tt.request)
			var providerErr *provider.Error
			if !asProviderError(err, &providerErr) || providerErr.Code != tt.code {
				t.Fatalf("metrics query error = %#v, want provider.Error{Code: %s}", err, tt.code)
			}
		})
	}
}

func TestHuaweiMetricCredentialCarriesSelectedProject(t *testing.T) {
	t.Setenv("HUAWEICLOUD_SDK_AK", "test-ak")
	t.Setenv("HUAWEICLOUD_SDK_SK", "test-sk")
	adapter := &huaweiAdapter{name: "huawei-prod", profile: config.Profile{Credential: config.Credential{Source: "env"}}}
	credential, err := adapter.huaweiBasicCredential(huaweiMetricsOperation, "project-a")
	if err != nil {
		t.Fatalf("huaweiBasicCredential() error = %v", err)
	}
	if credential == nil || credential.ProjectId != "project-a" {
		t.Fatalf("Huawei metric credential project = %#v, want project-a", credential)
	}
}

func TestHuaweiMetricCredentialRequiresCredentials(t *testing.T) {
	t.Setenv("HUAWEICLOUD_SDK_AK", "")
	t.Setenv("HUAWEICLOUD_SDK_SK", "")
	adapter := &huaweiAdapter{name: "huawei-prod", profile: config.Profile{Credential: config.Credential{Source: "env"}}}
	_, err := adapter.huaweiBasicCredential(huaweiMetricsOperation, "project-a")
	var providerErr *provider.Error
	if !asProviderError(err, &providerErr) || providerErr.Code != "missing_credentials" {
		t.Fatalf("huaweiBasicCredential() error = %#v, want provider.Error{Code: missing_credentials}", err)
	}
}

func TestHuaweiCostsFailClosedWithoutBillingSite(t *testing.T) {
	start := stableObservedTime.Add(-time.Hour)
	end := stableObservedTime
	adapter := &huaweiAdapter{name: "huawei-prod", profile: config.Profile{}}
	_, err := adapter.Query(context.Background(), provider.QueryRequest{
		Source: model.SourceCosts,
		Start:  &start,
		End:    &end,
	})
	if err == nil {
		t.Fatal("Huawei costs query succeeded without options.billing_site")
	}
	var providerErr *provider.Error
	if !asProviderError(err, &providerErr) || providerErr.Code != "capability_unavailable" {
		t.Fatalf("Huawei costs error = %#v, want provider.Error{Code: capability_unavailable}", err)
	}
}

func TestSevenCloudReadinessStatusesDoNotExposeCredentialValues(t *testing.T) {
	const secret = "provider-test-secret-value"

	tests := []struct {
		name    string
		adapter provider.Adapter
		env     map[string]string
	}{
		{
			name:    "aws",
			adapter: &awsAdapter{name: "aws-prod", profile: envCredentialProfile(map[string]string{"AWS_ACCESS_KEY_ID": "MCP_TEST_AWS_ACCESS", "AWS_SECRET_ACCESS_KEY": "MCP_TEST_AWS_SECRET"})},
			env:     map[string]string{"MCP_TEST_AWS_ACCESS": secret, "MCP_TEST_AWS_SECRET": secret},
		},
		{
			name:    "gcp",
			adapter: &gcpAdapter{name: "gcp-prod", profile: envCredentialProfile(map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": "MCP_TEST_GCP_CREDENTIALS"})},
			env:     map[string]string{"MCP_TEST_GCP_CREDENTIALS": secret},
		},
		{
			name:    "azure",
			adapter: &azureAdapter{name: "azure-prod", profile: envCredentialProfile(map[string]string{"AZURE_TENANT_ID": "MCP_TEST_AZURE_TENANT", "AZURE_CLIENT_ID": "MCP_TEST_AZURE_CLIENT", "AZURE_CLIENT_SECRET": "MCP_TEST_AZURE_SECRET"})},
			env:     map[string]string{"MCP_TEST_AZURE_TENANT": secret, "MCP_TEST_AZURE_CLIENT": secret, "MCP_TEST_AZURE_SECRET": secret},
		},
		{
			name:    "alibaba",
			adapter: &alibabaAdapter{name: "alibaba-prod", profile: envCredentialProfile(map[string]string{"ALIBABA_CLOUD_ACCESS_KEY_ID": "MCP_TEST_ALIBABA_ACCESS", "ALIBABA_CLOUD_ACCESS_KEY_SECRET": "MCP_TEST_ALIBABA_SECRET"})},
			env:     map[string]string{"MCP_TEST_ALIBABA_ACCESS": secret, "MCP_TEST_ALIBABA_SECRET": secret},
		},
		{
			name:    "huawei",
			adapter: &huaweiAdapter{name: "huawei-prod", profile: envCredentialProfile(map[string]string{"HUAWEICLOUD_SDK_AK": "MCP_TEST_HUAWEI_AK", "HUAWEICLOUD_SDK_SK": "MCP_TEST_HUAWEI_SK"})},
			env:     map[string]string{"MCP_TEST_HUAWEI_AK": secret, "MCP_TEST_HUAWEI_SK": secret},
		},
		{
			name:    "tencent",
			adapter: &tencentAdapter{name: "tencent-prod", profile: envCredentialProfile(map[string]string{"TENCENTCLOUD_SECRET_ID": "MCP_TEST_TENCENT_ID", "TENCENTCLOUD_SECRET_KEY": "MCP_TEST_TENCENT_KEY"})},
			env:     map[string]string{"MCP_TEST_TENCENT_ID": secret, "MCP_TEST_TENCENT_KEY": secret},
		},
		{
			name:    "volcengine",
			adapter: &volcengineAdapter{name: "volcengine-prod", profile: envCredentialProfile(map[string]string{"VOLCENGINE_ACCESS_KEY_ID": "MCP_TEST_VOLC_ACCESS", "VOLCENGINE_SECRET_ACCESS_KEY": "MCP_TEST_VOLC_SECRET"})},
			env:     map[string]string{"MCP_TEST_VOLC_ACCESS": secret, "MCP_TEST_VOLC_SECRET": secret},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for name, value := range tt.env {
				t.Setenv(name, value)
			}
			status := tt.adapter.Readiness(context.Background())
			if !status.Ready || status.Status != "configured_unverified" || status.Error != "" {
				t.Fatalf("Readiness() = %#v, want configured_unverified without an error", status)
			}
			encoded, err := json.Marshal(status)
			if err != nil {
				t.Fatalf("json.Marshal(Readiness()) error = %v", err)
			}
			if strings.Contains(string(encoded), secret) {
				t.Fatalf("Readiness() JSON contains credential value: %s", encoded)
			}
		})
	}
}

func TestProviderCursorsRejectInvalidBase64OversizedAndMalformedTokens(t *testing.T) {
	rawToken := func(value string) string {
		return base64.RawURLEncoding.EncodeToString([]byte(value))
	}
	tests := []struct {
		name   string
		decode func(string) error
		token  string
	}{
		{
			name:  "period cursor invalid base64",
			token: "not-base64!",
			decode: func(token string) error {
				_, err := decodePeriodCursor(token)
				return err
			},
		},
		{
			name:  "period cursor oversized payload",
			token: rawToken(strings.Repeat("x", 257)),
			decode: func(token string) error {
				_, err := decodePeriodCursor(token)
				return err
			},
		},
		{
			name:  "period cursor malformed json",
			token: rawToken("{"),
			decode: func(token string) error {
				_, err := decodePeriodCursor(token)
				return err
			},
		},
		{
			name:  "period cursor negative offset",
			token: encodePeriodCursor(periodCursor{Offset: -1}),
			decode: func(token string) error {
				_, err := decodePeriodCursor(token)
				return err
			},
		},
		{
			name:  "bigquery cursor invalid base64",
			token: "not-base64!",
			decode: func(token string) error {
				_, err := decodeBigQueryCursor(token)
				return err
			},
		},
		{
			name:  "bigquery cursor oversized payload",
			token: rawToken(strings.Repeat("x", 4097)),
			decode: func(token string) error {
				_, err := decodeBigQueryCursor(token)
				return err
			},
		},
		{
			name:  "bigquery cursor malformed json",
			token: rawToken("{"),
			decode: func(token string) error {
				_, err := decodeBigQueryCursor(token)
				return err
			},
		},
		{
			name:  "bigquery cursor invalid job id",
			token: encodeBigQueryCursor(bigQueryCursor{JobID: "job/id"}),
			decode: func(token string) error {
				_, err := decodeBigQueryCursor(token)
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.decode(tt.token); err == nil {
				t.Fatalf("decode(%q) succeeded, want rejection", tt.token)
			}
		})
	}
}

func TestMetricSelectorRejectsMalformedAndOversizedInput(t *testing.T) {
	dimensions := make([]string, 17)
	for i := range dimensions {
		dimensions[i] = "d" + string(rune('a'+i)) + "=value"
	}
	tests := []string{
		"",
		" metric",
		strings.Repeat("a", 257),
		"metric?name=value&name=other",
		"metric?bad%zz=value",
		"metric?=value",
		"metric?" + strings.Join(dimensions, "&"),
	}

	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			if _, err := parseMetricSelector(raw, 1, 1); err == nil {
				t.Fatalf("parseMetricSelector(%q) succeeded, want rejection", raw)
			}
		})
	}
}

func TestNativeSelectorsRejectUnsafeIdentifiersAndLiterals(t *testing.T) {
	if got, err := nativeIdentifier(map[string]any{"service": "compute.instances"}, "service"); err != nil || got != "compute.instances" {
		t.Fatalf("nativeIdentifier(valid) = %q, %v; want compute.instances", got, err)
	}
	for _, value := range []string{
		"service name",
		"service\nname",
		"service' OR '1'='1",
		strings.Repeat("x", 257),
	} {
		if _, err := nativeIdentifier(map[string]any{"service": value}, "service"); err == nil {
			t.Errorf("nativeIdentifier(%q) accepted an unsafe selector", value)
		}
	}
	if _, err := nativeIdentifier(map[string]any{"service": 42}, "service"); err == nil {
		t.Fatal("nativeIdentifier() accepted a non-string selector")
	}
	if _, err := nativeLiteral(map[string]any{"name": strings.Repeat("x", 1025)}, "name"); err == nil {
		t.Fatal("nativeLiteral() accepted an oversized literal")
	}
	if _, err := nativeLiteral(map[string]any{"name": "vm\r\nnext"}, "name"); err == nil {
		t.Fatal("nativeLiteral() accepted a control-character literal")
	}
}

func TestAzureCostSkipTokenRejectsUnsafePaginationLinks(t *testing.T) {
	const base = "https://management.azure.com/subscriptions/sub-1/providers/Microsoft.CostManagement/query"
	valid, err := azureCostSkipToken(base+"?$skiptoken=opaque%2Btoken", "sub-1")
	if err != nil || valid != "opaque+token" {
		t.Fatalf("azureCostSkipToken(valid) = %q, %v; want opaque+token", valid, err)
	}

	tests := []struct {
		name string
		link string
	}{
		{name: "wrong scheme", link: strings.Replace(base, "https://", "http://", 1) + "?$skiptoken=x"},
		{name: "wrong host", link: strings.Replace(base, "management.azure.com", "evil.example", 1) + "?$skiptoken=x"},
		{name: "wrong path", link: base + "/other?$skiptoken=x"},
		{name: "userinfo", link: "https://user:pass@management.azure.com/subscriptions/sub-1/providers/Microsoft.CostManagement/query?$skiptoken=x"},
		{name: "missing token", link: base},
		{name: "oversized token", link: base + "?$skiptoken=" + strings.Repeat("x", 4097)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := azureCostSkipToken(tt.link, "sub-1"); err == nil {
				t.Fatalf("azureCostSkipToken(%q) succeeded, want rejection", tt.link)
			}
		})
	}
}

func TestGCPNormalizationAndInjectedRESTHelper(t *testing.T) {
	selector := metricSelector{
		Parts:      []string{"compute.googleapis.com/cpu"},
		Dimensions: map[string]string{"metric.instance_name": `vm"one\blue;Resources`},
	}
	filter, err := gcpMetricFilter(selector)
	if err != nil {
		t.Fatalf("gcpMetricFilter() error = %v", err)
	}
	if !strings.Contains(filter, `metric.labels."instance_name" = "vm\"one\\blue;Resources"`) {
		t.Fatalf("gcpMetricFilter() = %q, want escaped metric dimension", filter)
	}
	if _, err := gcpMetricFilter(metricSelector{Parts: []string{"metric"}, Dimensions: map[string]string{"labels.name": "value"}}); err == nil {
		t.Fatal("gcpMetricFilter() accepted a non metric/resource dimension")
	}
	if got := gcpService("compute.googleapis.com/Instance"); got != "compute" {
		t.Fatalf("gcpService() = %q, want compute", got)
	}
	doubleValue := 3.5
	intValue := "7"
	if got, ok := gcpPointValue(&doubleValue, &intValue); !ok || got != doubleValue {
		t.Fatalf("gcpPointValue(double, int64) = (%v, %t), want (%v, true)", got, ok, doubleValue)
	}
	badIntValue := "not-a-number"
	if _, ok := gcpPointValue(nil, &badIntValue); ok {
		t.Fatal("gcpPointValue() accepted an invalid int64 value")
	}

	var payload struct {
		Value string `json:"value"`
	}
	called := false
	client := &http.Client{Transport: providerTestRoundTripper(func(request *http.Request) (*http.Response, error) {
		called = true
		if request.Method != http.MethodPost || request.Header.Get("Content-Type") != "application/json" {
			t.Errorf("request = %s %s, Content-Type = %q; want POST JSON", request.Method, request.URL, request.Header.Get("Content-Type"))
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"value":"ok"}`)),
			Request:    request,
		}, nil
	})}
	if err := gcpJSONRequest(context.Background(), client, http.MethodPost, "https://example.test/query", []byte(`{"query":"read-only"}`), gcpCostsOperation, &payload); err != nil {
		t.Fatalf("gcpJSONRequest() error = %v", err)
	}
	if !called || payload.Value != "ok" {
		t.Fatalf("gcpJSONRequest() called = %t, payload = %#v; want one decoded response", called, payload)
	}
}

func TestMetricAndCostRowsUseProviderScopeIdentifiers(t *testing.T) {
	tests := []struct {
		name         string
		provider     model.Provider
		metricScope  string
		costScope    string
		metricKey    string
		costKey      string
		metricForbid string
		costForbid   string
	}{
		{name: "gcp project", provider: model.ProviderGCP, metricScope: "project-a", costScope: "project-a", metricKey: "project_id", costKey: "project_id", metricForbid: "account_id", costForbid: "account_id"},
		{name: "azure subscription", provider: model.ProviderAzure, metricScope: "sub-1", costScope: "sub-1", metricKey: "subscription_id", costKey: "subscription_id", metricForbid: "account_id", costForbid: "project_id"},
		{name: "huawei metric project and cost account", provider: model.ProviderHuawei, metricScope: "project-a", costScope: "account-a", metricKey: "project_id", costKey: "account_id", metricForbid: "account_id", costForbid: "project_id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metric := metricRow(tt.provider, "profile", "metric", "monitor", "resource-1", "region-1", tt.metricScope, stableObservedTime, 1, "", nil)
			metricScope, ok := metric["scope"].(map[string]any)
			if !ok {
				t.Fatalf("metricRow() scope = %#v, want map", metric["scope"])
			}
			if got := metricScope[tt.metricKey]; got != tt.metricScope {
				t.Fatalf("metricRow() scope[%q] = %#v, want %q", tt.metricKey, got, tt.metricScope)
			}
			if _, ok := metricScope[tt.metricForbid]; ok {
				t.Fatalf("metricRow() populated forbidden scope key %q: %#v", tt.metricForbid, metricScope)
			}

			cost := costRow(tt.provider, "profile", "cost-1", "2026-08-25", tt.costScope, "service", "region-1", 1, "USD")
			costScope, ok := cost["scope"].(map[string]any)
			if !ok {
				t.Fatalf("costRow() scope = %#v, want map", cost["scope"])
			}
			if got := costScope[tt.costKey]; got != tt.costScope {
				t.Fatalf("costRow() scope[%q] = %#v, want %q", tt.costKey, got, tt.costScope)
			}
			if _, ok := costScope[tt.costForbid]; ok {
				t.Fatalf("costRow() populated forbidden scope key %q: %#v", tt.costForbid, costScope)
			}
		})
	}
}

func TestGCPResourcesPushDownValidatedLocationQuery(t *testing.T) {
	credentialsPath := t.TempDir() + "/gcp-credentials.json"
	credentials := `{"type":"authorized_user","client_id":"test-client","client_secret":"test-secret","refresh_token":"test-refresh"}`
	if err := os.WriteFile(credentialsPath, []byte(credentials), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Setenv("MCPCLOUD_TEST_GCP_CREDENTIALS", credentialsPath)
	oldTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	var assetRequest *http.Request
	http.DefaultTransport = providerTestRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "oauth2.googleapis.com" {
			return providerTestHTTPResponse(request, `{"access_token":"test-token","token_type":"Bearer","expires_in":3600}`), nil
		}
		if request.URL.Host != "cloudasset.googleapis.com" {
			return nil, fmt.Errorf("unexpected GCP request host %q", request.URL.Host)
		}
		assetRequest = request.Clone(request.Context())
		return providerTestHTTPResponse(request, `{"results":[]}`), nil
	})
	adapter := &gcpAdapter{
		name: "gcp-prod",
		profile: config.Profile{
			Credential: config.Credential{Source: "env", Env: map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": "MCPCLOUD_TEST_GCP_CREDENTIALS"}},
			Options:    map[string]string{"asset_scope": "projects/project-a"},
		},
	}
	_, err := adapter.Query(context.Background(), provider.QueryRequest{Source: model.SourceResources, Region: "us-east1"})
	if err != nil {
		t.Fatalf("GCP resources Query() error = %v", err)
	}
	if assetRequest == nil {
		t.Fatal("GCP resources Query() did not send a Cloud Asset request")
	}
	if got, want := assetRequest.URL.Path, "/v1/projects/project-a:searchAllResources"; got != want {
		t.Fatalf("Cloud Asset path = %q, want %q", got, want)
	}
	if got, want := assetRequest.URL.Query().Get("query"), "location:us-east1"; got != want {
		t.Fatalf("Cloud Asset query = %q, want %q", got, want)
	}
	if got := assetRequest.Header.Get("Authorization"); got != "Bearer test-token" {
		t.Fatalf("Cloud Asset Authorization = %q, want bearer token", got)
	}
}

func TestAzureMetricsRequireExactRegion(t *testing.T) {
	start := stableObservedTime.Add(-time.Hour)
	end := stableObservedTime
	baseRequest := provider.QueryRequest{
		Source:   model.SourceMetrics,
		Accounts: []string{"sub-1"},
		Metrics:  []string{"Microsoft.Compute/virtualMachines::Percentage CPU?resourceId=vm-1"},
		Start:    &start,
		End:      &end,
	}
	withoutRegion := &azureAdapter{name: "azure-prod", profile: config.Profile{Scopes: config.Scopes{Subscriptions: []string{"sub-1"}}}}
	if _, err := withoutRegion.Query(context.Background(), baseRequest); !hasProviderErrorCode(err, "invalid_region") {
		t.Fatalf("Azure metrics without region error = %#v, want invalid_region", err)
	}
}

func TestAzureAndTencentNormalizationHelpers(t *testing.T) {
	item := map[string]any{
		"id":             "/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/vm-1",
		"type":           "Microsoft.Compute/virtualMachines",
		"name":           "vm-1",
		"location":       "eastasia",
		"subscriptionId": "sub-1",
		"resourceGroup":  "rg",
		"tags":           map[string]any{"env": "test"},
		"properties":     map[string]any{"provisioningState": "Succeeded"},
	}
	row := (&azureAdapter{name: "azure-prod"}).azureRow(item, stableObservedTime)
	if row["provider"] != string(model.ProviderAzure) || row["service"] != "Microsoft.Compute" || row["kind"] != "instance" || row["state"] != "Succeeded" {
		t.Fatalf("azureRow() = %#v, want canonical Azure compute fields", row)
	}
	scope := row["scope"].(map[string]any)
	if scope["subscription_id"] != "sub-1" || scope["resource_group_id"] != "rg" {
		t.Fatalf("azureRow() scope = %#v, want subscription and resource group", scope)
	}
	if row["tags"].(map[string]any)["env"] != "test" || row["native"].(map[string]any)["type"] != "Microsoft.Compute/virtualMachines" {
		t.Fatalf("azureRow() lost tags/native type: %#v", row)
	}
	if got := azureCostDate("20240102"); got != "2024-01-02" {
		t.Fatalf("azureCostDate() = %q, want 2024-01-02", got)
	}
	if got := azureDuration(90 * time.Second); got != "PT90S" {
		t.Fatalf("azureDuration() = %q, want PT90S", got)
	}
	metricValue := 2.5
	if got, name, ok := azureMetricValue(nil, &metricValue); !ok || got != metricValue || name != "Total" {
		t.Fatalf("azureMetricValue() = (%v, %q, %t), want (2.5, Total, true)", got, name, ok)
	}

	for _, tt := range []struct {
		resourceType string
		want         string
	}{
		{resourceType: "qcs::cvm", want: "cvm"},
		{resourceType: "qcs::cos", want: "cos"},
		{resourceType: "malformed", want: "cloudrc"},
	} {
		if got := tencentService(tt.resourceType); got != tt.want {
			t.Errorf("tencentService(%q) = %q, want %q", tt.resourceType, got, tt.want)
		}
	}
}

func TestReadOperationSchemasRejectRawQueryAndURLLikeParameters(t *testing.T) {
	tests := []struct {
		name       string
		operations []provider.Operation
		wantNames  []string
		allowed    map[string]any
	}{
		{
			name:       "aws resource explorer",
			operations: (&awsAdapter{name: "aws-prod"}).Operations(),
			wantNames:  []string{awsInventoryOperation},
		},
		{
			name:       "gcp cloud asset",
			operations: (&gcpAdapter{name: "gcp-prod"}).Operations(),
			wantNames:  []string{gcpResourcesOperation, gcpIAMOperation},
		},
		{
			name:       "azure resource graph",
			operations: (&azureAdapter{name: "azure-prod"}).Operations(),
			wantNames:  []string{azureResourcesOperation},
			allowed: map[string]any{
				"subscriptions":  []string{"sub-1"},
				"resource_type":  "Microsoft.Compute/virtualMachines",
				"resource_group": "rg-1",
				"name":           "vm-1",
				"location":       "eastasia",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, name := range tt.wantNames {
				operation := requireOperation(t, tt.operations, name)
				for _, forbidden := range []string{"query", "url", "action", "view_arn"} {
					if _, ok := operation.Parameters[forbidden]; ok {
						t.Errorf("%s schema exposes forbidden parameter %q: %#v", operation.Name, forbidden, operation.Parameters)
					}
				}
				if err := operation.ValidateParams(map[string]any{"query": "provider-side query"}); err == nil {
					t.Errorf("%s accepted removed query parameter", operation.Name)
				}
			}
			if len(tt.allowed) > 0 {
				if err := requireOperation(t, tt.operations, tt.wantNames[0]).ValidateParams(tt.allowed); err != nil {
					t.Fatalf("Azure typed parameters rejected: %v", err)
				}
			}
		})
	}
}

func TestNativeProductCatalogCoversAllProvidersAndCapabilityOperations(t *testing.T) {
	globalNames := map[string]bool{}
	for _, adapter := range nativeProductTestAdapters() {
		operations := adapter.Operations()
		assertNativeProductNames(t, adapter, operations)
		for _, operation := range operations {
			if _, ok := nativeProductFor(adapter.Provider(), operation.Name); !ok {
				continue
			}
			if globalNames[operation.Name] {
				t.Fatalf("duplicate product operation name %q across providers", operation.Name)
			}
			globalNames[operation.Name] = true
		}

		capabilitiesBySource := map[model.Source]map[string]bool{}
		for _, capability := range adapter.Capabilities() {
			if capabilitiesBySource[capability.Source] == nil {
				capabilitiesBySource[capability.Source] = map[string]bool{}
			}
			for _, operation := range capability.Operations {
				capabilitiesBySource[capability.Source][operation] = true
			}
		}
		for _, spec := range nativeProductCatalog {
			name := nativeProductOperationName(adapter.Provider(), spec)
			if !capabilitiesBySource[spec.source][name] {
				t.Errorf("%s capability source %s does not expose product operation %q", adapter.Provider(), spec.source, name)
			}
		}
	}
	if want := len(model.Providers) * len(nativeProductCatalog); len(globalNames) != want {
		t.Fatalf("unique native product operations = %d, want %d", len(globalNames), want)
	}
}

func TestNativeProductSchemasAreTypedAndClosed(t *testing.T) {
	forbidden := []string{"query", "url", "action", "view_arn", "resource_type", "service", "type"}
	for _, adapter := range nativeProductTestAdapters() {
		productOperations := make([]provider.Operation, 0, len(nativeProductCatalog))
		for _, operation := range adapter.Operations() {
			if _, ok := nativeProductFor(adapter.Provider(), operation.Name); !ok {
				continue
			}
			productOperations = append(productOperations, operation)
			for _, name := range forbidden {
				if _, ok := operation.Parameters[name]; ok {
					t.Errorf("%s operation %q exposes forbidden parameter %q", adapter.Provider(), operation.Name, name)
				}
			}
		}
		if len(productOperations) != len(nativeProductCatalog) {
			t.Fatalf("%s product operations = %d, want %d", adapter.Provider(), len(productOperations), len(nativeProductCatalog))
		}
		for _, operation := range productOperations {
			for _, name := range forbidden {
				if err := operation.ValidateParams(map[string]any{name: "blocked"}); err == nil {
					t.Errorf("%s schema accepted undeclared parameter %q", operation.Name, name)
				}
			}
		}
		for _, name := range forbidden {
			operation := productOperations[0]
			if _, err := adapter.NativeRead(context.Background(), provider.NativeRequest{Operation: operation.Name, Params: map[string]any{name: "blocked"}}); err == nil {
				t.Errorf("%s direct NativeRead accepted undeclared parameter %q", operation.Name, name)
			}
		}
	}
}

func TestNativeProductRowsFilterTargetKindsAndPreservePageStats(t *testing.T) {
	for _, spec := range nativeProductCatalog {
		t.Run(spec.suffix, func(t *testing.T) {
			allowedKind := spec.kinds[0]
			rows := []map[string]any{{"domain": spec.domain, "kind": allowedKind}}
			if len(spec.kinds) > 1 {
				rows = append(rows, map[string]any{"domain": spec.domain, "kind": spec.kinds[1]})
			}
			rows = append(rows,
				map[string]any{"domain": "other", "kind": allowedKind},
				map[string]any{"domain": spec.domain, "kind": "unrelated"},
			)
			page, err := completeNativeProduct(provider.Page{Rows: rows, NextToken: "next-" + spec.suffix, Scanned: 41, Requests: 3}, nil, spec, "test."+spec.suffix)
			if err != nil {
				t.Fatalf("completeNativeProduct() error = %v", err)
			}
			wantRows := 1
			if len(spec.kinds) > 1 {
				wantRows++
			}
			if len(page.Rows) != wantRows {
				t.Fatalf("filtered rows = %#v, want %d target-kind rows", page.Rows, wantRows)
			}
			for _, row := range page.Rows {
				if row["domain"] != spec.domain || !stringIn(spec.kinds, row["kind"].(string)) {
					t.Fatalf("filtered row = %#v, contains a non-target domain/kind", row)
				}
			}
			if page.NextToken != "next-"+spec.suffix || page.Scanned != 41 || page.Requests != 3 {
				t.Fatalf("page stats = %#v, want cursor and scan/request accounting preserved", page)
			}
		})
	}
}

func TestVolcengineNativeProductsRejectLookalikeIndexedResources(t *testing.T) {
	instanceSpec, ok := nativeProductFor(model.ProviderVolcengine, "volcengine.compute.list_instances")
	if !ok {
		t.Fatal("Volcengine instance product is not registered")
	}
	instanceRows := []map[string]any{
		{"domain": "compute", "kind": "instance", "native": map[string]any{"resource_type": "Volcengine::ECS::Instance"}},
		{"domain": "compute", "kind": "instance", "native": map[string]any{"resource_type": "Volcengine::ECS::Invocation"}},
		{"domain": "compute", "kind": "instance", "native": map[string]any{"resource_type": "Volcengine::FileNAS::Instance"}},
	}
	page, err := completeNativeProduct(provider.Page{Rows: instanceRows}, nil, instanceSpec, "volcengine.compute.list_instances")
	if err != nil || len(page.Rows) != 1 || page.Rows[0]["native"].(map[string]any)["resource_type"] != "Volcengine::ECS::Instance" {
		t.Fatalf("Volcengine instance product rows = %#v, err=%v, want only ECS instances", page.Rows, err)
	}

	diskSpec, ok := nativeProductFor(model.ProviderVolcengine, "volcengine.compute.list_disks")
	if !ok {
		t.Fatal("Volcengine disk product is not registered")
	}
	diskRows := []map[string]any{
		{"domain": "compute", "kind": "disk", "native": map[string]any{"resource_type": "Volcengine::StorageEBS::Volume"}},
		{"domain": "compute", "kind": "disk", "native": map[string]any{"resource_type": "snapshot"}},
	}
	page, err = completeNativeProduct(provider.Page{Rows: diskRows}, nil, diskSpec, "volcengine.compute.list_disks")
	if err != nil || len(page.Rows) != 1 || page.Rows[0]["native"].(map[string]any)["resource_type"] != "Volcengine::StorageEBS::Volume" {
		t.Fatalf("Volcengine disk product rows = %#v, err=%v, want only EBS volumes", page.Rows, err)
	}

	bucketSpec, ok := nativeProductFor(model.ProviderVolcengine, "volcengine.storage.list_buckets")
	if !ok {
		t.Fatal("Volcengine bucket product is not registered")
	}
	bucketRows := []map[string]any{
		{"domain": "storage", "kind": "bucket", "native": map[string]any{"resource_type": "Volcengine::TOS::Bucket"}},
		{"domain": "storage", "kind": "bucket", "native": map[string]any{"resource_type": "Volcengine::AutoScaling::ScalingGroup"}},
		{"domain": "storage", "kind": "bucket", "native": map[string]any{"resource_type": "snapshot"}},
	}
	page, err = completeNativeProduct(provider.Page{Rows: bucketRows}, nil, bucketSpec, "volcengine.storage.list_buckets")
	if err != nil || len(page.Rows) != 1 || page.Rows[0]["native"].(map[string]any)["resource_type"] != "Volcengine::TOS::Bucket" {
		t.Fatalf("Volcengine bucket product rows = %#v, err=%v, want only TOS buckets", page.Rows, err)
	}

	iamSpec, ok := nativeProductFor(model.ProviderVolcengine, "volcengine.iam.list_resources")
	if !ok {
		t.Fatal("Volcengine IAM product is not registered")
	}
	iamRows := []map[string]any{
		{"domain": "iam", "kind": "user", "native": map[string]any{"resource_type": "Volcengine::IAM::User"}},
		{"domain": "iam", "kind": "role", "native": map[string]any{"resource_type": "Volcengine::IAM::Role"}},
		{"domain": "iam", "kind": "policy", "native": map[string]any{"resource_type": "Volcengine::IAM::Policy"}},
		{"domain": "iam", "kind": "role", "native": map[string]any{"resource_type": "Volcengine::IAM::Group"}},
		{"domain": "iam", "kind": "policy", "native": map[string]any{"resource_type": "Volcengine::StorageEBS::SnapshotPolicy"}},
	}
	page, err = completeNativeProduct(provider.Page{Rows: iamRows}, nil, iamSpec, "volcengine.iam.list_resources")
	if err != nil || len(page.Rows) != 4 {
		t.Fatalf("Volcengine IAM product rows = %#v, err=%v, want only IAM resource types", page.Rows, err)
	}
	for _, row := range page.Rows {
		typeName := row["native"].(map[string]any)["resource_type"]
		if !strings.HasPrefix(typeName.(string), "Volcengine::IAM::") {
			t.Fatalf("Volcengine IAM product row = %#v, contains non-IAM resource type", row)
		}
	}
}

func TestNativeProductErrorsAreAttributedToProductOperation(t *testing.T) {
	spec := nativeProductCatalog[0]
	operation := nativeProductOperationName(model.ProviderAWS, spec)
	upstream := &provider.Error{Code: "permission_denied", Message: "inventory denied", Operation: awsInventoryOperation}
	_, err := completeNativeProduct(provider.Page{}, upstream, spec, operation)
	var providerErr *provider.Error
	if !asProviderError(err, &providerErr) || providerErr.Operation != operation || providerErr.Code != upstream.Code {
		t.Fatalf("completeNativeProduct() error = %#v, want operation attribution to %q", err, operation)
	}
}

func TestInstanceDetailCatalogRegistersSevenOperationsAndResourceCapabilities(t *testing.T) {
	wantServices := map[model.Provider]string{
		model.ProviderAWS:        "ec2",
		model.ProviderGCP:        "compute",
		model.ProviderAzure:      "compute",
		model.ProviderAlibaba:    "ecs",
		model.ProviderHuawei:     "ecs",
		model.ProviderTencent:    "cvm",
		model.ProviderVolcengine: "ecs",
	}
	seen := map[string]bool{}
	for _, adapter := range nativeProductTestAdapters() {
		spec, ok := instanceDetailCatalog[adapter.Provider()]
		if !ok {
			t.Fatalf("instance detail catalog has no %s entry", adapter.Provider())
		}
		operation := requireOperation(t, adapter.Operations(), spec.operation)
		if operation.Service != wantServices[adapter.Provider()] {
			t.Errorf("%s detail service = %q, want %q", operation.Name, operation.Service, wantServices[adapter.Provider()])
		}
		if seen[operation.Name] {
			t.Fatalf("duplicate instance detail operation %q", operation.Name)
		}
		seen[operation.Name] = true
		resourceCapability := capabilityForSource(adapter.Capabilities(), model.SourceResources)
		if !stringIn(resourceCapability.Operations, operation.Name) {
			t.Errorf("%s resources capability does not expose detail operation", operation.Name)
		}
		params := map[string]any{}
		for _, required := range spec.required {
			params[required] = detailParameterValue(required)
		}
		if err := validateInstanceDetailRequest(spec, provider.NativeRequest{Params: params}); err != nil {
			t.Errorf("%s valid required parameters rejected: %v", operation.Name, err)
		}
		if err := operation.ValidateParams(map[string]any{"unknown": "blocked"}); err == nil {
			t.Errorf("%s schema accepted unknown parameter", operation.Name)
		}
		for _, forbidden := range []string{"query", "url", "action", "view_arn", "user_data", "metadata", "password", "console", "vnc"} {
			if err := operation.ValidateParams(map[string]any{forbidden: "blocked"}); err == nil {
				t.Errorf("%s schema accepted forbidden parameter %q", operation.Name, forbidden)
			}
		}
		for _, forbidden := range []string{"action", "url", "metadata", "user_data"} {
			if _, err := adapter.NativeRead(context.Background(), provider.NativeRequest{Operation: operation.Name, Params: map[string]any{forbidden: "blocked"}}); err == nil {
				t.Errorf("%s direct NativeRead accepted forbidden parameter %q", operation.Name, forbidden)
			}
		}
		if err := validateInstanceDetailRequest(spec, provider.NativeRequest{Params: params, PageToken: "opaque"}); !hasProviderErrorCode(err, "cursor_not_supported") {
			t.Errorf("%s cursor validation error = %#v, want cursor_not_supported", operation.Name, err)
		}
		missing := map[string]any{}
		for _, required := range spec.required[1:] {
			missing[required] = detailParameterValue(required)
		}
		if err := validateInstanceDetailRequest(spec, provider.NativeRequest{Params: missing}); !hasProviderErrorCode(err, "missing_parameter") {
			t.Errorf("%s missing required parameter error = %#v, want missing_parameter", operation.Name, err)
		}
	}
	if len(seen) != len(model.Providers) {
		t.Fatalf("instance detail operations = %d, want %d", len(seen), len(model.Providers))
	}
}

func TestDeepDetailCatalogRegistersClosedOperationsAndCapabilities(t *testing.T) {
	seen := map[string]bool{}
	for _, adapter := range nativeProductTestAdapters() {
		specs := deepDetailCatalog[adapter.Provider()]
		want := 2
		if adapter.Provider() == model.ProviderVolcengine {
			want = 4
		}
		if len(specs) != want {
			t.Fatalf("%s deep detail catalog entries = %d, want %d", adapter.Provider(), len(specs), want)
		}
		capability := capabilityForSource(adapter.Capabilities(), model.SourceResources)
		for _, spec := range specs {
			operation := requireOperation(t, adapter.Operations(), spec.operation)
			if operation.Service != spec.service || !stringIn(capability.Operations, spec.operation) {
				t.Fatalf("%s deep operation = %#v, want service %q and resources exposure", spec.operation, operation, spec.service)
			}
			if seen[spec.operation] {
				t.Fatalf("duplicate deep detail operation %q", spec.operation)
			}
			seen[spec.operation] = true
			params := map[string]any{}
			for _, name := range spec.required {
				params[name] = deepParameterValue(name)
			}
			if err := validateDeepDetailRequest(spec, provider.NativeRequest{Params: params}); err != nil {
				t.Errorf("%s valid required parameters rejected: %v", spec.operation, err)
			}
			for _, name := range spec.required {
				missing := cloneStringAny(params)
				delete(missing, name)
				if err := validateDeepDetailRequest(spec, provider.NativeRequest{Params: missing}); !hasProviderErrorCode(err, "missing_parameter") {
					t.Errorf("%s missing %s error = %#v, want missing_parameter", spec.operation, name, err)
				}
			}
			if err := validateDeepDetailRequest(spec, provider.NativeRequest{Params: params, PageToken: "opaque"}); !hasProviderErrorCode(err, "cursor_not_supported") {
				t.Errorf("%s cursor error = %#v, want cursor_not_supported", spec.operation, err)
			}
			for _, forbidden := range []string{"query", "url", "action", "view_arn", "password", "connection_string", "endpoint", "kubeconfig", "certificate", "token", "secret", "user_data", "metadata"} {
				if _, ok := operation.Parameters[forbidden]; ok {
					t.Errorf("%s schema exposes forbidden parameter %q", spec.operation, forbidden)
				}
				if err := operation.ValidateParams(map[string]any{forbidden: "blocked"}); err == nil {
					t.Errorf("%s schema accepted forbidden parameter %q", spec.operation, forbidden)
				}
				if _, err := adapter.NativeRead(context.Background(), provider.NativeRequest{Operation: spec.operation, Params: map[string]any{forbidden: "blocked"}}); err == nil {
					t.Errorf("%s direct NativeRead accepted forbidden parameter %q", spec.operation, forbidden)
				}
			}
		}
	}
	want := len(model.Providers)*2 + 2
	if len(seen) != want {
		t.Fatalf("deep detail operation count = %d, want %d", len(seen), want)
	}
}

func TestDeepDetailScopeValidationFailsClosedPerProvider(t *testing.T) {
	tests := []struct {
		name    string
		adapter provider.Adapter
		spec    deepDetailSpec
		params  map[string]any
		region  string
		code    string
	}{
		{name: "aws account", adapter: &awsAdapter{name: "aws-prod", profile: config.Profile{Regions: []string{"us-east-1"}}}, spec: deepDetailCatalog[model.ProviderAWS][0], params: map[string]any{"db_instance_identifier": "db-1"}, region: "us-east-1", code: "capability_unavailable"},
		{name: "gcp project", adapter: &gcpAdapter{name: "gcp-prod"}, spec: deepDetailCatalog[model.ProviderGCP][0], params: map[string]any{"project_id": "project-a", "instance": "db-1"}, code: "capability_unavailable"},
		{name: "azure subscription", adapter: &azureAdapter{name: "azure-prod"}, spec: deepDetailCatalog[model.ProviderAzure][0], params: map[string]any{"subscription_id": "sub-a", "resource_group": "rg-a", "name": "db-1"}, code: "capability_unavailable"},
		{name: "alibaba account", adapter: &alibabaAdapter{name: "alibaba-prod", profile: config.Profile{Regions: []string{"cn-shanghai"}}}, spec: deepDetailCatalog[model.ProviderAlibaba][0], params: map[string]any{"db_instance_id": "db-1"}, region: "cn-shanghai", code: "capability_unavailable"},
		{name: "huawei project", adapter: &huaweiAdapter{name: "huawei-prod"}, spec: deepDetailCatalog[model.ProviderHuawei][0], params: map[string]any{"project_id": "project-a", "instance_id": "db-1"}, code: "capability_unavailable"},
		{name: "tencent account", adapter: &tencentAdapter{name: "tencent-prod", profile: config.Profile{Regions: []string{"ap-singapore"}}}, spec: deepDetailCatalog[model.ProviderTencent][0], params: map[string]any{"instance_id": "db-1"}, region: "ap-singapore", code: "capability_unavailable"},
		{name: "volcengine account", adapter: &volcengineAdapter{name: "volcengine-prod", profile: config.Profile{Regions: []string{"cn-beijing"}}}, spec: deepDetailCatalog[model.ProviderVolcengine][0], params: map[string]any{"instance_id": "db-1"}, region: "cn-beijing", code: "capability_unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.adapter.NativeRead(context.Background(), provider.NativeRequest{Operation: tt.spec.operation, Region: tt.region, Params: tt.params})
			if !hasProviderErrorCode(err, tt.code) {
				t.Fatalf("%s NativeRead error = %#v, want %s before SDK access", tt.spec.operation, err, tt.code)
			}
		})
	}
}

func TestAWSDeepDetailsUseFixedSDKCallsAndScope(t *testing.T) {
	t.Setenv("MCP_TEST_AWS_ACCESS", "access")
	t.Setenv("MCP_TEST_AWS_SECRET", "secret")
	var rdsInput *rds.DescribeDBInstancesInput
	var eksInput *eks.DescribeClusterInput
	previousRDS, previousEKS := newAWSRDSDetailClient, newAWSEKSDetailClient
	t.Cleanup(func() { newAWSRDSDetailClient, newAWSEKSDetailClient = previousRDS, previousEKS })
	newAWSRDSDetailClient = func(cfg awsbase.Config) awsRDSDetailAPI {
		if cfg.Region != "us-east-1" {
			t.Errorf("AWS RDS region = %q, want us-east-1", cfg.Region)
		}
		return awsRDSDeepStub{capture: &rdsInput}
	}
	newAWSEKSDetailClient = func(cfg awsbase.Config) awsEKSDetailAPI {
		if cfg.Region != "us-east-1" {
			t.Errorf("AWS EKS region = %q, want us-east-1", cfg.Region)
		}
		return awsEKSDeepStub{capture: &eksInput}
	}
	adapter := &awsAdapter{name: "aws-prod", profile: config.Profile{
		Credential: config.Credential{Source: "env", Env: map[string]string{"AWS_ACCESS_KEY_ID": "MCP_TEST_AWS_ACCESS", "AWS_SECRET_ACCESS_KEY": "MCP_TEST_AWS_SECRET"}},
		Scopes:     config.Scopes{Accounts: []string{"123456789012"}}, Regions: []string{"us-east-1"},
	}}
	for _, tt := range []struct {
		operation string
		params    map[string]any
	}{
		{operation: "aws.rds.describe_db_instance", params: map[string]any{"db_instance_identifier": "db-1"}},
		{operation: "aws.eks.describe_cluster", params: map[string]any{"name": "cluster-a"}},
	} {
		_, err := adapter.NativeRead(context.Background(), provider.NativeRequest{Operation: tt.operation, Region: "us-east-1", Params: tt.params})
		if err != nil {
			t.Fatalf("%s NativeRead() error = %v, want stubbed success", tt.operation, err)
		}
	}
	if rdsInput == nil || awsbase.ToString(rdsInput.DBInstanceIdentifier) != "db-1" {
		t.Fatalf("AWS RDS input = %#v, want only DB instance identifier db-1", rdsInput)
	}
	if eksInput == nil || awsbase.ToString(eksInput.Name) != "cluster-a" {
		t.Fatalf("AWS EKS input = %#v, want only cluster name cluster-a", eksInput)
	}
	if _, err := adapter.NativeRead(context.Background(), provider.NativeRequest{Operation: "aws.rds.describe_db_instance", Region: "us-west-2", Params: map[string]any{"db_instance_identifier": "db-1"}}); !hasProviderErrorCode(err, "scope_not_allowed") {
		t.Fatalf("AWS deep detail out-of-allowlist region error = %#v, want scope_not_allowed", err)
	}
}

func TestAlibabaDeepDetailsUseFixedSDKCallsAndAccountRegion(t *testing.T) {
	var rdsRequest *alirds.DescribeDBInstanceAttributeRequest
	var csRequest *alics.DescribeClusterDetailRequest
	previousRDS, previousCS := newAlibabaRDSDetailClient, newAlibabaCSDetailClient
	t.Cleanup(func() { newAlibabaRDSDetailClient, newAlibabaCSDetailClient = previousRDS, previousCS })
	newAlibabaRDSDetailClient = func(_ config.Profile, region string) (alibabaRDSDetailAPI, error) {
		if region != "cn-shanghai" {
			t.Errorf("Alibaba RDS region = %q, want cn-shanghai", region)
		}
		return alibabaRDSDeepStub{capture: &rdsRequest}, nil
	}
	newAlibabaCSDetailClient = func(_ config.Profile, region string) (alibabaCSDetailAPI, error) {
		if region != "cn-shanghai" {
			t.Errorf("Alibaba CS region = %q, want cn-shanghai", region)
		}
		return alibabaCSDeepStub{capture: &csRequest}, nil
	}
	adapter := &alibabaAdapter{name: "alibaba-prod", profile: config.Profile{Scopes: config.Scopes{Accounts: []string{"123456789012"}}, Regions: []string{"cn-shanghai"}}}
	for _, tt := range []struct {
		operation string
		params    map[string]any
	}{
		{operation: "alibaba.rds.describe_db_instance_attribute", params: map[string]any{"db_instance_id": "db-1"}},
		{operation: "alibaba.cs.describe_cluster_detail", params: map[string]any{"cluster_id": "cluster-a"}},
	} {
		_, err := adapter.NativeRead(context.Background(), provider.NativeRequest{Operation: tt.operation, Region: "cn-shanghai", Params: tt.params})
		requireDeepProviderError(t, err, tt.operation, "alibaba_api_error")
	}
	if rdsRequest == nil || rdsRequest.DBInstanceId != "db-1" {
		t.Fatalf("Alibaba RDS request = %#v, want DBInstanceId db-1", rdsRequest)
	}
	if csRequest == nil || csRequest.ClusterId != "cluster-a" {
		t.Fatalf("Alibaba CS request = %#v, want ClusterId cluster-a", csRequest)
	}
}

func TestGCPDeepDetailsUseFixedRESTPathsAndProjectScope(t *testing.T) {
	credentialsPath := t.TempDir() + "/gcp-credentials.json"
	if err := os.WriteFile(credentialsPath, []byte(`{"type":"authorized_user","client_id":"test-client","client_secret":"test-secret","refresh_token":"test-refresh"}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Setenv("MCP_TEST_GCP_DEEP_CREDENTIALS", credentialsPath)
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	var paths []string
	http.DefaultTransport = providerTestRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "oauth2.googleapis.com" {
			return providerTestHTTPResponse(request, `{"access_token":"test-token","token_type":"Bearer","expires_in":3600}`), nil
		}
		if request.URL.Host != "sqladmin.googleapis.com" && request.URL.Host != "container.googleapis.com" {
			return nil, fmt.Errorf("unexpected GCP deep host %q", request.URL.Host)
		}
		paths = append(paths, request.URL.Path)
		return providerTestHTTPResponse(request, `{}`), nil
	})
	adapter := &gcpAdapter{name: "gcp-prod", profile: config.Profile{
		Credential: config.Credential{Source: "env", Env: map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": "MCP_TEST_GCP_DEEP_CREDENTIALS"}},
		Scopes:     config.Scopes{Projects: []string{"project-a"}},
	}}
	_, sqlErr := adapter.NativeRead(context.Background(), provider.NativeRequest{Operation: "gcp.sql.instances.get", Params: map[string]any{"project_id": "project-a", "instance": "db-1"}})
	_, gkeErr := adapter.NativeRead(context.Background(), provider.NativeRequest{Operation: "gcp.container.clusters.get", Params: map[string]any{"project_id": "project-a", "location": "us-east1", "cluster": "cluster-a"}})
	if !hasProviderErrorCode(sqlErr, "not_found") || !hasProviderErrorCode(gkeErr, "not_found") {
		t.Fatalf("GCP deep errors = (%#v, %#v), want not_found after fixed REST requests", sqlErr, gkeErr)
	}
	if !stringIn(paths, "/sql/v1beta4/projects/project-a/instances/db-1") || !stringIn(paths, "/v1/projects/project-a/locations/us-east1/clusters/cluster-a") {
		t.Fatalf("GCP deep paths = %#v, want fixed SQL and GKE paths", paths)
	}
	if _, err := adapter.NativeRead(context.Background(), provider.NativeRequest{Operation: "gcp.sql.instances.get", Params: map[string]any{"project_id": "project-b", "instance": "db-1"}}); !hasProviderErrorCode(err, "scope_not_allowed") {
		t.Fatalf("GCP deep project outside allowlist error = %#v, want scope_not_allowed", err)
	}
}

func TestAzureDeepDetailsUseFixedManagementPathsAndSubscriptionScope(t *testing.T) {
	authority := httptest.NewTLSServer(nil)
	t.Cleanup(authority.Close)
	useAzureTestAuthority(t, authority)
	t.Setenv("AZURE_AUTHORITY_HOST", authority.URL)
	t.Setenv("MCP_TEST_AZURE_DEEP_TENANT", "adfs")
	t.Setenv("MCP_TEST_AZURE_DEEP_CLIENT", "client")
	t.Setenv("MCP_TEST_AZURE_DEEP_SECRET", "secret")
	authority.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, ".well-known/openid-configuration") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, fmt.Sprintf(`{"authorization_endpoint":%q,"issuer":%q,"token_endpoint":%q}`, authority.URL+"/adfs/v2.0", authority.URL+"/adfs/v2.0", authority.URL+"/adfs/oauth2/v2.0/token"))
			return
		}
		if strings.HasSuffix(request.URL.Path, "/oauth2/v2.0/token") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"azure-token","token_type":"Bearer","expires_in":3600}`)
			return
		}
		http.NotFound(w, request)
	})
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	authorityTransport := authority.Client().Transport
	var paths []string
	http.DefaultTransport = providerTestRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == strings.TrimPrefix(authority.URL, "https://") {
			return authorityTransport.RoundTrip(request)
		}
		if request.URL.Host == "login.microsoftonline.com" {
			return providerTestHTTPResponse(request, `{"access_token":"azure-token","token_type":"Bearer","expires_in":3600}`), nil
		}
		if request.URL.Host != "management.azure.com" {
			return nil, fmt.Errorf("unexpected Azure deep host %q", request.URL.Host)
		}
		paths = append(paths, request.URL.RequestURI())
		if strings.Contains(request.URL.Path, "dbforpostgresql") {
			return providerTestHTTPResponse(request, `{"id":"/subscriptions/sub-a/resourceGroups/rg-a/providers/Microsoft.DBforPostgreSQL/flexibleServers/db-1","name":"db-1","location":"eastasia","properties":{"state":"Ready"}}`), nil
		}
		return providerTestHTTPResponse(request, `{"id":"/subscriptions/sub-a/resourceGroups/rg-a/providers/Microsoft.ContainerService/managedClusters/cluster-a","name":"cluster-a","location":"eastasia","properties":{"provisioningState":"Succeeded"}}`), nil
	})
	adapter := &azureAdapter{name: "azure-prod", profile: config.Profile{
		Credential: config.Credential{Source: "env", Env: map[string]string{"AZURE_TENANT_ID": "MCP_TEST_AZURE_DEEP_TENANT", "AZURE_CLIENT_ID": "MCP_TEST_AZURE_DEEP_CLIENT", "AZURE_CLIENT_SECRET": "MCP_TEST_AZURE_DEEP_SECRET"}},
		Scopes:     config.Scopes{Subscriptions: []string{"sub-a"}},
	}}
	for _, tt := range []struct {
		operation string
		params    map[string]any
	}{
		{operation: "azure.dbforpostgresql.flexible_servers.get", params: map[string]any{"subscription_id": "sub-a", "resource_group": "rg-a", "name": "db-1"}},
		{operation: "azure.containerservice.managed_clusters.get", params: map[string]any{"subscription_id": "sub-a", "resource_group": "rg-a", "name": "cluster-a"}},
	} {
		page, err := adapter.NativeRead(context.Background(), provider.NativeRequest{Operation: tt.operation, Params: tt.params})
		if err != nil || len(page.Rows) != 1 {
			t.Fatalf("%s result = (%#v, %v), want one normalized row", tt.operation, page, err)
		}
	}
	if !stringIn(paths, "/subscriptions/sub-a/resourceGroups/rg-a/providers/Microsoft.DBforPostgreSQL/flexibleServers/db-1?api-version=2025-08-01") || !stringIn(paths, "/subscriptions/sub-a/resourceGroups/rg-a/providers/Microsoft.ContainerService/managedClusters/cluster-a?api-version=2025-10-01") {
		t.Fatalf("Azure deep request paths = %#v, want fixed PostgreSQL and AKS endpoints", paths)
	}
	if _, err := adapter.NativeRead(context.Background(), provider.NativeRequest{Operation: "azure.containerservice.managed_clusters.get", Params: map[string]any{"subscription_id": "sub-b", "resource_group": "rg-a", "name": "cluster-a"}}); !hasProviderErrorCode(err, "scope_not_allowed") {
		t.Fatalf("Azure deep subscription outside allowlist error = %#v, want scope_not_allowed", err)
	}
}

func TestTencentDeepDetailsUseFixedActionsAndAccountRegion(t *testing.T) {
	t.Setenv("MCP_TEST_TENCENT_DEEP_ID", "secret-id")
	t.Setenv("MCP_TEST_TENCENT_DEEP_KEY", "secret-key")
	var cdbRequest, tkeRequest tencentDetailRequest
	previousCDB, previousTKE := newTencentCDBDetailClient, newTencentTKEDetailClient
	t.Cleanup(func() { newTencentCDBDetailClient, newTencentTKEDetailClient = previousCDB, previousTKE })
	newTencentCDBDetailClient = func(_ tencent.CredentialIface, region string) tencentDetailSender {
		if region != "ap-singapore" {
			t.Errorf("Tencent CDB region = %q, want ap-singapore", region)
		}
		return tencentInstanceDetailStub{capture: &cdbRequest, err: fmt.Errorf("stub CDB")}
	}
	newTencentTKEDetailClient = func(_ tencent.CredentialIface, region string) tencentDetailSender {
		if region != "ap-singapore" {
			t.Errorf("Tencent TKE region = %q, want ap-singapore", region)
		}
		return tencentInstanceDetailStub{capture: &tkeRequest, err: fmt.Errorf("stub TKE")}
	}
	adapter := &tencentAdapter{name: "tencent-prod", profile: config.Profile{
		Credential: config.Credential{Source: "env", Env: map[string]string{"TENCENTCLOUD_SECRET_ID": "MCP_TEST_TENCENT_DEEP_ID", "TENCENTCLOUD_SECRET_KEY": "MCP_TEST_TENCENT_DEEP_KEY"}},
		Scopes:     config.Scopes{Accounts: []string{"1000000001"}}, Regions: []string{"ap-singapore"},
	}}
	for _, tt := range []struct {
		operation string
		params    map[string]any
	}{
		{operation: "tencent.cdb.describe_db_instances", params: map[string]any{"instance_id": "db-1"}},
		{operation: "tencent.tke.describe_cluster", params: map[string]any{"cluster_id": "cluster-a"}},
	} {
		_, err := adapter.NativeRead(context.Background(), provider.NativeRequest{Operation: tt.operation, Region: "ap-singapore", Params: tt.params})
		requireDeepProviderError(t, err, tt.operation, "tencent_api_error")
	}
	assertTencentDeepRequest(t, cdbRequest.request, "cdb", "2017-03-20", "DescribeDBInstances", `"InstanceIds":["db-1"]`)
	assertTencentDeepRequest(t, tkeRequest.request, "tke", "2018-05-25", "DescribeClusters", `"ClusterIds":["cluster-a"]`)
}

func TestVolcengineDeepDetailsUseFixedSDKInputsAndAccountRegion(t *testing.T) {
	t.Setenv("MCP_TEST_VOLC_DEEP_ACCESS", "access")
	t.Setenv("MCP_TEST_VOLC_DEEP_SECRET", "secret")
	var rdsInput *volcrds.DescribeDBInstanceDetailInput
	var redisInput *volcredis.DescribeDBInstanceDetailInput
	var vkeInput *volcvke.ListClustersInput
	var tosInput *volctos.GetBucketInfoInput
	previousRDS, previousRedis, previousVKE, previousTOS := newVolcengineRDSDetailClient, newVolcengineRedisDetailClient, newVolcengineVKEDetailClient, newVolcengineTOSDetailClient
	t.Cleanup(func() {
		newVolcengineRDSDetailClient, newVolcengineRedisDetailClient, newVolcengineVKEDetailClient = previousRDS, previousRedis, previousVKE
		newVolcengineTOSDetailClient = previousTOS
	})
	newVolcengineRDSDetailClient = func(_ *volcsession.Session) volcengineRDSDetailAPI {
		return volcengineRDSDeepStub{capture: &rdsInput, err: fmt.Errorf("stub RDS")}
	}
	newVolcengineRedisDetailClient = func(_ *volcsession.Session) volcengineRedisDetailAPI {
		return volcengineRedisDeepStub{capture: &redisInput, err: fmt.Errorf("stub Redis")}
	}
	newVolcengineVKEDetailClient = func(_ *volcsession.Session) volcengineVKEDetailAPI {
		return volcengineVKEDetailDeepStub{capture: &vkeInput, err: fmt.Errorf("stub VKE")}
	}
	newVolcengineTOSDetailClient = func(_ config.Profile, region, _ string) (volcengineTOSDetailAPI, error) {
		if region != "cn-beijing" {
			t.Errorf("Volcengine TOS region = %q, want cn-beijing", region)
		}
		return &volcengineTOSDetailDeepStub{capture: &tosInput, err: fmt.Errorf("stub TOS")}, nil
	}
	adapter := &volcengineAdapter{name: "volcengine-prod", profile: config.Profile{
		Credential: config.Credential{Source: "env", Env: map[string]string{
			"VOLCENGINE_ACCESS_KEY_ID":     "MCP_TEST_VOLC_DEEP_ACCESS",
			"VOLCENGINE_SECRET_ACCESS_KEY": "MCP_TEST_VOLC_DEEP_SECRET",
		}},
		Scopes: config.Scopes{Accounts: []string{"2000000001"}}, Regions: []string{"cn-beijing"},
	}}
	for _, tt := range []struct {
		operation string
		params    map[string]any
	}{
		{operation: "volcengine.rdsmysql.describe_db_instance_detail", params: map[string]any{"instance_id": "db-1"}},
		{operation: "volcengine.redis.describe_db_instance_detail", params: map[string]any{"instance_id": "redis-1"}},
		{operation: "volcengine.vke.list_clusters", params: map[string]any{"cluster_id": "cluster-a"}},
		{operation: "volcengine.tos.get_bucket_info", params: map[string]any{"bucket_name": "bucket-a"}},
	} {
		_, err := adapter.NativeRead(context.Background(), provider.NativeRequest{Operation: tt.operation, Region: "cn-beijing", Params: tt.params})
		requireDeepProviderError(t, err, tt.operation, "volcengine_api_error")
	}
	if rdsInput == nil || volc.StringValue(rdsInput.InstanceId) != "db-1" {
		t.Fatalf("Volcengine RDS input = %#v, want only InstanceId=db-1", rdsInput)
	}
	if redisInput == nil || volc.StringValue(redisInput.InstanceId) != "redis-1" {
		t.Fatalf("Volcengine Redis input = %#v, want only InstanceId=redis-1", redisInput)
	}
	if vkeInput == nil || vkeInput.Filter == nil || len(vkeInput.Filter.Ids) != 1 || volc.StringValue(vkeInput.Filter.Ids[0]) != "cluster-a" || volc.Int32Value(vkeInput.PageNumber) != 1 || volc.Int32Value(vkeInput.PageSize) != 1 {
		t.Fatalf("Volcengine VKE input = %#v, want one ID and page 1/1", vkeInput)
	}
	if tosInput == nil || tosInput.Bucket != "bucket-a" {
		t.Fatalf("Volcengine TOS input = %#v, want only Bucket=bucket-a", tosInput)
	}
	if _, err := adapter.NativeRead(context.Background(), provider.NativeRequest{Operation: "volcengine.vke.list_clusters", Region: "cn-shanghai", Params: map[string]any{"cluster_id": "cluster-a"}}); !hasProviderErrorCode(err, "scope_not_allowed") {
		t.Fatalf("Volcengine out-of-allowlist region error = %#v, want scope_not_allowed", err)
	}
}

func TestVolcengineVKEInventoryUsesClusterFilterPaginationAndSafeRows(t *testing.T) {
	t.Setenv("MCP_TEST_VOLC_VKE_ACCESS", "access")
	t.Setenv("MCP_TEST_VOLC_VKE_SECRET", "secret")
	var nodePoolInput *volcvke.ListNodePoolsInput
	var nodeInput *volcvke.ListNodesInput
	previous := newVolcengineVKEDetailClient
	t.Cleanup(func() { newVolcengineVKEDetailClient = previous })
	newVolcengineVKEDetailClient = func(_ *volcsession.Session) volcengineVKEDetailAPI {
		return volcengineVKEDetailDeepStub{
			nodePoolCapture: &nodePoolInput,
			nodeCapture:     &nodeInput,
			nodePoolOutput: &volcvke.ListNodePoolsOutput{
				Items: []*volcvke.ItemForListNodePoolsOutput{{
					Id:        volc.String("pool-a"),
					Name:      volc.String("workers"),
					ClusterId: volc.String("cluster-a"),
					Status:    &volcvke.StatusForListNodePoolsOutput{Phase: volc.String("Running")},
					NodeStatistics: &volcvke.NodeStatisticsForListNodePoolsOutput{
						TotalCount: volc.Int32(6), RunningCount: volc.Int32(6), FailedCount: volc.Int32(0),
					},
					AutoScaling: &volcvke.AutoScalingForListNodePoolsOutput{Enabled: volc.Bool(false), DesiredReplicas: volc.Int32(6)},
					NodeConfig: &volcvke.NodeConfigForListNodePoolsOutput{
						InstanceTypeIds:  []*string{volc.String("ecs.g3il.2xlarge")},
						SubnetIds:        []*string{volc.String("subnet-a")},
						InitializeScript: volc.String("must-not-leak"),
					},
				}},
				TotalCount: volc.Int32(3),
			},
			nodeOutput: &volcvke.ListNodesOutput{
				Items: []*volcvke.ItemForListNodesOutput{{
					Id:               volc.String("node-a"),
					MetadataName:     volc.String("worker-a"),
					ClusterId:        volc.String("cluster-a"),
					NodePoolId:       volc.String("pool-a"),
					InstanceId:       volc.String("i-a"),
					ZoneId:           volc.String("cn-beijing-a"),
					Status:           &volcvke.StatusForListNodesOutput{Phase: volc.String("Running")},
					InitializeScript: volc.String("must-not-leak"),
					KubernetesConfig: &volcvke.KubernetesConfigForListNodesOutput{Cordon: volc.Bool(false)},
				}},
				TotalCount: volc.Int32(7),
			},
		}
	}
	adapter := &volcengineAdapter{name: "volcengine-prod", profile: config.Profile{
		Credential: config.Credential{Source: "env", Env: map[string]string{
			"VOLCENGINE_ACCESS_KEY_ID":     "MCP_TEST_VOLC_VKE_ACCESS",
			"VOLCENGINE_SECRET_ACCESS_KEY": "MCP_TEST_VOLC_VKE_SECRET",
		}},
		Scopes: config.Scopes{Accounts: []string{"2000000001"}}, Regions: []string{"cn-beijing"},
	}}
	for _, name := range volcengineVKEInventoryOperationNames() {
		operation := requireOperation(t, adapter.Operations(), name)
		if operation.Service != "vke" || operation.ValidateParams(map[string]any{"cluster_id": "cluster-a"}) != nil {
			t.Fatalf("VKE operation = %#v, want closed cluster_id schema", operation)
		}
		if operation.ValidateParams(map[string]any{"cluster_id": "cluster-a", "url": "blocked"}) == nil {
			t.Fatalf("VKE operation %q accepted undeclared URL", name)
		}
		if !stringIn(capabilityForSource(adapter.Capabilities(), model.SourceResources).Operations, name) {
			t.Fatalf("VKE operation %q missing from resource capability", name)
		}
	}
	poolPage, err := adapter.NativeRead(context.Background(), provider.NativeRequest{
		Operation: volcengineVKEListNodePoolsOperation, Region: "cn-beijing", Params: map[string]any{"cluster_id": "cluster-a"}, Limit: 1,
	})
	if err != nil || len(poolPage.Rows) != 1 || poolPage.NextToken != "2" {
		t.Fatalf("VKE node pool page = %#v, err=%v, want one row and next page 2", poolPage, err)
	}
	if nodePoolInput == nil || nodePoolInput.Filter == nil || len(nodePoolInput.Filter.ClusterIds) != 1 || volc.StringValue(nodePoolInput.Filter.ClusterIds[0]) != "cluster-a" || volc.Int32Value(nodePoolInput.PageNumber) != 1 || volc.Int32Value(nodePoolInput.PageSize) != 1 {
		t.Fatalf("VKE node pool input = %#v, want fixed cluster filter and page 1/1", nodePoolInput)
	}
	poolAttributes := poolPage.Rows[0]["attributes"].(map[string]any)
	if poolPage.Rows[0]["kind"] != "node_pool" || poolPage.Rows[0]["state"] != "running" || poolAttributes["cluster_id"] != "cluster-a" || poolAttributes["node_count"] != 6 {
		t.Fatalf("VKE node pool row = %#v, want normalized cluster linkage and counts", poolPage.Rows[0])
	}
	if strings.Contains(fmt.Sprint(poolPage.Rows[0]), "must-not-leak") {
		t.Fatalf("VKE node pool row leaked initialization script: %#v", poolPage.Rows[0])
	}
	nodePage, err := adapter.NativeRead(context.Background(), provider.NativeRequest{
		Operation: volcengineVKEListNodesOperation, Region: "cn-beijing", Params: map[string]any{"cluster_id": "cluster-a"}, PageToken: "2", Limit: 100,
	})
	if err != nil || len(nodePage.Rows) != 1 || nodePage.NextToken != "" {
		t.Fatalf("VKE node page = %#v, err=%v, want one final row", nodePage, err)
	}
	if nodeInput == nil || nodeInput.Filter == nil || len(nodeInput.Filter.ClusterIds) != 1 || volc.StringValue(nodeInput.Filter.ClusterIds[0]) != "cluster-a" || volc.Int32Value(nodeInput.PageNumber) != 2 || volc.Int32Value(nodeInput.PageSize) != 100 {
		t.Fatalf("VKE node input = %#v, want fixed cluster filter and page 2/100", nodeInput)
	}
	nodeAttributes := nodePage.Rows[0]["attributes"].(map[string]any)
	if nodePage.Rows[0]["kind"] != "node" || nodePage.Rows[0]["name"] != "worker-a" || nodePage.Rows[0]["state"] != "running" || nodePage.Rows[0]["zone"] != "cn-beijing-a" || nodeAttributes["node_pool_id"] != "pool-a" || nodeAttributes["instance_id"] != "i-a" {
		t.Fatalf("VKE node row = %#v, want normalized topology fields", nodePage.Rows[0])
	}
	if strings.Contains(fmt.Sprint(nodePage.Rows[0]), "must-not-leak") {
		t.Fatalf("VKE node row leaked initialization script: %#v", nodePage.Rows[0])
	}
	if _, _, _, err := validateVolcengineVKEInventoryRequest(provider.NativeRequest{Operation: volcengineVKEListNodesOperation, Params: map[string]any{"cluster_id": "cluster-a"}, PageToken: "opaque"}); !hasProviderErrorCode(err, "invalid_cursor") {
		t.Fatalf("VKE invalid cursor error = %#v, want invalid_cursor", err)
	}
}

func TestHuaweiDeepDetailsUseFixedRequestsAndFailClosedScope(t *testing.T) {
	t.Setenv("MCP_TEST_HUAWEI_DEEP_AK", "access")
	t.Setenv("MCP_TEST_HUAWEI_DEEP_SK", "secret")
	var rdsRequest *rdsmodel.ListInstancesRequest
	var cceRequest *ccemodel.ShowClusterRequest
	previousRDS, previousCCE := callHuaweiRDSDetail, callHuaweiCCEDetail
	t.Cleanup(func() { callHuaweiRDSDetail, callHuaweiCCEDetail = previousRDS, previousCCE })
	callHuaweiRDSDetail = func(_ huaweiRDSDetailAPI, request *rdsmodel.ListInstancesRequest) (*rdsmodel.ListInstancesResponse, error) {
		rdsRequest = request
		return nil, fmt.Errorf("stub Huawei RDS")
	}
	callHuaweiCCEDetail = func(_ huaweiCCEDetailAPI, request *ccemodel.ShowClusterRequest) (*ccemodel.ShowClusterResponse, error) {
		cceRequest = request
		return nil, fmt.Errorf("stub Huawei CCE")
	}
	adapter := &huaweiAdapter{name: "huawei-prod", profile: config.Profile{
		Credential: config.Credential{Source: "env", Env: map[string]string{"HUAWEICLOUD_SDK_AK": "MCP_TEST_HUAWEI_DEEP_AK", "HUAWEICLOUD_SDK_SK": "MCP_TEST_HUAWEI_DEEP_SK"}},
		Scopes:     config.Scopes{Projects: []string{"project-a"}}, Regions: []string{"cn-north-4"},
	}}
	for _, tt := range []struct {
		name      string
		operation string
		params    map[string]any
		region    string
		code      string
	}{
		{name: "database fixed request", operation: "huawei.rds.list_instances", params: map[string]any{"project_id": "project-a", "instance_id": "db-1"}, region: "cn-north-4", code: "huawei_api_error"},
		{name: "kubernetes fixed request", operation: "huawei.cce.show_cluster", params: map[string]any{"project_id": "project-a", "cluster_id": "cluster-a"}, region: "cn-north-4", code: "huawei_api_error"},
		{name: "database project", operation: "huawei.rds.list_instances", params: map[string]any{"project_id": "project-b", "instance_id": "db-1"}, region: "cn-north-4", code: "scope_not_allowed"},
		{name: "kubernetes project", operation: "huawei.cce.show_cluster", params: map[string]any{"project_id": "project-b", "cluster_id": "cluster-a"}, region: "cn-north-4", code: "scope_not_allowed"},
		{name: "database region", operation: "huawei.rds.list_instances", params: map[string]any{"project_id": "project-a", "instance_id": "db-1"}, region: "cn-south-1", code: "scope_not_allowed"},
		{name: "kubernetes region", operation: "huawei.cce.show_cluster", params: map[string]any{"project_id": "project-a", "cluster_id": "cluster-a"}, region: "cn-south-1", code: "scope_not_allowed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := adapter.NativeRead(context.Background(), provider.NativeRequest{Operation: tt.operation, Region: tt.region, Params: tt.params})
			if tt.code == "huawei_api_error" {
				requireDeepProviderError(t, err, tt.operation, tt.code)
			} else if !hasProviderErrorCode(err, tt.code) {
				t.Fatalf("%s error = %#v, want %s before Huawei SDK construction", tt.operation, err, tt.code)
			}
		})
	}
	if rdsRequest == nil || rdsRequest.Id == nil || *rdsRequest.Id != "db-1" || rdsRequest.Limit == nil || *rdsRequest.Limit != 1 {
		t.Fatalf("Huawei RDS request = %#v, want Id=db-1 and Limit=1", rdsRequest)
	}
	if cceRequest == nil || cceRequest.ClusterId != "cluster-a" {
		t.Fatalf("Huawei CCE request = %#v, want ClusterId=cluster-a", cceRequest)
	}
}

func TestDeepDetailRowsExposeUnifiedPostureAndBoundedRelatedFields(t *testing.T) {
	aws := &awsAdapter{name: "aws-prod"}
	storageEncrypted, backupRetention, multiAZ, publiclyAccessible, deletionProtection, autoMinor := true, int32(7), true, false, true, true
	rdsRow := aws.awsRDSDetailRow(rdstypes.DBInstance{
		DBInstanceIdentifier:         awsbase.String("db-1"),
		DBInstanceStatus:             awsbase.String("available"),
		StorageEncrypted:             &storageEncrypted,
		BackupRetentionPeriod:        &backupRetention,
		MultiAZ:                      &multiAZ,
		PubliclyAccessible:           &publiclyAccessible,
		DeletionProtection:           &deletionProtection,
		AutoMinorVersionUpgrade:      &autoMinor,
		EnabledCloudwatchLogsExports: []string{"error", "audit"},
	}, "us-east-1", "123456789012")
	posture := rdsRow["attributes"].(map[string]any)["posture"].(map[string]any)
	for key, want := range map[string]any{"encryption_enabled": true, "backup_enabled": true, "logging_enabled": true, "audit_enabled": true} {
		if posture[key] != want {
			t.Fatalf("AWS RDS posture[%q] = %#v, want %#v", key, posture[key], want)
		}
	}
	if _, ok := rdsRow["attributes"].(map[string]any)["posture"].(map[string]any)["raw_response"]; ok {
		t.Fatal("AWS RDS row contains raw posture response")
	}

	gcp := &gcpAdapter{name: "gcp-prod"}
	var sql gcpSQLDetailResponse
	if err := json.Unmarshal([]byte(`{"name":"db-1","state":"RUNNABLE","region":"us-east1","databaseVersion":"POSTGRES_15","diskEncryptionConfiguration":{"kmsKeyName":"projects/p/locations/l/keyRings/r/cryptoKeys/k"},"settings":{"backupConfiguration":{"enabled":true,"transactionLogRetentionDays":7}} ,"password":"must-not-leak","connectionString":"must-not-leak"}`), &sql); err != nil {
		t.Fatal(err)
	}
	sqlRow := gcp.gcpSQLDetailRow(sql, "project-a")
	sqlPosture := sqlRow["attributes"].(map[string]any)["posture"].(map[string]any)
	if sqlPosture["customer_managed_encryption_key_enabled"] != true {
		t.Fatalf("GCP SQL posture = %#v, want CMEK posture", sqlPosture)
	}
	if _, old := sqlPosture["encryption_enabled"]; old {
		t.Fatalf("GCP SQL posture = %#v, must not expose ambiguous encryption_enabled", sqlPosture)
	}
	assertDeepDetailRowSafe(t, sqlRow, "gcp", "project_id", "project-a")

	row := newDeepDetailRow(model.ProviderAWS, "aws-prod", "cluster-a", "cluster-a", "eks", "AWS::EKS::Cluster", "kubernetes", "cluster", "us-east-1", "123456789012")
	values := make([]string, 0, 105)
	for i := 0; i < 102; i++ {
		values = append(values, fmt.Sprintf("subnet-%03d", i))
	}
	values = append(values, "subnet-000", "")
	setRelated(row, "subnet_ids", values)
	related := row["attributes"].(map[string]any)["related"].(map[string]any)["subnet_ids"].([]string)
	if len(related) != 100 || related[0] != "subnet-000" || related[99] != "subnet-099" {
		t.Fatalf("related subnet IDs = len %d first %q last %q, want 100 unique bounded values", len(related), related[0], related[99])
	}
	setPosture(row, "deletion_protection", true)
	if row["attributes"].(map[string]any)["posture"].(map[string]any)["deletion_protection"] != true {
		t.Fatal("unified posture setter did not retain boolean posture")
	}
}

func TestInstanceDetailScopesFailClosed(t *testing.T) {
	operation := "aws.ec2.describe_instance"
	for _, tt := range []struct {
		name       string
		requested  string
		configured []string
		code       string
	}{
		{name: "invalid requested region", requested: "not a region", configured: []string{"us-east-1"}, code: "invalid_region"},
		{name: "requested region outside allowlist", requested: "us-west-2", configured: []string{"us-east-1"}, code: "scope_not_allowed"},
		{name: "wildcard with no unique configured region", requested: "*", configured: []string{"*"}, code: "missing_region"},
		{name: "wildcard with multiple configured regions", requested: "*", configured: []string{"us-east-1", "us-west-2"}, code: "missing_region"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := exactDetailRegion(tt.requested, tt.configured, operation); !hasProviderErrorCode(err, tt.code) {
				t.Fatalf("exactDetailRegion() error = %#v, want %s", err, tt.code)
			}
		})
	}
	if got, err := exactDetailRegion("", []string{"us-east-1"}, operation); err != nil || got != "us-east-1" {
		t.Fatalf("exactDetailRegion() = (%q, %v), want unique configured region", got, err)
	}
	for _, accounts := range [][]string{nil, {"a", "b"}} {
		if _, err := singleDetailAccount(config.Profile{Scopes: config.Scopes{Accounts: accounts}}, operation); !hasProviderErrorCode(err, "capability_unavailable") {
			t.Errorf("singleDetailAccount(%v) error = %#v, want capability_unavailable", accounts, err)
		}
	}
	if got, err := singleDetailAccount(config.Profile{Scopes: config.Scopes{Accounts: []string{"account-a"}}}, operation); err != nil || got != "account-a" {
		t.Fatalf("singleDetailAccount() = (%q, %v), want one account", got, err)
	}
	for _, tt := range []struct {
		name       string
		configured []string
		requested  string
		code       string
	}{
		{name: "empty project", configured: []string{"project-a"}, requested: "", code: "missing_parameter"},
		{name: "project scope is not configured", configured: nil, requested: "project-a", code: "capability_unavailable"},
		{name: "project outside allowlist", configured: []string{"project-a"}, requested: "project-b", code: "scope_not_allowed"},
		{name: "subscription outside allowlist", configured: []string{"sub-a"}, requested: "sub-b", code: "scope_not_allowed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := exactDetailScope(tt.configured, tt.requested, "detail", "scope"); !hasProviderErrorCode(err, tt.code) {
				t.Fatalf("exactDetailScope() error = %#v, want %s", err, tt.code)
			}
		})
	}
	if got, err := exactDetailScope([]string{"project-a"}, "projects/project-a", "detail", "project_id"); err != nil || got != "project-a" {
		t.Fatalf("exactDetailScope() = (%q, %v), want normalized allowed project", got, err)
	}
}

func TestInstanceDetailErrorsRetainOperationAndAvoidRawProviderDetails(t *testing.T) {
	operation := "gcp.compute.instances.get"
	upstream := &provider.Error{Code: "provider_denied", Operation: "compute.instances.get", Message: "permission denied"}
	err := attributeInstanceDetailError(upstream, operation)
	var providerErr *provider.Error
	if !asProviderError(err, &providerErr) || providerErr.Operation != operation || providerErr.Code != upstream.Code || providerErr.Message != upstream.Message {
		t.Fatalf("attributeInstanceDetailError() = %#v, want safe operation attribution", err)
	}
	wrapped := attributeInstanceDetailError(fmt.Errorf("upstream failure"), operation)
	if !asProviderError(wrapped, &providerErr) || providerErr.Operation != operation || providerErr.Code != "provider_api_error" {
		t.Fatalf("attributeInstanceDetailError(non-provider) = %#v, want provider_api_error attribution", wrapped)
	}
}

func TestInstanceDetailRowsKeepStableFieldsAndDropSensitivePayloads(t *testing.T) {
	var gcpDetail gcpInstanceDetailResponse
	if err := json.Unmarshal([]byte(`{"id":"123","name":"web-1","status":"RUNNING","zone":"us-east1-a","machineType":"zones/us-east1-a/machineTypes/e2","creationTimestamp":"2026-01-02T03:04:05Z","labels":{"env":"test"},"userData":"admin-password"}`), &gcpDetail); err != nil {
		t.Fatal(err)
	}
	gcpRow := (&gcpAdapter{name: "gcp-prod"}).gcpInstanceDetailRow(gcpDetail, "project-a", "us-east1-a")
	assertDetailRowSafe(t, gcpRow, "gcp", "project_id", "project-a")
	if gcpRow["id"] != "123" || gcpRow["kind"] != "instance" || gcpRow["state"] != "running" || gcpRow["native"].(map[string]any)["instance_type"] != "e2" {
		t.Fatalf("GCP detail row = %#v, want stable instance fields", gcpRow)
	}

	var azureDetail azureVMDetailResponse
	if err := json.Unmarshal([]byte(`{"id":"/subscriptions/sub-a/vm/web-1","name":"web-1","location":"eastasia","zones":["1"],"properties":{"provisioningState":"Succeeded","hardwareProfile":{"vmSize":"Standard_D2"},"storageProfile":{"osDisk":{"osType":"Linux"}},"customData":"console-password"},"customData":"should-not-appear"}`), &azureDetail); err != nil {
		t.Fatal(err)
	}
	azureRow := (&azureAdapter{name: "azure-prod"}).azureVMDetailRow(azureDetail, "sub-a", "rg-a")
	assertDetailRowSafe(t, azureRow, "azure", "subscription_id", "sub-a")
	if azureRow["id"] == "" || azureRow["kind"] != "instance" || azureRow["state"] != "succeeded" || azureRow["native"].(map[string]any)["instance_type"] != "Standard_D2" {
		t.Fatalf("Azure detail row = %#v, want stable instance fields", azureRow)
	}

	var huaweiDetail ecsmodel.ServerDetail
	if err := json.Unmarshal([]byte(`{"id":"server-a","name":"web-1","status":"ACTIVE","created":"2026-01-02T03:04:05Z","OS-EXT-SRV-ATTR:user_data":"top-secret-user-data","metadata":{"vpc_id":"vpc-a","os_type":"Linux","metering.image_id":"img-a","admin":"password","console":"vnc-secret","arbitrary":"must-not-leak"}}`), &huaweiDetail); err != nil {
		t.Fatal(err)
	}
	huaweiRow := (&huaweiAdapter{name: "huawei-prod"}).huaweiServerDetailRow(huaweiDetail, "ap-southeast-3", "project-a")
	assertDetailRowSafe(t, huaweiRow, "huawei", "project_id", "project-a")
	if huaweiRow["id"] != "server-a" || huaweiRow["kind"] != "instance" || huaweiRow["state"] != "active" || huaweiRow["attributes"].(map[string]any)["vpc_id"] != "vpc-a" {
		t.Fatalf("Huawei detail row = %#v, want stable instance fields", huaweiRow)
	}
}

func TestAWSInstanceDetailUsesFixedDescribeInstancesRequest(t *testing.T) {
	t.Setenv("MCP_TEST_AWS_ACCESS", "access")
	t.Setenv("MCP_TEST_AWS_SECRET", "secret")
	var captured *ec2.DescribeInstancesInput
	previous := newAWSEC2DescribeClient
	t.Cleanup(func() { newAWSEC2DescribeClient = previous })
	newAWSEC2DescribeClient = func(cfg awsbase.Config) awsEC2DescribeAPI {
		if cfg.Region != "ap-southeast-1" {
			t.Errorf("AWS detail SDK region = %q, want ap-southeast-1", cfg.Region)
		}
		return awsInstanceDetailStub{capture: &captured, output: &ec2.DescribeInstancesOutput{Reservations: []ec2types.Reservation{{Instances: []ec2types.Instance{{InstanceId: awsbase.String("i-1"), State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}, Tags: []ec2types.Tag{{Key: awsbase.String("Name"), Value: awsbase.String("web-1")}}}}}}}}
	}
	adapter := &awsAdapter{
		name: "aws-prod",
		profile: config.Profile{
			Credential: config.Credential{Source: "env", Env: map[string]string{"AWS_ACCESS_KEY_ID": "MCP_TEST_AWS_ACCESS", "AWS_SECRET_ACCESS_KEY": "MCP_TEST_AWS_SECRET"}},
			Scopes:     config.Scopes{Accounts: []string{"123456789012"}},
			Regions:    []string{"ap-southeast-1"},
		},
	}
	page, err := adapter.NativeRead(context.Background(), provider.NativeRequest{Operation: "aws.ec2.describe_instance", Region: "ap-southeast-1", Params: map[string]any{"instance_id": "i-1"}})
	if err != nil {
		t.Fatalf("AWS detail NativeRead() error = %v", err)
	}
	if captured == nil || len(captured.InstanceIds) != 1 || captured.InstanceIds[0] != "i-1" {
		t.Fatalf("AWS DescribeInstances input = %#v, want exactly the requested instance ID", captured)
	}
	if len(page.Rows) != 1 || page.Rows[0]["id"] != "i-1" || page.Rows[0]["name"] != "web-1" || page.Scanned != 1 || page.Requests != 1 {
		t.Fatalf("AWS detail page = %#v, want one normalized row and fixed accounting", page)
	}
}

func TestAlibabaInstanceDetailUsesFixedDescribeInstanceAttributeRequest(t *testing.T) {
	var captured *aliecs.DescribeInstanceAttributeRequest
	previous := newAlibabaECSDetailClient
	t.Cleanup(func() { newAlibabaECSDetailClient = previous })
	newAlibabaECSDetailClient = func(_ config.Profile, region string) (alibabaECSDetailAPI, error) {
		if region != "cn-shanghai" {
			t.Errorf("Alibaba detail region = %q, want cn-shanghai", region)
		}
		return alibabaInstanceDetailStub{capture: &captured, response: &aliecs.DescribeInstanceAttributeResponse{InstanceId: "i-1", InstanceName: "web-1", RegionId: "cn-shanghai"}}, nil
	}
	adapter := &alibabaAdapter{name: "alibaba-prod", profile: config.Profile{Scopes: config.Scopes{Accounts: []string{"123456789012"}}, Regions: []string{"cn-shanghai"}}}
	page, err := adapter.NativeRead(context.Background(), provider.NativeRequest{Operation: "alibaba.ecs.describe_instance_attribute", Region: "cn-shanghai", Params: map[string]any{"instance_id": "i-1"}})
	if err != nil {
		t.Fatalf("Alibaba detail NativeRead() error = %v", err)
	}
	if captured == nil || captured.InstanceId != "i-1" {
		t.Fatalf("Alibaba request = %#v, want exactly the requested instance ID", captured)
	}
	if len(page.Rows) != 1 || page.Rows[0]["id"] != "i-1" || page.Rows[0]["name"] != "web-1" {
		t.Fatalf("Alibaba detail page = %#v, want one normalized row", page)
	}
}

func TestGCPInstanceDetailUsesFixedEndpointAndProjectAllowlist(t *testing.T) {
	credentialsPath := t.TempDir() + "/gcp-credentials.json"
	if err := os.WriteFile(credentialsPath, []byte(`{"type":"authorized_user","client_id":"test-client","client_secret":"test-secret","refresh_token":"test-refresh"}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Setenv("MCP_TEST_GCP_CREDENTIALS", credentialsPath)
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	var computeRequest *http.Request
	http.DefaultTransport = providerTestRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "oauth2.googleapis.com" {
			return providerTestHTTPResponse(request, `{"access_token":"test-token","token_type":"Bearer","expires_in":3600}`), nil
		}
		if request.URL.Host != "compute.googleapis.com" {
			return nil, fmt.Errorf("unexpected GCP detail host %q", request.URL.Host)
		}
		computeRequest = request.Clone(request.Context())
		return providerTestHTTPResponse(request, `{"id":"123","name":"web-1","status":"RUNNING","zone":"https://www.googleapis.com/compute/v1/projects/project-a/zones/us-east1-a","machineType":"https://www.googleapis.com/compute/v1/projects/project-a/zones/us-east1-a/machineTypes/e2","creationTimestamp":"2026-01-02T03:04:05Z","labels":{"env":"test"}}`), nil
	})
	adapter := &gcpAdapter{
		name: "gcp-prod",
		profile: config.Profile{
			Credential: config.Credential{Source: "env", Env: map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": "MCP_TEST_GCP_CREDENTIALS"}},
			Scopes:     config.Scopes{Projects: []string{"project-a"}},
		},
	}
	page, err := adapter.NativeRead(context.Background(), provider.NativeRequest{Operation: "gcp.compute.instances.get", Params: map[string]any{"project_id": "project-a", "zone": "us-east1-a", "instance": "web-1"}})
	if err != nil {
		t.Fatalf("GCP detail NativeRead() error = %v", err)
	}
	if computeRequest == nil || computeRequest.Method != http.MethodGet || computeRequest.URL.Path != "/compute/v1/projects/project-a/zones/us-east1-a/instances/web-1" || computeRequest.URL.RawQuery != "" {
		t.Fatalf("GCP detail request = %#v, want fixed GET endpoint without arbitrary query", computeRequest)
	}
	if computeRequest.Header.Get("Authorization") != "Bearer test-token" {
		t.Fatalf("GCP detail Authorization = %q, want bearer token", computeRequest.Header.Get("Authorization"))
	}
	if len(page.Rows) != 1 || page.Rows[0]["id"] != "123" || page.Rows[0]["kind"] != "instance" || page.Rows[0]["scope"].(map[string]any)["project_id"] != "project-a" {
		t.Fatalf("GCP detail page = %#v, want normalized instance row", page)
	}
	if _, err := adapter.NativeRead(context.Background(), provider.NativeRequest{Operation: "gcp.compute.instances.get", Params: map[string]any{"project_id": "project-b", "zone": "us-east1-a", "instance": "web-1"}}); !hasProviderErrorCode(err, "scope_not_allowed") {
		t.Fatalf("GCP detail outside project allowlist error = %#v, want scope_not_allowed", err)
	}
}

func TestAzureInstanceDetailUsesFixedManagementEndpointAndSubscriptionAllowlist(t *testing.T) {
	authority := httptest.NewTLSServer(nil)
	t.Cleanup(authority.Close)
	useAzureTestAuthority(t, authority)
	t.Setenv("AZURE_AUTHORITY_HOST", authority.URL)
	t.Setenv("MCP_TEST_AZURE_TENANT", "adfs")
	t.Setenv("MCP_TEST_AZURE_CLIENT", "client")
	t.Setenv("MCP_TEST_AZURE_SECRET", "secret")
	authority.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, ".well-known/openid-configuration") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, fmt.Sprintf(`{"authorization_endpoint":%q,"issuer":%q,"token_endpoint":%q}`, authority.URL+"/adfs/v2.0", authority.URL+"/adfs/v2.0", authority.URL+"/adfs/oauth2/v2.0/token"))
			return
		}
		if strings.HasSuffix(request.URL.Path, "/oauth2/v2.0/token") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"azure-token","token_type":"Bearer","expires_in":3600}`)
			return
		}
		http.NotFound(w, request)
	})
	authorityTransport := authority.Client().Transport
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	var managementRequest *http.Request
	http.DefaultTransport = providerTestRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == strings.TrimPrefix(authority.URL, "https://") {
			return authorityTransport.RoundTrip(request)
		}
		if request.URL.Host == "login.microsoftonline.com" {
			return providerTestHTTPResponse(request, `{"access_token":"azure-token","token_type":"Bearer","expires_in":3600}`), nil
		}
		if request.URL.Host != "management.azure.com" {
			return nil, fmt.Errorf("unexpected Azure detail host %q", request.URL.Host)
		}
		managementRequest = request.Clone(request.Context())
		return providerTestHTTPResponse(request, `{"id":"/subscriptions/sub-a/resourceGroups/rg-a/providers/Microsoft.Compute/virtualMachines/web-1","name":"web-1","location":"eastasia","properties":{"provisioningState":"Succeeded","hardwareProfile":{"vmSize":"Standard_D2"},"storageProfile":{"osDisk":{"osType":"Linux"}}}}`), nil
	})
	adapter := &azureAdapter{
		name: "azure-prod",
		profile: config.Profile{
			Credential: config.Credential{Source: "env", Env: map[string]string{
				"AZURE_TENANT_ID":     "MCP_TEST_AZURE_TENANT",
				"AZURE_CLIENT_ID":     "MCP_TEST_AZURE_CLIENT",
				"AZURE_CLIENT_SECRET": "MCP_TEST_AZURE_SECRET",
			}},
			Scopes: config.Scopes{Subscriptions: []string{"sub-a"}},
		},
	}
	page, err := adapter.NativeRead(context.Background(), provider.NativeRequest{Operation: "azure.compute.virtual_machines.get", Params: map[string]any{"subscription_id": "sub-a", "resource_group": "rg-a", "name": "web-1"}})
	if err != nil {
		t.Fatalf("Azure detail NativeRead() error = %v", err)
	}
	if managementRequest == nil || managementRequest.Method != http.MethodGet {
		t.Fatalf("Azure detail request = %#v, want fixed GET endpoint", managementRequest)
	}
	wantPath := "/subscriptions/sub-a/resourceGroups/rg-a/providers/Microsoft.Compute/virtualMachines/web-1"
	if managementRequest.URL.Path != wantPath || managementRequest.URL.Query().Get("api-version") != "2024-03-01" {
		t.Fatalf("Azure detail URL = %s, want path %s and api-version 2024-03-01", managementRequest.URL, wantPath)
	}
	if managementRequest.Header.Get("Authorization") != "Bearer azure-token" {
		t.Fatalf("Azure detail Authorization = %q, want bearer token", managementRequest.Header.Get("Authorization"))
	}
	if len(page.Rows) != 1 || page.Rows[0]["id"] == "" || page.Rows[0]["state"] != "succeeded" || page.Rows[0]["scope"].(map[string]any)["subscription_id"] != "sub-a" {
		t.Fatalf("Azure detail page = %#v, want normalized instance row", page)
	}
	if _, err := adapter.NativeRead(context.Background(), provider.NativeRequest{Operation: "azure.compute.virtual_machines.get", Params: map[string]any{"subscription_id": "sub-b", "resource_group": "rg-a", "name": "web-1"}}); !hasProviderErrorCode(err, "scope_not_allowed") {
		t.Fatalf("Azure detail outside subscription allowlist error = %#v, want scope_not_allowed", err)
	}
}

func TestTencentInstanceDetailUsesFixedDescribeInstancesRequest(t *testing.T) {
	t.Setenv("MCP_TEST_TENCENT_ID", "secret-id")
	t.Setenv("MCP_TEST_TENCENT_KEY", "secret-key")
	var captured tencentDetailRequest
	previous := newTencentCVMDetailClient
	t.Cleanup(func() { newTencentCVMDetailClient = previous })
	newTencentCVMDetailClient = func(_ tencent.CredentialIface, region string) tencentDetailSender {
		if region != "ap-singapore" {
			t.Errorf("Tencent detail region = %q, want ap-singapore", region)
		}
		return tencentInstanceDetailStub{capture: &captured, body: []byte(`{"Response":{"InstanceSet":[{"InstanceId":"i-1","InstanceName":"web-1","InstanceState":"RUNNING","Placement":{"Zone":"ap-singapore-1"},"InstanceType":"S5.MEDIUM4","CPU":2,"Memory":4}]}}`)}
	}
	adapter := &tencentAdapter{
		name: "tencent-prod",
		profile: config.Profile{
			Credential: config.Credential{Source: "env", Env: map[string]string{
				"TENCENTCLOUD_SECRET_ID":  "MCP_TEST_TENCENT_ID",
				"TENCENTCLOUD_SECRET_KEY": "MCP_TEST_TENCENT_KEY",
			}},
			Scopes:  config.Scopes{Accounts: []string{"1000000001"}},
			Regions: []string{"ap-singapore"},
		},
	}
	page, err := adapter.NativeRead(context.Background(), provider.NativeRequest{Operation: "tencent.cvm.describe_instances", Region: "ap-singapore", Params: map[string]any{"instance_id": "i-1"}})
	if err != nil {
		t.Fatalf("Tencent detail NativeRead() error = %v", err)
	}
	if captured.request == nil || captured.request.GetService() != "cvm" || captured.request.GetVersion() != "2017-03-12" || captured.request.GetAction() != "DescribeInstances" {
		t.Fatalf("Tencent detail request metadata = %#v, want cvm/2017-03-12/DescribeInstances", captured.request)
	}
	commonRequest, ok := captured.request.(*tchttp.CommonRequest)
	if !ok {
		t.Fatalf("Tencent detail request type = %T, want *http.CommonRequest", captured.request)
	}
	encodedBody, err := commonRequest.MarshalJSON()
	if err != nil {
		t.Fatalf("Tencent detail request body encoding error = %v", err)
	}
	body := string(encodedBody)
	if !strings.Contains(body, `"InstanceIds":["i-1"]`) || !strings.Contains(body, `"Limit":1`) {
		t.Fatalf("Tencent detail request body = %s, want one requested ID and Limit=1", body)
	}
	if len(page.Rows) != 1 || page.Rows[0]["id"] != "i-1" || page.Rows[0]["name"] != "web-1" || page.Rows[0]["scope"].(map[string]any)["account_id"] != "1000000001" {
		t.Fatalf("Tencent detail page = %#v, want normalized instance row", page)
	}
}

func TestVolcengineInstanceDetailUsesFixedDescribeInstancesRequest(t *testing.T) {
	t.Setenv("MCP_TEST_VOLC_ACCESS", "access")
	t.Setenv("MCP_TEST_VOLC_SECRET", "secret")
	var captured *volcecs.DescribeInstancesInput
	previous := newVolcengineECSDetailClient
	t.Cleanup(func() { newVolcengineECSDetailClient = previous })
	newVolcengineECSDetailClient = func(_ *volcsession.Session) volcengineECSDetailAPI {
		return volcengineInstanceDetailStub{
			capture: &captured,
			output: &volcecs.DescribeInstancesOutput{Instances: []*volcecs.InstanceForDescribeInstancesOutput{{
				InstanceId: volc.String("i-1"), InstanceName: volc.String("web-1"), Status: volc.String("Running"), InstanceTypeId: volc.String("ecs.g1.large"),
			}}},
		}
	}
	adapter := &volcengineAdapter{
		name: "volcengine-prod",
		profile: config.Profile{
			Credential: config.Credential{Source: "env", Env: map[string]string{
				"VOLCENGINE_ACCESS_KEY_ID":     "MCP_TEST_VOLC_ACCESS",
				"VOLCENGINE_SECRET_ACCESS_KEY": "MCP_TEST_VOLC_SECRET",
			}},
			Scopes:  config.Scopes{Accounts: []string{"2000000001"}},
			Regions: []string{"cn-beijing"},
		},
	}
	page, err := adapter.NativeRead(context.Background(), provider.NativeRequest{Operation: "volcengine.ecs.describe_instances", Region: "cn-beijing", Params: map[string]any{"instance_id": "i-1"}})
	if err != nil {
		t.Fatalf("Volcengine detail NativeRead() error = %v", err)
	}
	if captured == nil || len(captured.InstanceIds) != 1 || volc.StringValue(captured.InstanceIds[0]) != "i-1" || captured.MaxResults == nil || volc.Int32Value(captured.MaxResults) != 1 {
		t.Fatalf("Volcengine DescribeInstances input = %#v, want one ID and MaxResults=1", captured)
	}
	if len(page.Rows) != 1 || page.Rows[0]["id"] != "i-1" || page.Rows[0]["name"] != "web-1" || page.Rows[0]["native"].(map[string]any)["instance_type"] != "ecs.g1.large" {
		t.Fatalf("Volcengine detail page = %#v, want normalized instance row", page)
	}
}

func TestVolcengineConsoleEvidenceNormalization(t *testing.T) {
	adapter := &volcengineAdapter{name: "volcengine-prod"}
	rdsRow := adapter.volcengineRDSDetailRow(&volcrds.DescribeDBInstanceDetailOutput{
		BasicInfo: &volcrds.BasicInfoForDescribeDBInstanceDetailOutput{
			InstanceId: volc.String("mysql-1"), InstanceName: volc.String("mysql-prod"), RegionId: volc.String("cn-shanghai"),
			Memory: volc.Int32(4), AutoUpgradeMinorVersion: volc.String("Auto"), DeletionProtection: volc.String("Enabled"),
		},
	}, "cn-beijing", "2000000001")
	rdsAttributes := rdsRow["attributes"].(map[string]any)
	if rdsAttributes["memory_mb"] != 4096 {
		t.Fatalf("Volcengine RDS memory_mb = %#v, want 4096 for provider value 4 GiB", rdsAttributes["memory_mb"])
	}
	rdsPosture := rdsAttributes["posture"].(map[string]any)
	if rdsPosture["automatic_minor_version_upgrade"] != true || rdsPosture["deletion_protection"] != true {
		t.Fatalf("Volcengine RDS posture = %#v, want console Auto/Enabled normalized true", rdsPosture)
	}

	redisRow := adapter.volcengineRedisDetailRow(&volcredis.DescribeDBInstanceDetailOutput{
		InstanceId: volc.String("redis-1"), InstanceName: volc.String("redis-prod"), RegionId: volc.String("cn-shanghai"), Status: volc.String("Running"),
		EngineVersion: volc.String("5.0"), InstanceClass: volc.String("PrimarySecondary"), ProjectName: volc.String("default"),
		Capacity:        &volcredis.CapacityForDescribeDBInstanceDetailOutput{Total: volc.Int64(1024), Used: volc.Int64(179)},
		ShardCapacityV2: volc.Int64(512), ShardNumber: volc.Int32(2), NodeNumber: volc.Int32(2), MaxConnections: volc.Int32(10000),
		MultiAZ: volc.String("disabled"), DeletionProtection: volc.String("enabled"), AutoRenew: volc.Bool(true), ShardedCluster: volc.Int32(1),
		VpcAuthMode: volc.String("close"), ZoneIds: []*string{volc.String("cn-shanghai-b")},
		VisitAddrs: []*volcredis.VisitAddrForDescribeDBInstanceDetailOutput{{Address: volc.String("must-not-leak.redis.volces.com")}},
	}, "cn-beijing", "2000000001")
	if redisRow["state"] != "running" || redisRow["kind"] != "cache" {
		t.Fatalf("Volcengine Redis identity = %#v, want running cache", redisRow)
	}
	redisAttributes := redisRow["attributes"].(map[string]any)
	if redisAttributes["memory_mb"] != int64(1024) || redisAttributes["memory_used_mb"] != int64(179) || redisAttributes["shard_memory_mb"] != int64(512) {
		t.Fatalf("Volcengine Redis memory attributes = %#v, want documented MiB values", redisAttributes)
	}
	redisPosture := redisAttributes["posture"].(map[string]any)
	if redisPosture["deletion_protection"] != true || redisPosture["automatic_renewal_enabled"] != true || redisPosture["multi_zone"] != false || redisPosture["sharded_cluster_enabled"] != true || redisPosture["password_free_access_enabled"] != false {
		t.Fatalf("Volcengine Redis posture = %#v, want normalized provider flags", redisPosture)
	}
	assertDeepDetailRowSafe(t, redisRow, "volcengine-redis", "account_id", "2000000001")

	tosRow := adapter.volcengineTOSDetailRow(&volctos.GetBucketInfoOutput{Bucket: volctos.BucketInfo{
		Name: "bucket-a", CreationDate: stableObservedTime, StorageClass: volctosenum.StorageClassStandard,
		ProjectName: "project-a", Type: volctosenum.BucketTypeFNS, Location: "cn-shanghai", AzRedundancy: volctosenum.AzRedundancySingleAz,
		Versioning: "Enabled", CrossRegionReplication: volctosenum.StatusEnabled, TransferAcceleration: volctosenum.StatusDisabled,
		AccessMonitor: volctosenum.StatusEnabled, ExtranetEndpoint: "must-not-leak.tos.volces.com",
		ServerSideEncryptionConfiguration: volctos.ServerSideEncryptionConfiguration{Rule: volctos.BucketEncryptionRule{
			ApplyServerSideEncryptionByDefault: volctos.ApplyServerSideEncryptionByDefault{SSEAlgorithm: "AES256", KMSMasterKeyID: "must-not-leak-kms"},
		}},
	}}, "cn-beijing", "2000000001")
	tosAttributes := tosRow["attributes"].(map[string]any)
	if tosRow["id"] != "bucket-a" || tosRow["region"] != "cn-shanghai" || tosAttributes["storage_class"] != "STANDARD" || tosAttributes["bucket_type"] != "fns" || tosAttributes["server_side_encryption_algorithm"] != "AES256" {
		t.Fatalf("Volcengine TOS attributes = %#v, row=%#v", tosAttributes, tosRow)
	}
	tosPosture := tosAttributes["posture"].(map[string]any)
	if tosPosture["multi_zone"] != false || tosPosture["versioning_enabled"] != true || tosPosture["cross_region_replication_enabled"] != true || tosPosture["transfer_acceleration_enabled"] != false || tosPosture["access_monitor_enabled"] != true || tosPosture["server_side_encryption_enabled"] != true {
		t.Fatalf("Volcengine TOS posture = %#v, want normalized provider flags", tosPosture)
	}
	assertDeepDetailRowSafe(t, tosRow, "volcengine-tos", "account_id", "2000000001")

	tosInventoryRow := adapter.row(&volcresourcecenter.ResourceForSearchResourcesOutput{ResourceID: volc.String("sh-sit-tos-scopedb"), Service: volc.String("tos")}, stableObservedTime)
	if tosInventoryRow["name"] != "sh-sit-tos-scopedb" {
		t.Fatalf("Volcengine TOS name = %#v, want ResourceID fallback", tosInventoryRow["name"])
	}
	if got := firstDimension(map[string]string{"ResourceID": "i-yee5a5aq68vr6on043hw"}); got != "i-yee5a5aq68vr6on043hw" {
		t.Fatalf("firstDimension(ResourceID) = %q, want ECS instance ID", got)
	}
}

func TestGCPIAMProductUsesIAMCloudAssetEndpoint(t *testing.T) {
	credentialsPath := t.TempDir() + "/gcp-credentials.json"
	credentials := `{"type":"authorized_user","client_id":"test-client","client_secret":"test-secret","refresh_token":"test-refresh"}`
	if err := os.WriteFile(credentialsPath, []byte(credentials), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Setenv("MCPCLOUD_TEST_GCP_CREDENTIALS", credentialsPath)
	oldTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	var assetRequest *http.Request
	http.DefaultTransport = providerTestRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "oauth2.googleapis.com" {
			return providerTestHTTPResponse(request, `{"access_token":"test-token","token_type":"Bearer","expires_in":3600}`), nil
		}
		if request.URL.Host != "cloudasset.googleapis.com" {
			return nil, fmt.Errorf("unexpected GCP request host %q", request.URL.Host)
		}
		assetRequest = request.Clone(request.Context())
		return providerTestHTTPResponse(request, `{"results":[{"resource":"//compute.googleapis.com/projects/project-a/zones/us-east1-a/instances/i-1","assetType":"compute.googleapis.com/Instance","project":"projects/project-a","policy":{"bindings":[{"role":"roles/viewer","members":["user:alice@example.com"]}]}}],"nextPageToken":"iam-next"}`), nil
	})
	adapter := &gcpAdapter{
		name: "gcp-prod",
		profile: config.Profile{
			Credential: config.Credential{Source: "env", Env: map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": "MCPCLOUD_TEST_GCP_CREDENTIALS"}},
			Options:    map[string]string{"asset_scope": "projects/project-a"},
		},
	}
	operation := nativeProductOperationName(model.ProviderGCP, nativeProductCatalog[4])
	page, err := adapter.NativeRead(context.Background(), provider.NativeRequest{Operation: operation, Params: map[string]any{"scope": "projects/project-a"}, Limit: 10})
	if err != nil {
		t.Fatalf("GCP IAM product NativeRead() error = %v", err)
	}
	if assetRequest == nil || assetRequest.URL.Path != "/v1/projects/project-a:searchAllIamPolicies" {
		t.Fatalf("GCP IAM request = %#v, want IAM Cloud Asset endpoint", assetRequest)
	}
	if len(page.Rows) != 1 || page.Rows[0]["domain"] != "iam" || page.Rows[0]["kind"] != "binding" || page.NextToken != "iam-next" || page.Scanned != 1 || page.Requests != 1 {
		t.Fatalf("GCP IAM product page = %#v, want one IAM binding with cursor/stats", page)
	}
}

func TestAzureIAMProductUsesAuthorizationResourcesQuery(t *testing.T) {
	authority := httptest.NewTLSServer(nil)
	t.Cleanup(authority.Close)
	useAzureTestAuthority(t, authority)
	t.Setenv("AZURE_AUTHORITY_HOST", authority.URL)
	authority.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, ".well-known/openid-configuration") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, fmt.Sprintf(`{"authorization_endpoint":%q,"issuer":%q,"token_endpoint":%q}`, authority.URL+"/adfs/v2.0", authority.URL+"/adfs/v2.0", authority.URL+"/adfs/oauth2/v2.0/token"))
			return
		}
		if strings.HasSuffix(request.URL.Path, "/oauth2/v2.0/token") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"azure-token","token_type":"Bearer","expires_in":3600}`)
			return
		}
		http.NotFound(w, request)
	})
	t.Setenv("MCPCLOUD_TEST_AZURE_TENANT", "adfs")
	t.Setenv("MCPCLOUD_TEST_AZURE_CLIENT", "client")
	t.Setenv("MCPCLOUD_TEST_AZURE_SECRET", "secret")
	oldTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	var graphBody map[string]any
	http.DefaultTransport = providerTestRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "login.microsoftonline.com" {
			return providerTestHTTPResponse(request, `{"access_token":"azure-token","token_type":"Bearer","expires_in":3600}`), nil
		}
		if request.URL.Host != "management.azure.com" {
			return nil, fmt.Errorf("unexpected Azure request host %q", request.URL.Host)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(body, &graphBody); err != nil {
			return nil, err
		}
		return providerTestHTTPResponse(request, `{"data":[{"id":"/subscriptions/sub-1/providers/Microsoft.Authorization/roleAssignments/role-1","name":"role-1","type":"Microsoft.Authorization/roleAssignments","location":"eastasia","subscriptionId":"sub-1","resourceGroup":"rg","properties":{"principalId":"principal-1","principalType":"User","roleDefinitionId":"role-definition-1","scope":"/subscriptions/sub-1"}}],"$skipToken":"azure-next","totalRecords":1}`), nil
	})
	adapter := &azureAdapter{
		name: "azure-prod",
		profile: config.Profile{
			Credential: config.Credential{Source: "env", Env: map[string]string{
				"AZURE_TENANT_ID":     "MCPCLOUD_TEST_AZURE_TENANT",
				"AZURE_CLIENT_ID":     "MCPCLOUD_TEST_AZURE_CLIENT",
				"AZURE_CLIENT_SECRET": "MCPCLOUD_TEST_AZURE_SECRET",
			}},
			Scopes: config.Scopes{Subscriptions: []string{"sub-1"}},
		},
	}
	operation := nativeProductOperationName(model.ProviderAzure, nativeProductCatalog[4])
	page, err := adapter.NativeRead(context.Background(), provider.NativeRequest{Operation: operation, Params: map[string]any{"subscriptions": []string{"sub-1"}}})
	if err != nil {
		t.Fatalf("Azure IAM product NativeRead() error = %v", err)
	}
	query, _ := graphBody["query"].(string)
	if !strings.HasPrefix(query, "AuthorizationResources") {
		t.Fatalf("Azure IAM Resource Graph query = %q, want AuthorizationResources source", query)
	}
	if len(page.Rows) != 1 || page.Rows[0]["domain"] != "iam" || page.Rows[0]["kind"] != "binding" || page.NextToken != "azure-next" || page.Scanned != 1 || page.Requests != 1 {
		t.Fatalf("Azure IAM product page = %#v, want one IAM binding with cursor/stats", page)
	}
}

func TestEscapeKQL(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   string
		want string
	}{
		{name: "backslash quote and statement marker", in: `a\'; Resources`, want: `a\\\'; Resources`},
		{name: "plain", in: `plain`, want: `plain`},
		{name: "ordinary quote", in: `O'Reilly`, want: `O\'Reilly`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := escapeKQL(tt.in); got != tt.want {
				t.Fatalf("escapeKQL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func operationNames(operations []provider.Operation) []string {
	names := make([]string, 0, len(operations))
	for _, operation := range operations {
		names = append(names, operation.Name)
	}
	return names
}

func nativeProductTestAdapters() []provider.Adapter {
	return []provider.Adapter{
		&awsAdapter{name: "aws-prod", profile: config.Profile{Provider: model.ProviderAWS}},
		&gcpAdapter{name: "gcp-prod", profile: config.Profile{Provider: model.ProviderGCP}},
		&azureAdapter{name: "azure-prod", profile: config.Profile{Provider: model.ProviderAzure}},
		&alibabaAdapter{name: "alibaba-prod", profile: config.Profile{Provider: model.ProviderAlibaba}},
		&huaweiAdapter{name: "huawei-prod", profile: config.Profile{Provider: model.ProviderHuawei}},
		&tencentAdapter{name: "tencent-prod", profile: config.Profile{Provider: model.ProviderTencent}},
		&volcengineAdapter{name: "volcengine-prod", profile: config.Profile{Provider: model.ProviderVolcengine}},
	}
}

func assertNativeProductNames(t *testing.T, adapter provider.Adapter, operations []provider.Operation) {
	t.Helper()
	seen := map[string]bool{}
	for _, operation := range operations {
		if _, ok := nativeProductFor(adapter.Provider(), operation.Name); !ok {
			continue
		}
		if seen[operation.Name] {
			t.Fatalf("%s has duplicate product operation %q", adapter.Provider(), operation.Name)
		}
		seen[operation.Name] = true
	}
	if len(seen) != len(nativeProductCatalog) {
		t.Fatalf("%s product operation count = %d, want %d", adapter.Provider(), len(seen), len(nativeProductCatalog))
	}
	for _, spec := range nativeProductCatalog {
		name := nativeProductOperationName(adapter.Provider(), spec)
		if !seen[name] {
			t.Errorf("%s missing product operation %q", adapter.Provider(), name)
		}
	}
}

func detailParameterValue(name string) string {
	switch name {
	case "project_id":
		return "project-a"
	case "zone":
		return "us-east1-a"
	case "subscription_id":
		return "sub-a"
	case "resource_group":
		return "rg-a"
	case "server_id":
		return "server-a"
	case "instance":
		return "web-1"
	default:
		return "i-1"
	}
}

func deepParameterValue(name string) string {
	switch name {
	case "db_instance_identifier", "db_instance_id", "instance", "instance_id":
		return "db-1"
	case "project_id":
		return "project-a"
	case "location":
		return "us-east1"
	case "cluster", "cluster_id", "name":
		return "cluster-a"
	case "subscription_id":
		return "sub-a"
	case "resource_group":
		return "rg-a"
	default:
		return "value"
	}
}

func cloneStringAny(values map[string]any) map[string]any {
	clone := make(map[string]any, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

func capabilityForSource(capabilities []provider.Capability, source model.Source) provider.Capability {
	for _, capability := range capabilities {
		if capability.Source == source {
			return capability
		}
	}
	return provider.Capability{}
}

func assertDetailRowSafe(t *testing.T, row map[string]any, providerName, scopeKey, scopeValue string) {
	t.Helper()
	encoded, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal %s detail row: %v", providerName, err)
	}
	lower := strings.ToLower(string(encoded))
	for _, forbidden := range []string{"userdata", "customdata", "metadata", "admin", "password", "console", "vnc", "arbitrary"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("%s detail row contains forbidden payload key %q: %s", providerName, forbidden, encoded)
		}
	}
	scope, ok := row["scope"].(map[string]any)
	if !ok || scope[scopeKey] != scopeValue {
		t.Errorf("%s detail scope = %#v, want %s=%q", providerName, row["scope"], scopeKey, scopeValue)
	}
}

func assertDeepDetailRowSafe(t *testing.T, row map[string]any, providerName, scopeKey, scopeValue string) {
	t.Helper()
	encoded, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal %s deep detail row: %v", providerName, err)
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal %s deep detail row: %v", providerName, err)
	}
	forbidden := map[string]bool{
		"password": true, "connectionstring": true, "endpoint": true, "kubeconfig": true,
		"certificate": true, "token": true, "secret": true, "userdata": true, "customdata": true, "metadata": true,
		"owner": true, "kmsmasterkeyid": true,
	}
	var walk func(any)
	walk = func(value any) {
		switch item := value.(type) {
		case map[string]any:
			for key, nested := range item {
				normalized := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(key))
				if forbidden[normalized] {
					t.Errorf("%s deep detail row contains forbidden payload key %q: %s", providerName, key, encoded)
				}
				walk(nested)
			}
		case []any:
			for _, nested := range item {
				walk(nested)
			}
		}
	}
	walk(decoded)
	scope, ok := row["scope"].(map[string]any)
	if !ok || scope[scopeKey] != scopeValue {
		t.Errorf("%s deep detail scope = %#v, want %s=%q", providerName, row["scope"], scopeKey, scopeValue)
	}
}

type awsInstanceDetailStub struct {
	capture **ec2.DescribeInstancesInput
	output  *ec2.DescribeInstancesOutput
	err     error
}

type awsRDSDeepStub struct {
	capture **rds.DescribeDBInstancesInput
}

func (s awsRDSDeepStub) DescribeDBInstances(_ context.Context, input *rds.DescribeDBInstancesInput, _ ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error) {
	*s.capture = input
	identifier := awsbase.ToString(input.DBInstanceIdentifier)
	return &rds.DescribeDBInstancesOutput{DBInstances: []rdstypes.DBInstance{{DBInstanceIdentifier: awsbase.String(identifier)}}}, nil
}

type awsEKSDeepStub struct {
	capture **eks.DescribeClusterInput
}

func (s awsEKSDeepStub) DescribeCluster(_ context.Context, input *eks.DescribeClusterInput, _ ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
	*s.capture = input
	name := awsbase.ToString(input.Name)
	return &eks.DescribeClusterOutput{Cluster: &ekstypes.Cluster{Name: awsbase.String(name)}}, nil
}

func (s awsInstanceDetailStub) DescribeInstances(_ context.Context, input *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	*s.capture = input
	return s.output, s.err
}

type alibabaInstanceDetailStub struct {
	capture  **aliecs.DescribeInstanceAttributeRequest
	response *aliecs.DescribeInstanceAttributeResponse
	err      error
}

type alibabaRDSDeepStub struct {
	capture **alirds.DescribeDBInstanceAttributeRequest
}

func (s alibabaRDSDeepStub) DescribeDBInstanceAttribute(request *alirds.DescribeDBInstanceAttributeRequest) (*alirds.DescribeDBInstanceAttributeResponse, error) {
	*s.capture = request
	return nil, fmt.Errorf("stub Alibaba RDS")
}

type alibabaCSDeepStub struct {
	capture **alics.DescribeClusterDetailRequest
}

func (s alibabaCSDeepStub) DescribeClusterDetail(request *alics.DescribeClusterDetailRequest) (*alics.DescribeClusterDetailResponse, error) {
	*s.capture = request
	return nil, fmt.Errorf("stub Alibaba CS")
}

func (s alibabaInstanceDetailStub) DescribeInstanceAttribute(request *aliecs.DescribeInstanceAttributeRequest) (*aliecs.DescribeInstanceAttributeResponse, error) {
	*s.capture = request
	return s.response, s.err
}

type tencentDetailRequest struct {
	request tchttp.Request
}

type tencentInstanceDetailStub struct {
	capture *tencentDetailRequest
	body    []byte
	err     error
}

func assertTencentDeepRequest(t *testing.T, request tchttp.Request, service, version, action, bodyNeedle string) {
	t.Helper()
	if request == nil || request.GetService() != service || request.GetVersion() != version || request.GetAction() != action {
		t.Fatalf("Tencent request = %#v, want %s/%s/%s", request, service, version, action)
	}
	common, ok := request.(*tchttp.CommonRequest)
	if !ok {
		t.Fatalf("Tencent request type = %T, want *http.CommonRequest", request)
	}
	body, err := common.MarshalJSON()
	if err != nil {
		t.Fatalf("Tencent request body encoding error = %v", err)
	}
	if !strings.Contains(string(body), bodyNeedle) || !strings.Contains(string(body), `"Limit":1`) {
		t.Fatalf("Tencent request body = %s, want %s and Limit=1", body, bodyNeedle)
	}
}

func (s tencentInstanceDetailStub) Send(request tchttp.Request, response tchttp.Response) error {
	s.capture.request = request
	if s.err != nil {
		return s.err
	}
	commonResponse, ok := response.(*tchttp.CommonResponse)
	if !ok {
		return fmt.Errorf("unexpected Tencent response type %T", response)
	}
	return commonResponse.UnmarshalJSON(s.body)
}

type volcengineInstanceDetailStub struct {
	capture **volcecs.DescribeInstancesInput
	output  *volcecs.DescribeInstancesOutput
	err     error
}

type volcengineRDSDeepStub struct {
	capture **volcrds.DescribeDBInstanceDetailInput
	err     error
}

func (s volcengineRDSDeepStub) DescribeDBInstanceDetailWithContext(_ volc.Context, input *volcrds.DescribeDBInstanceDetailInput, _ ...volcrequest.Option) (*volcrds.DescribeDBInstanceDetailOutput, error) {
	*s.capture = input
	return nil, s.err
}

type volcengineRedisDeepStub struct {
	capture **volcredis.DescribeDBInstanceDetailInput
	err     error
}

func (s volcengineRedisDeepStub) DescribeDBInstanceDetailWithContext(_ volc.Context, input *volcredis.DescribeDBInstanceDetailInput, _ ...volcrequest.Option) (*volcredis.DescribeDBInstanceDetailOutput, error) {
	*s.capture = input
	return nil, s.err
}

type volcengineVKEDetailDeepStub struct {
	capture         **volcvke.ListClustersInput
	nodePoolCapture **volcvke.ListNodePoolsInput
	nodeCapture     **volcvke.ListNodesInput
	nodePoolOutput  *volcvke.ListNodePoolsOutput
	nodeOutput      *volcvke.ListNodesOutput
	err             error
}

type volcengineTOSDetailDeepStub struct {
	capture **volctos.GetBucketInfoInput
	err     error
}

func (s *volcengineTOSDetailDeepStub) GetBucketInfo(_ context.Context, input *volctos.GetBucketInfoInput) (*volctos.GetBucketInfoOutput, error) {
	*s.capture = input
	return nil, s.err
}

func (s *volcengineTOSDetailDeepStub) Close() {}

func (s volcengineVKEDetailDeepStub) ListClustersWithContext(_ volc.Context, input *volcvke.ListClustersInput, _ ...volcrequest.Option) (*volcvke.ListClustersOutput, error) {
	*s.capture = input
	return nil, s.err
}

func (s volcengineVKEDetailDeepStub) ListNodePoolsWithContext(_ volc.Context, input *volcvke.ListNodePoolsInput, _ ...volcrequest.Option) (*volcvke.ListNodePoolsOutput, error) {
	if s.nodePoolCapture != nil {
		*s.nodePoolCapture = input
	}
	return s.nodePoolOutput, s.err
}

func (s volcengineVKEDetailDeepStub) ListNodesWithContext(_ volc.Context, input *volcvke.ListNodesInput, _ ...volcrequest.Option) (*volcvke.ListNodesOutput, error) {
	if s.nodeCapture != nil {
		*s.nodeCapture = input
	}
	return s.nodeOutput, s.err
}

func (s volcengineInstanceDetailStub) DescribeInstancesWithContext(_ volc.Context, input *volcecs.DescribeInstancesInput, _ ...volcrequest.Option) (*volcecs.DescribeInstancesOutput, error) {
	*s.capture = input
	return s.output, s.err
}

func requireOperation(t *testing.T, operations []provider.Operation, name string) provider.Operation {
	t.Helper()
	for _, operation := range operations {
		if operation.Name == name {
			return operation
		}
	}
	t.Fatalf("operation %q not registered in %#v", name, operationNames(operations))
	return provider.Operation{}
}

func envCredentialProfile(env map[string]string) config.Profile {
	return config.Profile{Credential: config.Credential{Source: "env", Env: env}}
}

type providerTestRoundTripper func(*http.Request) (*http.Response, error)

func (f providerTestRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

var stableObservedTime = mustParseProviderTestTime("2026-01-02T03:04:05Z")

func mustParseProviderTestTime(value string) (resultTime time.Time) {
	resultTime, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return resultTime
}

func asProviderError(err error, target **provider.Error) bool {
	if err == nil {
		return false
	}
	value, ok := err.(*provider.Error)
	if ok {
		*target = value
	}
	return ok
}

func hasProviderErrorCode(err error, code string) bool {
	var providerErr *provider.Error
	return asProviderError(err, &providerErr) && providerErr.Code == code
}

func requireDeepProviderError(t *testing.T, err error, operation, code string) {
	t.Helper()
	var providerErr *provider.Error
	if !asProviderError(err, &providerErr) || providerErr.Code != code || providerErr.Operation != operation {
		t.Fatalf("error = %#v, want provider error code=%q operation=%q", err, code, operation)
	}
}

func providerTestHTTPResponse(request *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}
