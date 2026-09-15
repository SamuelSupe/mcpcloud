# Pipeline DSL

The DSL is intentionally small. It lets an MCP client ask the same question of multiple cloud profiles without passing an SDK action, URL, or provider-native query string.

## Sources and stages

The first token selects one source:

```text
resources | ...
iam       | ...
metrics   | ...
costs     | ...
```

Supported stages are:

| Stage | Meaning |
| --- | --- |
| `scope` | Select named profiles and optional provider/account/region scope |
| `where` | Filter normalized fields using the supported predicates |
| `range` | Select a time interval for metrics or costs |
| `step` | Select a metrics grouping interval when supported by the provider adapter |
| `include native` | Required opt-in for adapter-controlled native metadata and any `native.*` field reference |
| `fields` | Project normalized fields, tag lookups, and allow-listed adapter-controlled native metadata |
| `summarize` | Aggregate with `count`, `sum`, `avg`, `min`, or `max`, optionally `by` dimensions; native aggregate inputs/group keys are subject to the same opt-in and allow-list |
| `sort` | Stable ascending/descending ordering; native sort keys are subject to the same opt-in and allow-list |
| `limit` | Bound returned rows; server-side scan and request limits still apply |

The parser rejects unknown stages and unsupported constructs such as joins, subqueries, unions, regular-expression execution, custom functions, and provider-native query injection.

## Example resource query

```text
resources
| scope profile in ("aws-prod", "gcp-prod")
| where domain == "compute" and kind == "instance" and state == "running"
| include native
| fields provider, profile, region, id, name, state, tags["env"], native.instance_type
| sort provider asc, name asc
| limit 100
```

`scope.account_id` is available when a provider exposes it. Scope selectors currently use `profile`, `provider`, `account`, and `region`; each clause uses `in (...)`. Field availability is resource-kind and provider dependent; use `cloud_schema` or `cloud_explain` rather than assuming every provider has every field.

### Native field contract

`native.*` is a controlled projection/filter namespace, not a raw SDK response. A query must contain `| include native` before `native.*` may appear in `fields`, `where`, `sort`, or `summarize` (including aggregate inputs and `by` keys). Every native field name must be present in `cloud_schema.native_fields`, which is generated from the same shared `model.NativeFields` allow-list used by DSL validation. Allow-listed native metadata is optional: a provider may omit a field on an individual inventory row, and that absence is not permission to inspect the raw SDK response. Unknown names or unapproved paths such as `native.password`, `native.unknown`, or `native.foo.bar` are rejected during parse/validation; the server never falls back to serializing an unlisted provider field. Without `include native`, any native reference is rejected even if the name is otherwise allow-listed.

### Fields and source-aware types

`cloud_schema.fields` is a complete field directory for the DSL, not a promise that every field applies to every source. The parser applies the source boundary before `cloud_explain` or `cloud_query` proceeds:

- Common resource/result fields shared by all four sources are `provider`, `profile`, `domain`, `service`, `kind`, `id`, `name`, `scope`, `region`, `zone`, `state`, `tags`, `created_at`, `updated_at`, `observed_at`, `attributes`, and the opt-in `native` namespace.
- IAM-only fields are `principal`, `principal_type`, `resource_id`, `roles`, `actions`, `effect`, and `condition`.
- Metrics-only fields are `metric`, `timestamp`, `value`, `unit`, and `dimensions`.
- Costs-only fields are `date`, `amount`, and `currency`.

The source-specific lists apply to `fields`, `where`, `sort`, and `summarize` group keys or aggregate inputs. A cross-source reference such as `resources | fields amount` or `costs | where metric == "cpu"` is rejected during parse/validation, even though both names remain visible in the global `cloud_schema.fields` directory. Use the source capability metadata and `cloud_explain` to determine which directory entries are applicable to a query.

Aggregate input types are deliberately narrow: `sum`, `avg`, `min`, and `max` accept only `value`, `amount`, `attributes.*`, or allow-listed `native.*` fields; `count` accepts `*` or any field allowed for the selected source. Native aggregate inputs still require `include native` and the shared native allow-list. For example, `avg(value)` is a metrics aggregate, while `sum(amount)` is a costs aggregate; using either source-specific field under another source is rejected before execution.

## Predicates

Boolean expressions support `and`, `or`, `not`, and parentheses. Comparisons support `==`, `!=`, `<`, `<=`, `>`, `>=`, `in`, `contains`, `starts_with`, `ends_with`, and `exists`.

Examples:

```text
resources
| scope provider in ("aws"), region in ("ap-southeast-1", "us-east-1")
| where tags["environment"] exists and name starts_with "prod-"
| fields provider, region, id, name
| limit 50
```

String literals use double quotes. Scalar literals are validated by the parser; timestamps are accepted in the `range` stage as RFC3339 or `YYYY-MM-DD`. Account/project/subscription scope selects the provider target and remains constrained by the profile allow-list. Region handling is source/provider-specific: Azure subscription metrics require an exact region and pass it as the API's required `region` parameter; GCP IAM/metrics calls are global for their selected scope and do not fan out by region; and all cost connectors make one global/account/project-level call per expanded target, then apply an explicit DSL region scope locally to normalized rows. In the current engine, `where` predicates are evaluated locally against normalized rows (and `cloud_explain` reports them as residual filters), subject to the server scan limit.

## Metrics and costs

`metrics` and `costs` are implemented as read-only provider connectors and are DSL-only sources. They are not registered `cloud_native_read` operations, and the 63 product-level native operations do not expose metric or billing APIs. Their capability entries describe the connector and configuration state; they do not prove that the selected profile has credentials, permissions, enabled services, or data. Use `cloud_schema` and `cloud_explain` before executing a query.

Metric selectors are intentionally provider-specific, but are still ordinary DSL string values rather than provider query languages:

| Provider | Selector form | Notes |
| --- | --- | --- |
| AWS | `namespace::metric?dim=value` | Two `::` parts; dimensions are optional. A selected account scope is sent to CloudWatch as `AccountId` when one is available. |
| Alibaba Cloud | `namespace::metric?dim=value` | Two `::` parts; dimensions are optional. The profile must contain exactly one `scopes.accounts` entry; missing or multiple accounts report `not_configured`. |
| Huawei Cloud | `namespace::metric?dim=value` | Two `::` parts; one to four dimensions are required by CES. The selected `scopes.projects` target is bound as the CES `project_id`; an account scope is not a substitute, and a missing project scope is `not_configured`. |
| Tencent Cloud | `namespace::metric?dim=value` | Two `::` parts and at least one dimension. The profile must contain exactly one `scopes.accounts` entry and a non-`*` exact region; missing prerequisites report `not_configured`. |
| Azure | `namespace::metric?dim=value` | Two `::` parts; dimensions become Azure Monitor metric filters. |
| GCP | `metric.type?metric.label=v&resource.label=v` | One metric type; dimensions must be `metric.<label>` or `resource.<label>`. |
| Volcengine | `namespace::subnamespace::metric?dim=value` | Three `::` parts and at least one instance dimension. The profile must contain exactly one `scopes.accounts` entry; missing or multiple accounts report `not_configured`. |

Metric target and region semantics are separate from selector syntax. Azure subscription metrics require one exact profile region (a `*` region is not sufficient) and push that region into the provider request; missing exact region reports `not_configured`. GCP metrics require a project scope from `scopes.projects` or a project-level `options.asset_scope`; missing project scope reports `not_configured`. The Monitoring call is project-global, so a DSL `scope region` is a local filter on normalized rows and does not create regional API calls. GCP IAM follows the same global/no-region-fan-out rule. Other metric adapters may use their provider-specific region contract; inspect `cloud_explain` and `cloud_schema` for the selected targets.

Dimension names and values are single URL-query pairs. A dimension name is a strict ASCII selector identifier matching `[A-Za-z0-9][A-Za-z0-9._/-]{0,127}`. The parser URL-decodes each value before validation; the decoded literal must be non-empty, at most 256 bytes, have no leading or trailing whitespace, and contain no control characters. Values may therefore contain ARN/URI punctuation such as `:` (for example, encode the colon as `%3A` in the selector when needed). The parser accepts up to 16 dimensions per selector. A metric query must contain an exact `metric == "selector"` predicate, or a finite `metric in ("selector-a", "selector-b")` predicate on every OR branch. At most 20 selectors are allowed per query. Registered provider constructors receive dimensions as structured SDK fields/maps or correctly escaped provider filter values; these literals are never treated as an original-provider query string or query injection.

Metric queries require `range`; the maximum range is 31 days. `step` is optional and defaults to the provider adapter's five-minute interval; the DSL minimum is one minute and it cannot exceed the requested range. Provider APIs may apply a stricter service-specific period or page limit. Normalized metric rows expose `metric`, `timestamp`, `value`, `unit`, and `dimensions` in addition to the common result fields.

```text
metrics
| scope profile in ("aws-prod", "gcp-prod")
| range "2026-08-01T00:00:00Z" to "2026-08-02T00:00:00Z"
| where metric == "AWS/EC2::CPUUtilization?InstanceId=i-0123456789abcdef0"
| step 1h
| summarize avg(value) by provider, profile, region
| sort provider asc, profile asc
| limit 100
```

The GCP form uses the metric type as the part before `?`, for example:

```text
metrics
| scope profile in ("gcp-prod")
| range last 6h
| where metric == "compute.googleapis.com/instance/cpu/utilization?metric.instance_name=web-1&resource.project_id=example-project"
| summarize avg(value) by profile, region
| limit 100
```

```text
costs
| scope profile in ("aws-prod", "gcp-prod")
| range "2026-08-01" to "2026-08-08"
| summarize sum(amount) by provider, currency, service
| sort provider asc, currency asc
```

Cost queries require `range`; the maximum range is 400 days. Cost rows expose `date`, `amount`, and `currency`, with amount preserved in the provider-reported currency. The engine rejects a non-count amount aggregate unless `currency` is included in the grouping and never performs implicit exchange-rate conversion. Cost connectors issue one global/account/project-level API call per expanded target rather than one call per region. A DSL `scope region` therefore filters returned cost rows locally; it is not a promise that the provider cost API receives a region filter. Tencent's cost API does not return a currency, so its profile must declare `options.billing_currency`; that declaration is attached to normalized rows and is not inferred from the API.

GCP costs read a user-prepared standard Billing Export table through the BigQuery query API. Set `options.billing_table` to `project.dataset.table`; `billing_project` and `billing_location` are optional query-job settings. The service does not enable Billing Export, create datasets, or write billing data. See [configuration](configuration.md) and [permissions](permissions.md) for the required read-only setup. Huawei's `options.billing_site` is required and explicitly selects the `china` or `intl` BSS endpoint; omission is `not_configured` in capabilities and `capability_unavailable` for a cost query. Tencent costs additionally require exactly one `scopes.accounts` entry and the declared `options.billing_currency`.

## Explain before execute

`cloud_explain` validates the same DSL as `cloud_query` without making cloud calls. Its response identifies the source, target profiles, normalized fields, target-planning/provider pushdown metadata, local residual stages, and capability notes or configuration requirements. `pushdown` is source/provider-specific: an Azure metrics `scope.region` is a required provider query parameter, while GCP IAM/metrics and all costs use a single global/account/project-level target and report the region constraint as local/residual filtering. Filter expressions remain residual/local in the current engine. A `not_configured` or provider capability error is actionable: supply the required profile option or exact region/project scope, narrow the scope, or record the unavailable target explicitly.

## Result contract

`cloud_query` returns a structured result with a status of `ok`, `partial`, or `failed`; columns; rows; optional `next_cursor`; target/coverage information; per-target errors; and request, scan, and truncation statistics.

Normalized resource rows use these stable fields when available:

`provider`, `profile`, `domain`, `service`, `kind`, `id`, `name`, `scope`, `region`, `zone`, `state`, `tags`, `created_at`, `updated_at`, `observed_at`, `attributes`.

`native` appears only when `include native` is present and contains only adapter-controlled normalized metadata. A missing field is not permission to inspect the raw SDK response. Partial results preserve successful rows and identify failed targets with a safe error code and retryability flag.

## Bounds and pagination

The service enforces server-side bounds on execution targets, provider requests, scanned items, execution time, page size, and cursor lifetime. A DSL query cannot raise those limits. A `next_cursor` is an opaque, short-lived server token; send it alone for the next page as specified by the tool schema. Cursors are in-memory and become invalid after process restart or expiry.

Alibaba costs use `QueryInstanceBill` with `Granularity=DAILY`, not settlement-order usage-start timestamps. Use date-only ranges such as `range "2026-09-03" to "2026-09-10"` for the seven billing dates September 3–9. Dates are provider billing-day labels represented at UTC midnight; a sub-day start excludes that date, with no proration. Daily bills can lag by one day. Amounts are `PretaxAmount`, grouped by provider currency. Known localized regions are normalized; unknown regions remain unchanged and are subject to the profile allowlist. Empty-region charges remain unallocated and are included in unscoped totals, never assigned to a named region. Explicit regional queries exclude them. The legacy API has a 50,000-row daily ceiling: larger totals fail explicitly rather than silently truncate. A 120000 ms query timeout is recommended for multi-day scans; request/item budgets still apply.

Tencent international partner customer costs are opt-in: set `options.billing_source: intl_partner_customer` and explicitly set `options.billing_currency: USD`. The default `billing` source retains its existing behavior. This source calls the international `DescribeCustomerOwnCostExplorerSummary` API with Business dimension, totalCost, billing statement (BillType 1), and daily periods. It requires exactly one configured account and midnight-aligned date ranges within two calendar months. Service identifiers are provider product codes. Each provider page is one request; errors and changing totals fail explicitly.

The summary does not return currency: USD is a configuration assertion, identified by `native.currency_source`, and must be checked against the customer's settlement records. `amount` uses the existing float64 representation, including engine aggregates; `include native` exposes `native.amount_decimal` for exact decimal reconciliation. Do not claim decimal-exact aggregate arithmetic. Provider pagination is not a transactional snapshot.
