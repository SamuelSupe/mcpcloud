# mcpcloud

`mcpcloud` is a Go MCP server for read-only cloud control-plane discovery. It presents one small, provider-neutral query language and a provider adapter registry for AWS, GCP, Azure, Alibaba Cloud, Huawei Cloud, Tencent Cloud, and Volcengine. All seven providers have resource-inventory adapters backed by their resource explorer, resource graph, resource center, or equivalent inventory APIs. Each adapter also registers the same nine product-level native suffixes, for 63 provider-qualified operations in total, plus one service-level compute-instance detail operation and at least two managed database/cache/Kubernetes detail operations. Volcengine also exposes two cluster-scoped VKE topology list operations and ten fixed CLB, EIP, public/private-NAT, and TOS configuration operations. The service operations use fixed provider read APIs and return only normalized metadata. The DSL is the normal interface; the native tool is deliberately constrained to registered typed operations. A separate CLI/HTTP readiness gate performs a bounded fixed inventory probe without changing the six-tool MCP surface.

> Status: local implementation. The MCP executable, six tools, seven read-only inventory adapters, 63 product-level native operations (7 providers × 9 suffixes), seven service-level compute-instance detail operations, 16 managed database/cache/Kubernetes/storage detail operations, and seven-provider metrics/cost connectors are present. Resource and IAM capabilities are reported as `inventory`; all 23 service-level detail operations are registered but live-unverified; metrics and costs are reported as `available` or, when required provider configuration or an unambiguous target scope is absent, `not_configured`. These statuses describe implemented connectors and registered schemas, not live access. `probe-profile` and `/readyz` provide fixed inventory scope evidence, but they are not a principal-identity API and do not replace the seven-provider live resource matrix or provider-audit acceptance. No cloud profile, credential, representative resource/instance/cluster, or billing dataset was available, so seven-provider live acceptance has not been performed. Runtime capability metadata is authoritative over this document.

## Safety boundary

The server is control-plane read-only. The DSL does not accept provider-native query-language strings, arbitrary provider actions, or arbitrary URLs. `cloud_native_read` accepts only the exact typed parameters declared by the selected registered operation. The 63 product-level operations use the provider-specific schemas in [docs/tools.md](docs/tools.md) and fixed inventory/resource-graph backends; the seven compute and 16 managed database/cache/Kubernetes/storage detail operations use fixed provider service read APIs and an allow-listed normalized row. Neither class returns a complete SDK response. The call remains constrained by the profile scope, operation allow-list, and parameter schema. It must not return object bodies, database rows, database endpoints, connection strings, messages, message bodies, log events, kubeconfigs, certificates, tokens, secret values, private keys, credentials, user/custom data, console/VNC material, or other data-plane fields. `native` fields are opt-in and come only from adapter-controlled normalized metadata.

Credentials are resolved by the provider SDK's normal chain (shared profiles, workload identity, instance/task identity, or an environment-variable reference). They are not MCP arguments and must not be placed in YAML. `cloud_profiles` reports readiness metadata only; it never returns a secret.

Each provider call emits structured audit evidence with correlation ID, provider/profile, source and operation, scope/region, attempt and request counts, duration, row/scanned counts, status, and safe error code/retryability. `cloud_native_read` returns that correlation as `request_id`. Audit evidence is metadata only and must not contain credentials, request/response bodies, raw SDK responses, object contents, database rows, messages, log events, secret values, or connection details.

## MCP tools

The MCP layer registers these read-only tools:

| Tool | Purpose | Cloud calls |
| --- | --- | --- |
| `cloud_help` | Return structured, provider-aware guidance and safe examples | No |
| `cloud_query` | Parse, validate, execute, normalize, filter, aggregate, sort, and paginate a DSL query | Yes, for an executable query |
| `cloud_explain` | Return the validated logical plan, target profiles, pushdown and local stages, and capability notes/requirements | No |
| `cloud_schema` | Return the static seven-cloud capability baseline, configured-profile capabilities, DSL metadata, and registered operations | No |
| `cloud_profiles` | Return configured profile/provider/scope/region and credential readiness | No |
| `cloud_native_read` | Execute one registered, schema-described read operation with adapter validation | Yes, only for the registered operation |

For exact input and output schemas, call `cloud_schema` against the running server. Its `capability_matrix` is the static seven-provider/four-source compiled baseline, while `profile_capabilities` reflects configured profile prerequisites; neither is live readiness evidence. `operations` lists the typed native operations registered by currently configured profile adapters, including the 63 product-level names, seven compute-detail names, and 16 managed database/cache/Kubernetes/storage detail names when all seven adapters are configured. A normal `cloud_query` request contains the DSL `query`, and may contain `page_size` (server default and maximum apply) or a short-lived `cursor` for the next page. Do not send credentials, URLs, methods, arbitrary API actions, or provider-native query strings through the DSL. Use `cloud_native_read` only with a registered operation whose schema explicitly declares the typed parameter you need.

## `cloud_help`

`cloud_help` is the no-cloud-call entry point for the six-tool surface. Its public input is exactly `{topic?, provider?, source?}`: `topic` may be `quickstart`, `dsl`, `profiles`, `resources`, `iam`, `metrics`, `costs`, `native`, `pagination`, `errors`, or `security`; an omitted or blank topic returns the `index`. `provider` and `source` are optional filters for guidance and examples, not credentials, authorization, or a request to access a cloud.

The structured result is `HelpOutput`: `topic` is the selected topic (`index` for empty input), `summary` is its short explanation, `topics` lists index topics, `recommended_flow` contains ordered `{tool,purpose,arguments}` steps, `rules` contains safety/usage constraints, `examples` contains named `{tool,arguments}` examples, and `related_topics` links to adjacent help. The index/quickstart flow is explicitly `cloud_profiles` → `cloud_schema` → `cloud_explain` → `cloud_query`; `cloud_help` itself does not call any cloud API. Invalid topic, provider, or source values return a safe error. Help/readiness text is not live cloud-readiness evidence.

Minimal MCP inputs:

```json
{}
```

```json
{"topic":"metrics","provider":"aws"}
```

```json
{"topic":"native"}
```

Metrics and costs remain DSL-only. Their help topic gives only restricted DSL selector examples or resource-dimension placeholders; it does not accept original-provider query language or arbitrary actions. Use `native` guidance only when normalized DSL fields are insufficient, then call `cloud_schema` and select an operation already present in `cloud_schema.operations`; never invent an operation or `params`, and treat dynamic registrations as schema-authoritative. Do not place credentials, provider-native query strings, arbitrary URLs/actions, cursors, or sensitive data in help input or examples. `cloud_query` cursors remain opaque and short-lived; native inventory cursors are passed only as returned, while fixed service-detail operations do not paginate. The readiness probe is a CLI/HTTP gate, not an additional MCP tool; the MCP surface remains exactly the six tools listed above.

## Quick start

Build and validate the local binary:

```sh
go build -o mcpcloud ./cmd/mcpcloud
./mcpcloud validate-config --config examples/config.example.yaml
```

The example configuration contains seven provider profile references and no secret values. Copy it to the configured user path and replace only the profile names, scopes, regions, and credential references that exist in your environment. Keep credentials in the provider's normal credential store, workload identity, or environment variables named by the profile.

Before starting a broad query, run the fixed profile probe when real credentials are available:

```sh
./mcpcloud probe-profile --profile aws-prod --config ~/.config/mcpcloud/config.yaml
./mcpcloud probe-profile --all --config ~/.config/mcpcloud/config.yaml --timeout 45s
```

The command accepts exactly one of `--profile NAME` or `--all`; `--timeout` defaults to 30 seconds and is capped at two minutes. It writes a structured report to stdout and returns a non-zero status when the aggregate result is `degraded` or `not_ready`. Each selected profile first passes configuration readiness, then invokes only its first registered resources-inventory operation with a bounded page size of 100 and follows at most 10 provider cursors, stopping as soon as the declared scope is observed. This handles inventory services whose first page can be empty or contain resources outside the configured region. The report contains `authenticated`, declared and observed scopes, `scope_verified`, `identity_status`, operation/region, capability statuses, and sanitized errors; it never serializes resource rows or credentials. `authenticated` means that this fixed read completed, not that a principal-identity API was called.

Readiness is intentionally conservative for configured scope. A probe is `ready` only when exactly one configured resources scope exists and that unique scope is observed as matching. With multiple configured resources scopes, observing one matching scope is `degraded` with `identity_status: authenticated_partial_scope_evidence`; no matching scope is `degraded` with `authenticated_scope_unverified`. Scope matching is declaration-type strict, including Huawei project scopes matching only observed `project_id` and Huawei account scopes matching only observed `account_id`. All selected profiles must be `ready` for an aggregate `ready`; any authenticated but incomplete evidence yields `degraded`, and when no selected profile is reachable the result is `not_ready`. This probe is a preflight signal, not the seven-cloud live resource matrix or provider-audit acceptance.

Run MCP over stdio for a desktop client:

```sh
./mcpcloud serve --transport stdio --config ~/.config/mcpcloud/config.yaml
```

Run the protected local HTTP transport:

```sh
export MCPCLOUD_AUTH_TOKEN='use-a-secret-manager-or-a-process-environment'
./mcpcloud serve --transport http --config ~/.config/mcpcloud/config.yaml
```

The HTTP service exposes `/mcp` for Streamable HTTP, `/healthz` for process health, and `/readyz` for the cached fixed-profile readiness gate. `/readyz` refreshes at most once per one-minute TTL, returns only aggregate `profiles`, `ready`, `degraded`, and `not_ready` counts plus probe/cache timestamps, and never returns profile rows or credentials. `ready` and `degraded` return HTTP 200; `not_ready` (including a probe failure) returns HTTP 503. It binds loopback by default, reads the Bearer token from `server.auth_token_env` (default `MCPCLOUD_AUTH_TOKEN`), and validates Host/Origin. Put TLS and any remote exposure in an explicitly configured reverse proxy; do not bind this process to a public interface by accident.

## First query

```text
resources
| scope profile in ("aws-prod")
| where domain == "compute"
| include native
| fields provider, profile, region, id, name, native.instance_type
| sort provider asc, name asc
| limit 100
```

`native.instance_type` is an AWS allow-listed optional metadata field; inventory rows may omit it when the provider does not expose that value.

## Product-level native read

For a product-level inventory view, use the registered operation name and keep provider-specific filters in `params`. AWS product operations declare no `params`; its region is the top-level `cloud_native_read.region` field:

```json
{
  "profile": "aws-prod",
  "operation": "aws.compute.list_instances",
  "region": "ap-southeast-1",
  "params": {},
  "page_size": 25
}
```

The nine shared suffixes and all seven provider-qualified names, along with their exact parameter schemas, are in [docs/tools.md](docs/tools.md). Product reads are still inventory-backed control-plane metadata. A provider page can yield zero rows after product filtering while returning `next_cursor`; continue with that cursor until it is empty. Metrics and costs remain DSL-only sources and have no `cloud_native_read` operation.

## Service-level compute instance detail

The seven service-level compute detail operations are distinct from the 63 inventory product operations. They perform one fixed provider compute read for one named instance and return one normalized compute row. The exact operation names and required parameter/scope rules are in [docs/tools.md](docs/tools.md). The 16 managed database/cache/Kubernetes/storage detail operations are documented below and in the same reference; they are separate service-level metadata reads, not extensions of the inventory catalog.

```json
{
  "profile": "aws-prod",
  "operation": "aws.ec2.describe_instance",
  "region": "ap-southeast-1",
  "params": {"instance_id": "i-example"}
}
```

The profile must contain exactly one allowed account for AWS, Alibaba Cloud, Tencent Cloud, and Volcengine detail reads, and the request must supply or resolve one exact region. Detail output is limited to provider-supplied stable compute metadata such as instance type, CPU/memory when available, image, network/IP/security-group information, OS, and billing mode. It never exposes userData/customData, arbitrary metadata, admin/password material, console/VNC access, credentials, or a complete SDK response. All seven detail operations remain live-unverified; live acceptance is still blocked.

## Service-level managed database, Kubernetes, and storage detail

The 16 managed database/cache/Kubernetes/storage operations are distinct from both the 63 inventory operations and the seven compute-instance details. They call one fixed provider service read for one named managed database, cluster, or storage bucket and return one normalized metadata row. Exact public names, parameters, provider scope rules, fixed APIs, and the bounded `attributes.posture`/`attributes.related` contract are in [docs/tools.md](docs/tools.md) and [docs/providers.md](docs/providers.md). Schemas are closed: undeclared parameters and continuation cursors are rejected, and the operations do not accept arbitrary URLs, actions, paths, or follow-up graph queries.

```json
{
  "profile": "aws-prod",
  "operation": "aws.rds.describe_db_instance",
  "region": "ap-southeast-1",
  "params": {"db_instance_identifier": "database-1"}
}
```

The normalized `attributes.posture` map contains only provider-supplied, allow-listed posture values; provider-specific keys are optional and are not a universal schema. For example, GCP Cloud SQL uses `customer_managed_encryption_key_enabled`; AWS RDS `logging_enabled` and `audit_enabled` are present only when the primary response exposes CloudWatch log-export entries, including an audit export. `attributes.related` contains only unique identifiers from the primary response, at most 100 per key; no recursive or follow-up graph query is made. These operations never return a password, connection string, database endpoint, storage endpoint, bucket owner, KMS key ID, kubeconfig, certificate, token, secret, `user_data`, logs, database rows, message/body, or a complete SDK response. All 16 remain live-unverified in this repository and seven-provider live acceptance remains blocked.

The four DSL data sources are `resources`, `iam`, `metrics`, and `costs`. All seven adapters have read-only inventory, metric, and cost connector paths, while `cloud_native_read` exposes the 63 inventory product operations, seven fixed compute detail operations, and 16 fixed managed database/cache/Kubernetes/storage detail operations. Metrics and costs remain DSL-only. GCP costs require a preconfigured Billing Export table. Huawei costs require an explicit `options.billing_site` of `china` or `intl`; there is no default, and omission reports `not_configured` in capability metadata and `capability_unavailable` at query time. Tencent costs require the declared `billing_currency` and exactly one `scopes.accounts` entry; Tencent metrics require that same single account plus a non-`*` exact region. Alibaba and Volcengine metrics require exactly one `scopes.accounts` entry. AWS metrics pass a selected account scope to CloudWatch as `AccountId`; Huawei metrics require `scopes.projects`, bind that target as CES `project_id`, and report `not_configured` when the project scope is missing. Missing or ambiguous scope prerequisites are reported by runtime capability metadata rather than guessed. Use `cloud_explain` first when a query spans providers or when you need to inspect targets, residual filters, and capability notes. See [docs/dsl.md](docs/dsl.md) for selector syntax and the result contract, [docs/configuration.md](docs/configuration.md) for profile setup, and [docs/providers.md](docs/providers.md) for the implemented-but-live-unverified matrix.

Region handling is source/provider-specific. Azure subscription metrics require an exact configured region and send it as the required API query parameter. GCP IAM/metrics use one global project call without region fan-out. Every costs connector makes one global/account/project-level call per expanded target, then applies an explicit DSL region scope locally to normalized rows; `scope.region` is not universally a provider-side cost filter.

## Repository guides

- [DSL reference](docs/dsl.md)
- [MCP tool reference](docs/tools.md)
- [Configuration and runtime](docs/configuration.md)
- [Provider capability and readiness matrix](docs/providers.md)
- [Read-only and least-privilege guidance](docs/permissions.md)
- [Live acceptance report](docs/live-acceptance.md)
- [Secret-free example](examples/config.example.yaml)

## Verification status

Documentation describes the public contract and the safe baseline. The 63 product-level schemas, seven compute-detail registrations, and 16 managed database/cache/Kubernetes/storage detail registrations are complete in the local implementation, but profile readiness still depends on credentials, permissions, inventory/service availability, exact scope and region, and representative resources. It does not substitute for cloud evidence. The live acceptance report records the missing credentials/resources and the exact boundary of what was not tested; all 23 detail operations and seven-provider live acceptance remain blocked or live-unverified. Provider capability metadata returned by `cloud_schema` and errors returned by `cloud_query` are the source of truth for a particular deployment.
