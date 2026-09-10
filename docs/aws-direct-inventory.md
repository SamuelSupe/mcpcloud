# AWS direct regional inventory

Use `cloud_native_read` with profile, exact region, operation, empty params, and page_size. These operations call product APIs directly; existing `aws.compute.list_instances` and other product search operations continue to use Resource Explorer indexes.

| Operations | Native page size |
| --- | --- |
| `aws.ec2.describe_instances`, `describe_volumes`, `describe_vpcs`, `describe_subnets`, `describe_security_groups`, `describe_nat_gateways` (all with `aws.ec2.` prefix) | 5–1000 |
| `aws.ec2.describe_addresses` | Unpaginated; returns all addresses; cursor rejected |
| `aws.rds.describe_db_instances` | 20–100 |
| `aws.eks.list_clusters` | 1–100 |
| `aws.elb.describe_load_balancers`, `aws.elbv2.describe_load_balancers` | 1–400 |
| `aws.s3.list_buckets` | 1–10000 |

The server's configured maximum page size also applies. Default page size is 100. Pass `next_cursor` back as `cursor`, keeping the profile, operation, region, and page size unchanged. Continue until the token is empty, even if a page has no rows. EC2 instance page sizes apply to the AWS response and are not a local slicing guarantee.

Exactly one account scope is required, as for existing AWS detail operations. Credentials must belong to that account (or assume its configured role). The adapter uses the profile account as normalized metadata; it does not perform STS identity verification on each request. Region allowlists and service filters still apply. Unknown params are rejected before contacting AWS.

S3 lists general-purpose buckets only, using the requested regional endpoint and AWS BucketRegion filter. It does not read object bodies, keys, or directory buckets. EKS returns cluster names; use the existing describe_cluster operation for details. ELB lists load balancer metadata, not listeners or target health.

Example:

```json
{"profile":"aws-cn-readonly","operation":"aws.ec2.describe_instances","region":"cn-northwest-1","params":{},"page_size":5}
```

Rows contain explicitly selected metadata. Product IDs replace indexed ARNs where appropriate (EC2 IDs, RDS identifiers, EKS names, S3 names); ELBv2 retains its ARN. Do not compare product IDs directly with Resource Explorer ARNs without converting identifiers.
