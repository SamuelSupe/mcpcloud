# Azure/GCP acceptance follow-up — 2026-10-13

This follow-up closes the test-bucket capacity and global backend live acceptance gaps recorded in the [original snapshot](../20261012/README.md). It uses the implementation merged in PR #38; no provider code, IAM grants, audit configuration or retained production deployment was changed.

## Original scope

All 27 original native operations had positive representative samples (Azure 13, GCP 14). The merged source was independently checked out, tested and built; selected metrics and real GCP billing were reconciled to native references. These results do not claim exhaustive cloud-product, metric or scale coverage.

| Retained condition | Reason / next requirement |
|---|---|
| Azure real costs remain deferred | The tested classic Sponsorship offer does not support the selected Cost Management path. A supported subscription with real cost data is needed. Request/decoding fixes are not live amount acceptance. |
| Complete cross-service cloud audit remains incomplete | Azure Activity Log does not typically log reads. GCP non-BigQuery Data Access logging defaults off; organization inheritance, exemptions and routing could not all be verified. Existing BigQuery evidence cannot prove every service call. Service logging and administrator checks must precede a new reconciliation. |

## Completed extensions

| Extension | Positive evidence |
|---|---|
| Object metadata | Two objects and 1,024 bytes per cloud; complete object pagination. No object-body download capability is claimed. |
| Pod state and pagination | Azure 14 Pods / five pages; GCP 17 Pods / six pages. The 601-Pod boundary check used simulated responses. |
| GCP new test bucket capacity | Three MCP hourly points match a separate native query using the same window and ALIGN_MEAN/3,600-second alignment; all are 1,024 bytes. Object metadata also totals 1,024 bytes. Earlier missing samples are historical; their cause is not established. |
| GCP global backend details and health | Reused the existing test VM/nginx through a dedicated zonal NEG. Original MCP identity and the current Codex session read one detail and one HEALTHY endpoint. Native references match identity, associations, instance, IP, port and health. Global and existing regional HTTP return 200 with the expected test page; regional health remains HEALTHY. |

Node continuation remains an optional unimplemented extension, not an original-scope blocker. Current Node reads reject incomplete continuation instead of claiming a complete list.

## Validation configuration and evidence

The independently authorized creation identity prepared a global backend, HTTP health check, NEG and minimal HTTP frontend. The original read identity performed MCP and native-reference reads. No new VM or IAM grant was created. A newly created instance group could not reuse the already load-balanced VM and was removed after switching to NEG; the original instance group and regional backend were retained.

CLI operation-wait transport failures were followed by authoritative reads to confirm committed state. The HTTP harness initially assumed an nginx welcome page; the existing fixture actually returns `mcpcloud validation`. The corrected assertion verifies that body and the nginx response header, without changing provider code.

- [Capacity summary](verification.gcp.capacity-followup-summary.20261012.json)
- [Global backend summary](verification.gcp.global-acceptance-summary.20261013.json)
- [Restored alert demo summary](verification.azure-gcp.demo-alerts-summary.20261013.json)
- [Acceptance status](verification.azure-gcp.acceptance-status.20261013.json)

The two existing alert fixtures were re-read after restoring the isolated demo entry: Azure one rule / one provider request; GCP one rule / eight provider requests, complete pagination. This verifies rule metadata, not alert firing or notification delivery.

Only sanitized summaries are published. Credentials, cloud identifiers, frontend addresses, raw IAM/billing/audit records, private reference responses and internal report URLs remain excluded. Formal acceptance and production replacement have not occurred; the new validation resources remain registered locally for later cleanup.
