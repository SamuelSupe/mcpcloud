# Configuration and runtime

`mcpcloud` uses a YAML configuration containing named, non-secret provider profiles. The exact schema is validated by `validate-config`; use [examples/config.example.yaml](../examples/config.example.yaml) as the starting shape and keep the installed binary's schema output as authoritative.

## Credential rules

Do not put an access key, secret key, bearer token, private key, password, connection string, or service-account JSON in YAML or MCP tool arguments. A profile may refer to a provider-native shared profile, workload identity, instance/task identity, or the name of an environment variable. The value stays outside the MCP process configuration and is resolved by the official SDK/default chain.

The server reports only whether a profile is configured and whether credential discovery is ready. It never echoes secret material. Treat process environments, shell history, logs, crash dumps, and debug output as sensitive.

## Profile shape

`profiles` is a YAML map keyed by stable local profile name. Each value has a `provider`, a nested `credential` reference, provider-specific `scopes`, and optional `regions`, `services`, and `options`. Restrict `scopes.accounts`, `scopes.projects`, `scopes.subscriptions`, and `regions` to the smallest read-only set that the deployment needs. `services` is enforced by `scopedAdapter` as a normalized result filter on each row's domain/service; it is not provider IAM and does not guarantee cloud API pushdown, so enforce the same restriction in provider IAM. The profile name is what the DSL uses in `scope profile ...`; it is not a cloud credential.

The example shows seven provider references, not credential values. Each provider adapter is registered in the current build; before a live query, keep only the scopes/regions and credential sources available in your environment, inspect configuration readiness with `cloud_profiles`, and use the fixed `probe-profile` command when live scope evidence is required:

```yaml
profiles:
  aws-prod:
    provider: aws
    credential:
      source: profile
      profile: readonly
    scopes:
      accounts: ["123456789012"]
    regions: ["ap-southeast-1"]
    services: [compute, storage, network, iam]
```

`credential.source` is one of `default`, `profile`, `env`, or `workload`. `profile` is supported only for AWS and names an AWS shared profile. Use `credential.env` only as a map from logical credential names to environment-variable names; the values are names such as `GOOGLE_APPLICATION_CREDENTIALS`, never secret contents. For GCP service-account JSON, keep the file in the provider credential environment and reference it with an env mapping, for example `GOOGLE_APPLICATION_CREDENTIALS: GOOGLE_APPLICATION_CREDENTIALS`; do not put the JSON or a file-content blob in YAML. Use provider-native configuration outside this file for the actual shared profile or workload identity. Never replace these references with an access key, token, private key, or JSON document.

The supported provider IDs are `aws`, `gcp`, `azure`, `alibaba`, `huawei`, `tencent`, and `volcengine`. A profile can be syntactically valid but still report `missing_credentials`, `configured_unverified`, `not_configured`, or a provider capability error at runtime; use `cloud_schema` and `cloud_profiles` before issuing broad queries. Every configured adapter registers the nine shared inventory product suffixes, for 63 product-level native operations across seven providers; `cloud_schema.operations` is the authoritative list for the current profile set. These operations remain inventory/resource-graph-backed metadata reads and do not imply live credentials, permissions, indexing, or service-detail SDK coverage. All seven adapters also expose metric and cost connector paths; those paths remain live-unverified until a real profile and representative data are checked. For metrics, Alibaba, Tencent, and Volcengine require exactly one `scopes.accounts` entry; missing or multiple entries make the metric capability `not_configured`. Tencent metrics additionally require a non-`*` exact profile region, and Tencent costs require that same single account plus `options.billing_currency`. AWS metrics pass a selected account scope to CloudWatch as `AccountId`. Huawei metrics require `scopes.projects`, bind the selected project as CES `project_id`, and report `not_configured` when the project scope is missing; they do not substitute an account scope. Azure subscription metrics require at least one exact profile region; `*` is not sufficient and the selected region is sent as the required API parameter; missing exact region reports `not_configured`. GCP metrics require `scopes.projects` or a project-level `options.asset_scope`; missing project scope reports `not_configured`. GCP IAM/metrics calls are global for the selected scope and do not fan out by region. All costs use one global/account/project-level call per expanded target, with explicit DSL/profile region constraints applied locally to normalized rows rather than guaranteed billing API pushdown.

## Metric and cost options

Provider options are identifiers and declarations, not credentials. They do not create external resources or enable billing exports.

```yaml
profiles:
  gcp-prod:
    provider: gcp
    options:
      asset_scope: projects/example-project
      # This project-level scope is required for GCP metrics; Monitoring/IAM are global calls.
      # Existing standard Billing Export table; project.dataset.table.
      billing_table: billing-project.billing_export.gcp_billing_export_v1_example
      # Optional BigQuery query-job project and dataset location.
      billing_project: billing-project
      billing_location: US

  huawei-prod:
    provider: huawei
    options:
      # Required for Huawei costs; explicitly choose china or intl (there is no default).
      billing_site: china

  tencent-prod:
    provider: tencent
    options:
      # The cost API does not return currency; declare the source currency.
      billing_currency: CNY
```

For the example's GCP and Azure profiles, configured regions remain useful result filters, but their metric semantics differ: GCP Monitoring and IAM do not fan out by region, whereas Azure subscription metrics require an exact region and send it to Azure Monitor. Cost APIs for every provider remain global/account/project-level per expanded target; configured or DSL regions filter normalized cost rows locally.

`gcp.options.billing_table` must name a preconfigured standard [Billing Export](https://cloud.google.com/billing/docs/how-to/export-data-bigquery) table. The service only submits a parameterized read-only BigQuery query job and reads its result; it does not create or modify the export. `billing_project` defaults to the project part of `billing_table`, and `billing_location` is optional. The caller needs BigQuery query-job and table-data permissions described in [permissions.md](permissions.md). `huawei.options.billing_site` is required for Huawei cost queries and must be explicitly set to `china` or `intl`; omission reports the cost capability as `not_configured` and a query as `capability_unavailable`. `tencent.options.billing_currency` must be a three-letter uppercase code such as `CNY`; it is a configuration declaration because the Tencent cost summary response has no currency field. Tencent cost queries also require exactly one `scopes.accounts` entry because the API neither selects nor returns a target account.

## Runtime commands

Validate a configuration without contacting cloud APIs:

```sh
./mcpcloud validate-config --config ~/.config/mcpcloud/config.yaml
```

Run a fixed read-only profile probe:

```sh
./mcpcloud probe-profile --profile aws-prod --config ~/.config/mcpcloud/config.yaml
./mcpcloud probe-profile --all --config ~/.config/mcpcloud/config.yaml --timeout 45s
```

`probe-profile` requires exactly one of `--profile NAME` and `--all`. Its timeout defaults to 30 seconds and may not exceed two minutes. It first checks each profile's configured readiness, then calls only the first registered `resources` inventory operation with `Limit=1`. The JSON report exposes profile/provider, authentication completion, declared and observed scope evidence, capability statuses, operation/region, `scope_verified`, `identity_status`, and sanitized error codes. It does not expose resource rows, request/response bodies, credentials, or principal identity details. A successful fixed read means `authenticated`; it is not proof that a principal identity API was called.

Scope evidence is deliberately conservative: `ready` requires exactly one configured resources scope and a match in the single probe result. If multiple resources scopes are configured, one matching scope is only partial evidence: the profile is `degraded` with `identity_status` `authenticated_partial_scope_evidence`. A successful read without a matching scope is `degraded` with `authenticated_scope_unverified`. Scope matching is declaration-type strict, including Huawei project scopes matching only observed `project_id` and Huawei account scopes matching only observed `account_id`. For `--all`, all profiles must be `ready` for aggregate `ready`; any reachable but incomplete profile yields `degraded`, and no reachable profile yields `not_ready`. The command still prints JSON for degraded/not-ready reports but exits non-zero.

Start stdio (the default for desktop MCP clients):

```sh
./mcpcloud serve --transport stdio --config ~/.config/mcpcloud/config.yaml
```

Start local Streamable HTTP:

```sh
export MCPCLOUD_AUTH_TOKEN='provided by the process secret environment'
./mcpcloud serve --transport http --config ~/.config/mcpcloud/config.yaml
```

HTTP endpoints:

- `/mcp`: MCP Streamable HTTP transport.
- `/healthz`: process health check.
- `/readyz`: one-minute-TTL-cached fixed profile readiness. It calls the same bounded inventory probe when the cache expires, returns only aggregate `profiles`, `ready`, `degraded`, and `not_ready` counts plus probe/cache timestamps, and never returns profile rows or credentials. `ready` and `degraded` return HTTP 200; `not_ready` or a probe failure returns HTTP 503.

Loopback binding, a non-empty Bearer token, and Host/Origin validation are the safe defaults. Configure the token environment variable with `server.auth_token_env` (default `MCPCLOUD_AUTH_TOKEN`); configure `server.allowed_hosts` and `server.allowed_origins` for the deployment. If remote access is required, terminate TLS and enforce network policy in an external reverse proxy. Keep the MCP process bound to a private interface unless the deployment explicitly supplies that boundary.

## Limits and lifecycle

The engine applies fixed upper bounds to execution targets, provider requests, scanned items, total execution time, page size, concurrency, and cursor TTL. Configuration may tighten these values; it cannot loosen them through a query. The current YAML keys are `limits.max_targets`, `max_provider_requests`, `max_scanned_items`, `max_page_size`, `default_timeout`, `max_timeout`, `cursor_ttl`, and `concurrency`; duration values use Go duration syntax such as `30s` and `10m`. Cursor state is in process memory, bounded by TTL, and is lost on restart. HTTP readiness uses a one-minute cache TTL and a 15-second probe timeout, tightened by `limits.max_timeout` when that configured maximum is lower.

SDK diagnostics go to stderr. Structured provider-call audit records contain correlation ID, provider, profile, source/operation, scope, region, attempt/request counts, duration, rows/scanned counts, status, and safe error code/retryability. `cloud_native_read` also returns the correlation as `request_id`. Audit records must not contain credentials, request bodies, raw cloud responses, object contents, database rows, messages, log events, secret values, or connection details. Probe and readiness evidence does not replace the seven-provider live resource matrix or provider-audit acceptance.
