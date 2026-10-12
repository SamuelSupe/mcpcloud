# Azure/GCP candidate delivery and rollback

This branch combines the validated Azure/GCP candidate with delivery baseline `eb600d6cacfde14454d746327fea1937300690fc`. It remains a local review candidate. The original deployed binary/configuration is retained; no push or production replacement is implied by this document.

## Build and verification

Use Go 1.26.5 and a clean checkout of the integration commit:

```powershell
go test ./...
go build -trimpath -buildvcs=false -o mcpcloud.azure-gcp-integration.exe ./cmd/mcpcloud
```

The explicit flags permit binary hash comparison between clean checkouts on the same OS/architecture and toolchain. The earlier validation binaries used default build flags and a different source baseline; their hashes are not expected to match the integration build.

## Configuration and startup

Copy the separately reviewed validation configuration into an isolated candidate configuration. Keep resource scope restricted to the approved subscription/project, use a distinct loopback listen port, and set `allowed_hosts` to that port. Actual validated credentials remain in external files or environment variables; never commit them or include them in an artifact package.

Azure uses the configured Entra identity through `AZURE_TENANT_ID`, `AZURE_CLIENT_ID`, and `AZURE_CLIENT_SECRET`. GCP uses the external file referenced by `GOOGLE_APPLICATION_CREDENTIALS`. Load the MCP bearer through the configured `auth_token_env` (`MCPCLOUD_AUTH_TOKEN` in the local validation configuration).

```powershell
./mcpcloud.azure-gcp-integration.exe serve --transport http --config ./config.azure-gcp.integration.yaml
```

Use the local credential-loading launcher when operating in the existing validation workspace; the artifact package deliberately excludes that launcher's referenced credentials. Discover the running candidate's operations with `cloud_schema`. Avoid pointing a desktop client at the candidate until startup and intended profile scope have been checked.

For GCP costs, configure the standard export table separately from the query-job project:

```yaml
options:
  billing_table: billingexport-438818.billing_data.gcp_billing_export_v1_01FBDE_1AC342_AE7E4E
  billing_project: studio-storage-integration
  billing_location: US
```

The provider filters costs to the configured business project. This SQL filter does not create row-level IAM isolation on the source dataset. Keep access to the export dataset under the administrator's intended boundary.

## Required permission boundaries

Use the already validated read identity instead of Owner credentials for runtime queries. Inventory/metrics and named service reads require their corresponding read permissions in the configured scope. The additional tested boundaries are:

- Azure Blob object listing: data-plane read/list access to the selected container, separate from ARM management Reader access. No storage account keys are needed by MCPCloud.
- Azure AKS: permission to obtain the cluster-user Entra configuration and Kubernetes/RBAC read permissions for actual Nodes and Pods in the selected cluster. Local/admin credentials and exec plugin execution are rejected.
- GCP GKE: cluster metadata read and Kubernetes Node/Pod list access. The current implementation requires the tested public IP endpoint and validates the cluster CA.
- GCP backend health: read access to the selected BackendService; the fixed POST `getHealth` is a read operation, not a resource mutation.
- GCP billing: dataset/table read on the configured export and query-job creation in `billing_project`. No runtime Billing Account Administrator role is required.

Do not broaden IAM merely because a read returns no rows. Check provider-native data, exact target, location and operation first.

## Acceptance scope and known limits

Live verification covers both clouds' test inventories, network details, actual Nodes/Pods, scoped Pod continuation, objects and selected monitoring windows. GCP real cost data is reconciled to the export table. Two existing GCP buckets provide nonzero capacity metrics; the newly created validation bucket still has no provider-native capacity series. The approved project has no global BackendService for live global health/detail acceptance; those paths are covered by unit tests only.

Azure's current classic Sponsorship subscription does not support the selected cost API and remains outside this delivery's live cost acceptance at the user's request. Other products, dimensions, private GKE endpoints, application-level health and invoice PDFs are not claimed by these checks.

Pod lists accept `page_size` up to 100. Return the cursor through `cloud_native_read` with identical profile, operation, region, params and page size. Expired snapshots must restart from the first page. Environment variables, Secret references, annotations and raw Pod specs are excluded from output.

## Rollback

The current side-by-side procedure needs no replacement rollback: stop only the candidate process and point the client back to the retained endpoint/configuration. Identify the candidate by its exact executable path and listen port before stopping it.

If a later replacement is explicitly approved, first preserve the old binary, configuration and hashes together. On rollback restore that pair and its original startup command; do not remove cloud test resources or alter IAM as part of a binary rollback. Cleanup of the temporary resources/roles is a separate operation.
