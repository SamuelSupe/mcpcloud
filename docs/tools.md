# MCP tool reference

The MCP layer exposes six read-only tools. The exact JSON schema advertised by a running binary is authoritative; the examples below match the current CLI and `internal/mcpserver` tool handlers. `cloud_help` provides structured guidance without a cloud call. Resource and IAM queries dispatch to the seven inventory adapters, including 63 product-level native operations (seven providers × nine shared suffixes). The same native tool also exposes seven service-level compute-instance detail operations, 16 managed database/cache/Kubernetes/storage detail operations, and two Volcengine VKE topology list operations. `metrics` and `costs` dispatch through the seven read-only DSL connectors described in [docs/providers.md](providers.md). Connector/configuration status is not proof of live credentials, permissions, or data.

## `cloud_help`

`cloud_help` returns structured, provider-aware guidance before using the other read-only tools. Its input is strictly `{topic?, provider?, source?}`; no `query`, `operation`, `params`, `cursor`, credentials, URL, or provider action is accepted as part of the help contract. `topic` is optional and accepts only `quickstart`, `dsl`, `profiles`, `resources`, `iam`, `metrics`, `costs`, `native`, `pagination`, `errors`, or `security`. An omitted or blank topic returns the `index`. `provider` and `source` are optional filters for the guidance and generated examples; they do not authorize, select, or execute a cloud request. Invalid topic/provider/source values return a safe error.

The returned `HelpOutput` has these meanings:

| Field | Meaning |
| --- | --- |
| `topic` | Selected topic, or `index` when topic is empty. |
| `summary` | Short explanation of the selected topic. |
| `topics` | Topic catalog, populated by the index response. |
| `recommended_flow` | Ordered `{tool, purpose, arguments}` guidance steps. The index and quickstart flow is `cloud_profiles` → `cloud_schema` → `cloud_explain` → `cloud_query`. |
| `rules` | Read-only, DSL, scope, pagination, or security rules for the topic. |
| `examples` | Named `{tool, arguments}` examples using placeholders where a runtime value is required. |
| `related_topics` | Topic names for the next relevant help page. |

`cloud_help` itself does not access a cloud API. Its text and examples are guidance, not live readiness evidence. Minimal MCP inputs are:

```json
{}
```

```json
{"topic":"metrics","provider":"aws"}
```

```json
{"topic":"native"}
```

Metrics and costs remain DSL-only sources. The `metrics` and `costs` help topics show only restricted DSL selector examples or resource-dimension placeholders and prerequisites; they do not accept original-provider query language or arbitrary actions. The `native` topic is only a fallback when normalized DSL fields are insufficient: first call `cloud_schema`, select an operation already registered in `cloud_schema.operations`, and use exactly its declared `params`. Never copy the placeholder `OPERATION_FROM_CLOUD_SCHEMA` or invent parameters. Dynamic operation availability remains authoritative in `cloud_schema.operations`.

The help guidance also preserves the pagination and safety boundaries: `cloud_query` continuation accepts only the opaque, short-lived cursor returned by a prior result; native inventory cursors are passed back only as returned; fixed service-detail operations do not paginate. Do not put credentials, tokens, private keys, passwords, connection strings, user data, object/database/log/message bodies, kubeconfigs, certificates, secrets, arbitrary URLs/actions, or complete SDK responses into help input or examples. `cloud_help`, `cloud_profiles`, `cloud_schema`, and `cloud_explain` do not call cloud APIs.

## `cloud_query`

Use the DSL as the primary query interface. The first request contains a `query` and may set a bounded `page_size` and `timeout_ms`:

```json
{
  "query": "resources | scope provider in (\"aws\") | where domain == \"compute\" | fields provider, profile, region, id, name | limit 25",
  "page_size": 25,
  "timeout_ms": 30000
}
```

The result contains `query_id`, `status`, `columns`, `rows`, `coverage`, `errors`, `stats`, and an optional `next_cursor`. `status` is `ok`, `partial`, or `failed`. A partial result preserves successful rows and returns a safe per-target error with provider/profile/region, code, operation when known, and retryability.

For a subsequent page, send the opaque cursor according to the running tool schema:

```json
{"cursor":"<short-lived-server-token>"}
```

The cursor is single-use, bounded by TTL, and held in process memory. It becomes invalid after expiry or process restart. It is not a provider token and must not be treated as one.

## `cloud_explain`

`cloud_explain` accepts the same DSL query and performs parse, validation, target expansion, and capability planning without a cloud API call:

```json
{
  "query": "costs | scope profile in (\"aws-prod\") | range last 168h | summarize sum(amount) by currency"
}
```

The result identifies `source`, target `profiles`, concrete `targets` (profile/provider/optional scope/region), the filter, requested fields, limit, capabilities, and any `capability_errors`. `pushdown` and `residual` identify source/provider target planning versus local stages when present. Region semantics are source-specific: Azure subscription metrics require an exact region and pass it as the provider `region` parameter; GCP IAM/metrics do not fan out by region; and all costs use one global/account/project-level call per expanded target, with explicit DSL region constraints applied to normalized rows locally. Filter expressions are residual and evaluated after normalization.

## `cloud_schema`

This no-credential tool returns the supported providers, sources (`resources`, `iam`, `metrics`, `costs`), domains, resource kinds, expression operators, the complete DSL field directory, pipeline stages, and native field allow-lists. `fields` is a union directory for all sources; source applicability comes from the capability metadata and the source-aware DSL validator. IAM-only fields are `principal`, `principal_type`, `resource_id`, `roles`, `actions`, `effect`, and `condition`; metrics-only fields are `metric`, `timestamp`, `value`, `unit`, and `dimensions`; costs-only fields are `date`, `amount`, and `currency`. Cross-source references are rejected before explain/query. `native_fields` is generated from the shared `model.NativeFields` map that the DSL validator uses; it is the authoritative per-provider allow-list for `native.*` references. It separates three capability/operation views:

- `capability_matrix`: static seven-provider by four-source compiled baseline. It describes connectors built into the binary (`inventory` for resource/IAM metadata and `available` for metric/cost connector classes), not profile readiness or live access.
- `profile_capabilities`: capabilities for currently configured profiles. Each entry includes `profile` and can report `not_configured` or provider-specific notes for missing scopes/options such as exact regions, project scopes, or billing settings.
- `operations`: deduplicated typed read operations registered by the currently configured profile adapters. With all seven adapters configured, this includes the 63 product-level operations, seven compute-instance detail operations, and 16 managed database/cache/Kubernetes/storage detail operations documented below, in addition to the base inventory operations. It is not a static promise for every provider; an operation must be present here before `cloud_native_read` can call it. The optional `source`, `provider`, and `kind` inputs filter capability views; `provider` filters the operation list.

The result is the source of truth for the installed build and account configuration. Resource capabilities are marked `inventory` (metadata coverage, not comprehensive service coverage); metrics and costs are marked `available` or `not_configured` with an explanatory note. Neither the static baseline nor profile capabilities proves live credentials, permissions, service enablement, or data; inspect the notes and confirm with a narrow query.

The DSL requires `include native` before a `native.*` path can be used in `fields`, `where`, `sort`, or `summarize`. The path must match the shared `native_fields` allow-list; unknown or deeper paths such as `native.password`, `native.unknown`, or `native.foo.bar` are rejected during parsing/validation. This gate applies to DSL expressions and projections independently of the registered `cloud_native_read` operation schemas.

## `cloud_profiles`

This no-credential tool returns configured profile names, provider IDs (`aws`, `gcp`, `azure`, `alibaba`, `huawei`, `tencent`, `volcengine`), scopes, regions, and readiness/status. An optional `provider` input filters the returned profile statuses. It must not return credential values, environment contents, private keys, SDK responses, or raw provider errors. A profile can be configured but not ready because credentials, permissions, provider-side inventory services, or representative resources are missing.

## `cloud_native_read`

Use this only when a normalized DSL field is insufficient and `cloud_schema` lists the operation. The request identifies a profile, a registered operation, bounded parameters, a top-level region, and page size. For example, the AWS product operation has no provider parameters; its region is the top-level `region` field:

```json
{
  "profile": "aws-prod",
  "operation": "aws.compute.list_instances",
  "region": "ap-southeast-1",
  "params": {},
  "page_size": 25
}
```

The operation name and parameter shape are selected from the registry. Arbitrary URLs, HTTP methods, API action names, SDK method names, provider-native query strings, and write operations are rejected before a provider call. The 63 product-level operation names and their exact `params` schemas are:

Volcengine inventory operations apply an exact top-level `region` as a Resource Center filter. Resource Center rejects `ProjectName` and `Service` as API filter keys, so `params.project_name` and the internal product service selector are applied to each normalized provider page; callers must continue the returned cursor to prove a complete project result. The legacy `params.region` filter remains supported; when both regions differ, the request is rejected before any provider call.

| Provider | Product-level operation names | `params` schema for each listed operation |
| --- | --- | --- |
| AWS | `aws.compute.list_instances`, `aws.compute.list_disks`, `aws.storage.list_buckets`, `aws.network.list_resources`, `aws.iam.list_resources`, `aws.database.list_resources`, `aws.kubernetes.list_resources`, `aws.monitoring.list_alarms`, `aws.logging.list_resources` | No parameters. `region` is the top-level native-read region. |
| GCP | `gcp.compute.list_instances`, `gcp.compute.list_disks`, `gcp.storage.list_buckets`, `gcp.network.list_resources`, `gcp.iam.list_resources`, `gcp.database.list_resources`, `gcp.kubernetes.list_resources`, `gcp.monitoring.list_alarms`, `gcp.logging.list_resources` | `scope`: string |
| Azure | `azure.compute.list_instances`, `azure.compute.list_disks`, `azure.storage.list_buckets`, `azure.network.list_resources`, `azure.iam.list_resources`, `azure.database.list_resources`, `azure.kubernetes.list_resources`, `azure.monitoring.list_alarms`, `azure.logging.list_resources` | `subscriptions`: array of strings; `resource_group`: string; `name`: string; `location`: string |
| Alibaba Cloud | `alibaba.compute.list_instances`, `alibaba.compute.list_disks`, `alibaba.storage.list_buckets`, `alibaba.network.list_resources`, `alibaba.iam.list_resources`, `alibaba.database.list_resources`, `alibaba.kubernetes.list_resources`, `alibaba.monitoring.list_alarms`, `alibaba.logging.list_resources` | `view`: string; `resource_id`: string; `resource_name`: string; `resource_group_id`: string; `region`: string |
| Huawei Cloud | `huawei.compute.list_instances`, `huawei.compute.list_disks`, `huawei.storage.list_buckets`, `huawei.network.list_resources`, `huawei.iam.list_resources`, `huawei.database.list_resources`, `huawei.kubernetes.list_resources`, `huawei.monitoring.list_alarms`, `huawei.logging.list_resources` | `region`: string; `id`: string; `name`: string; `enterprise_project_id`: string |
| Tencent Cloud | `tencent.compute.list_instances`, `tencent.compute.list_disks`, `tencent.storage.list_buckets`, `tencent.network.list_resources`, `tencent.iam.list_resources`, `tencent.database.list_resources`, `tencent.kubernetes.list_resources`, `tencent.monitoring.list_alarms`, `tencent.logging.list_resources` | `view_id`: string; `resource_id`: string; `resource_alias`: string; `region`: string; `zone`: string; `vpc_id`: string; `subnet_id`: string |
| Volcengine | `volcengine.compute.list_instances`, `volcengine.compute.list_disks`, `volcengine.storage.list_buckets`, `volcengine.network.list_resources`, `volcengine.iam.list_resources`, `volcengine.database.list_resources`, `volcengine.kubernetes.list_resources`, `volcengine.monitoring.list_alarms`, `volcengine.logging.list_resources` | `resource_id`: string; `region`: string; `project_name`: string |

All declared parameters are typed filters; omitted parameters use the provider/profile defaults where supported, and undeclared parameters are rejected. The shared suffix semantics are: `compute.list_instances` (instance), `compute.list_disks` (disk), `storage.list_buckets` (bucket), `network.list_resources` (network, subnet, security group, public IP, or load balancer), `iam.list_resources` (user, role, service account, policy, or binding), `database.list_resources` (database or cache), `kubernetes.list_resources` (cluster or node pool), `monitoring.list_alarms` (alarm), and `logging.list_resources` (log project, group, index, or sink).

These are inventory/resource-graph-backed read-only control-plane metadata operations, not service-detail SDK operations. Each product operation delegates to the provider's fixed inventory backend and filters normalized rows for its domain/kinds after the provider returns a page. Therefore a response can contain an empty `rows` array while `next_cursor` is non-empty; continue with that cursor to scan the next provider page. The request remains constrained by the configured profile scope, registered operation, and declared parameter types/allow-list. Unknown profiles, unregistered operations, oversized pages, and provider errors are returned as safe errors. Metrics and costs are DSL-only connectors and intentionally have no `cloud_native_read` operation.

### Service-level compute instance detail operations

These seven operations are distinct from the 63 inventory product operations. Each accepts only the required typed parameters below, performs one fixed provider compute read, and returns at most one normalized compute row. The top-level `region` is part of the `cloud_native_read` input; it is not a provider `params` key unless listed below. A detail operation rejects unknown parameters, requires its listed fields, and rejects a continuation `cursor`; it is not a list or pagination surface.

| Provider | Exact operation name | Required `params` | Scope and region rule |
| --- | --- | --- | --- |
| AWS | `aws.ec2.describe_instance` | `instance_id`: string | Top-level `region` must be exact; profile must contain exactly one `scopes.accounts` entry. |
| GCP | `gcp.compute.instances.get` | `project_id`: string; `zone`: string; `instance`: string | `project_id` must be in the profile project allowlist; `zone` is required. |
| Azure | `azure.compute.virtual_machines.get` | `subscription_id`: string; `resource_group`: string; `name`: string | `subscription_id` must be in the profile subscription allowlist. |
| Alibaba Cloud | `alibaba.ecs.describe_instance_attribute` | `instance_id`: string | Top-level `region` must be exact; profile must contain exactly one `scopes.accounts` entry. |
| Huawei Cloud | `huawei.ecs.show_server` | `project_id`: string; `server_id`: string | `project_id` must be in the profile project allowlist; top-level `region` must be exact. |
| Tencent Cloud | `tencent.cvm.describe_instances` | `instance_id`: string | Top-level `region` must be exact; profile must contain exactly one `scopes.accounts` entry. |
| Volcengine | `volcengine.ecs.describe_instances` | `instance_id`: string | Top-level `region` must be exact; profile must contain exactly one `scopes.accounts` entry. |

Example with a project-scoped provider:

```json
{
  "profile": "gcp-prod",
  "operation": "gcp.compute.instances.get",
  "params": {
    "project_id": "example-project",
    "zone": "asia-southeast1-a",
    "instance": "web-1"
  }
}
```

The normalized detail row contains common identity/scope/state fields and only provider-supplied stable compute attributes. The documented attribute categories are `instance_type`, CPU and memory when supplied, image, network/VPC/subnet and private/public IPs, security-group information when supplied, OS, and billing mode. Provider-specific normalized keys may be absent when the response does not provide that value; the implementation never falls back to arbitrary metadata. UserData/customData, arbitrary metadata, admin/password fields, console/VNC material, credentials, and other data-plane or sensitive fields are excluded, and a complete SDK response is never serialized.

### Managed database, cache, Kubernetes, and storage service-level detail operations

These 16 operations are distinct from the 63 inventory product operations and the seven compute-instance detail operations. Each performs one fixed provider service-level read for one named managed database, cache instance, Kubernetes cluster, or storage bucket and returns at most one normalized metadata row. The public operation names and parameter schemas below are the exact registry contract; each listed parameter is a required string and no other parameter is accepted.

| Provider | Managed service operation, fixed read API, and `params` | Kubernetes operation, fixed read API, and `params` | Scope and region rule |
| --- | --- | --- | --- |
| AWS | `aws.rds.describe_db_instance` → RDS `DescribeDBInstances`; `db_instance_identifier`: string | `aws.eks.describe_cluster` → EKS `DescribeCluster`; `name`: string | Top-level `region` must be exact; profile must contain exactly one account. |
| GCP | `gcp.sql.instances.get` → Cloud SQL Admin `instances.get`; `project_id`: string, `instance`: string | `gcp.container.clusters.get` → GKE Container `clusters.get`; `project_id`: string, `location`: string, `cluster`: string | `project_id` must be in the profile project allowlist. GKE `location` is required and validated; these operations do not require an exact top-level region or account. |
| Azure | `azure.dbforpostgresql.flexible_servers.get` → ARM `Microsoft.DBforPostgreSQL/flexibleServers` GET (`api-version=2025-08-01`); `subscription_id`: string, `resource_group`: string, `name`: string | `azure.containerservice.managed_clusters.get` → ARM `Microsoft.ContainerService/managedClusters` GET (`api-version=2025-10-01`); `subscription_id`: string, `resource_group`: string, `name`: string | `subscription_id` must be in the profile subscription allowlist; no exact top-level region or account is required by these adapters. |
| Alibaba Cloud | `alibaba.rds.describe_db_instance_attribute` → RDS `DescribeDBInstanceAttribute`; `db_instance_id`: string | `alibaba.cs.describe_cluster_detail` → Container Service `DescribeClusterDetail`; `cluster_id`: string | Top-level `region` must be exact; profile must contain exactly one account. |
| Huawei Cloud | `huawei.rds.list_instances` → RDS `ListInstances` with the `instance_id` ID filter; `project_id`: string, `instance_id`: string | `huawei.cce.show_cluster` → CCE `ShowCluster`; `project_id`: string, `cluster_id`: string | `project_id` must be in the profile project allowlist; top-level `region` must be exact. |
| Tencent Cloud | `tencent.cdb.describe_db_instances` → TencentDB `DescribeDBInstances` with `InstanceIds:[instance_id]` and `Limit:1`; `instance_id`: string | `tencent.tke.describe_cluster` → TKE `DescribeClusters` with `ClusterIds:[cluster_id]` and `Limit:1`; `cluster_id`: string | Top-level `region` must be exact; profile must contain exactly one account. |
| Volcengine | `volcengine.rdsmysql.describe_db_instance_detail` → RDS MySQL `DescribeDBInstanceDetail`; `instance_id`: string. `volcengine.redis.describe_db_instance_detail` → Cache for Redis `DescribeDBInstanceDetail`; `instance_id`: string. `volcengine.tos.get_bucket_info` → TOS `GetBucketInfo` without object listing; `bucket_name`: string | `volcengine.vke.list_clusters` → VKE `ListClusters` with `Filter.Ids:[cluster_id]`, page 1, size 1; `cluster_id`: string | Top-level `region` must be exact; profile must contain exactly one account. |

The request schema is closed: undeclared parameters and a continuation `cursor` are rejected. These are fixed read APIs, not arbitrary SDK method, action, URL, HTTP path, or provider-query injection. They make one provider request and do not issue recursive or follow-up graph queries, so there is no continuation cursor. `attributes.posture` is an allow-listed map of provider-supplied posture values; keys are provider-specific and optional, not a universal field set. In particular, GCP Cloud SQL uses `customer_managed_encryption_key_enabled`, while AWS RDS `logging_enabled` and `audit_enabled` are emitted only when the primary response contains CloudWatch log-export entries, with the latter requiring an audit export. `attributes.related` contains only unique identifiers taken from the primary response, with a maximum of 100 identifiers per key.

The normalized result never returns a password, connection string, database endpoint, storage endpoint, bucket owner, KMS key ID, kubeconfig, certificate, token, secret, `user_data`, logs, database rows, message/body, or a complete SDK response. Provider-supplied stable identity, state, network, billing, version, and posture metadata may be absent when the primary response does not provide it. The 16 managed database/cache/Kubernetes/storage detail operations are live-unverified in this repository; live acceptance remains blocked / not started. Existing 63 inventory operations and seven compute-instance detail operations remain compatible. Metrics and costs remain DSL-only and are not registered native operations.

### Volcengine VKE topology list operations

`volcengine.vke.list_node_pools` and `volcengine.vke.list_nodes` call the fixed VKE `ListNodePools` and `ListNodes` APIs. Both require only `params.cluster_id`, an exact top-level `region`, and exactly one profile account. The cluster ID is pushed into `Filter.ClusterIds`; results are paginated through the normal opaque MCP cursor and filtered again to the requested cluster. Output contains bounded normalized identity, state, node-pool association, capacity, instance/image/network identifiers, and posture metadata. Initialization scripts, labels, taints, private addresses, kubeconfigs, certificates, tokens, and complete SDK responses are excluded.

### Volcengine network and TOS configuration operations

Volcengine registers ten fixed reads: CLB instance, listeners, listener health and server-group membership; EIP attributes; NAT list, instance, SNAT and DNAT; and TOS bucket configuration. List operations use the provider's numeric pagination behind the opaque MCP cursor. `volcengine.nat.list_nat_gateways` requires `params.project_name` and accepts `network_type=internet|intranet`; both are pushed into `DescribeNatGateways`, making it the authoritative public/private empty-project check because Resource Center does not index the observed NAT gateway. TOS configuration returns ACL public-access posture, versioning, lifecycle counts, and delayed bucket statistics without listing or reading objects. NAT address/port mappings, backend addresses, ACL principals, owner identifiers, credentials, certificates, and object bodies are excluded.

Example database detail read:

```json
{
  "profile": "gcp-prod",
  "operation": "gcp.sql.instances.get",
  "params": {
    "project_id": "example-project",
    "instance": "orders-db"
  }
}
```

## Sensitive-data contract

None of the tools accept credentials as arguments. `cloud_query` and `cloud_native_read` return normalized metadata only. Object bodies, database rows, database endpoints, connection strings, messages, message bodies, log events, kubeconfigs, certificates, tokens, secret values, connection credentials, `user_data`, and Kubernetes API data are outside the contract. Native output is opt-in and restricted to adapter-controlled normalized fields; a complete SDK response must never be returned.
