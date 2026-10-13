# Azure/GCP functional acceptance — 2026-10-12

Latest status: [2026-10-13 acceptance follow-up](../20261013/README.md). The new test bucket capacity and global backend live acceptance gaps have now been closed; this directory retains the original validation snapshot.

The validated implementation covers the agreed Azure non-cost functions and GCP functions, including real BigQuery billing queries. All 27 original native operations have positive live samples (Azure 13, GCP 14), with selected metrics, network associations, Ready Nodes and regional backend health verified. This is representative acceptance, not exhaustive coverage of all cloud products or metrics.

- [Final function record](AZURE-GCP-ORIGINAL-FUNCTIONS-FINAL-20261012.md)
- [Original requirements and separate extensions](AZURE-GCP-FUNCTION-SCOPE-20261012.md)
- [Unified per-operation evidence ledger](verification.azure-gcp.original-functions-final.20261012.json)
- [Positive alert fixture summary](verification.azure-gcp.alert-acceptance-summary.20261012.json)
- [Cloud audit evidence boundary](AZURE-GCP-AUDIT-BOUNDARY-20261012.md)

Azure cost acceptance is deferred because the tested classic Sponsorship subscription does not support the selected APIs. Complete cross-service cloud audit reconciliation remains incomplete; existing BigQuery audit evidence does not establish coverage for every service. These conditions are recorded rather than marked passed.

The new GCP test bucket's nonzero capacity series and global backend live samples were separate gaps at this snapshot and passed in the linked follow-up. Node continuation remains an optional unimplemented extension. Alert rules are disabled metadata fixtures; alert firing and notification delivery are not claimed.

The two configuration examples require replacing `YOUR_*` placeholders. [original-scope.example.yaml](original-scope.example.yaml) uses port 8121 for resource/detail/metric reads; [alerts.example.yaml](alerts.example.yaml) uses port 8122 with monitoring-only profiles, including Azure `global`. Both load credentials from external environment/file settings. Do not commit credentials or a bearer token.

Reports summarize the prior live validation. The unified ledger was produced by reconciling saved evidence, not by rerunning every cloud operation. Evidence hashes refer to private local files; raw resources, IAM, billing, SQL, cloud audit events and credentials are deliberately excluded. Cloud project/subscription identifiers in these published materials are replaced with placeholders. Repository integration and a new checkout verification do not replace the retained deployed binary or configuration.
