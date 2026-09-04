package providers

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"mcpcloud/internal/config"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

func capabilities(p model.Provider, inventoryOperation, metricOperation, costOperation string) []provider.Capability {
	resourceOperations := append([]string{inventoryOperation}, nativeProductOperationNames(p, model.SourceResources)...)
	resourceOperations = append(resourceOperations, instanceDetailOperationNames(p)...)
	resourceOperations = append(resourceOperations, deepDetailOperationNames(p)...)
	iamOperations := append([]string{inventoryOperation}, nativeProductOperationNames(p, model.SourceIAM)...)
	return []provider.Capability{
		{Provider: p, Source: model.SourceResources, Domains: provider.CoreDomains[:8], Kinds: provider.InventoryKinds, Operations: resourceOperations, Status: "inventory", Notes: "indexed control-plane inventory; actual kinds depend on provider indexing and account enablement"},
		{Provider: p, Source: model.SourceIAM, Domains: []string{"iam"}, Kinds: []string{"user", "role", "service_account", "policy", "binding"}, Operations: iamOperations, Status: "inventory", Notes: "IAM inventory coverage depends on the provider resource index"},
		{Provider: p, Source: model.SourceMetrics, Domains: []string{"monitoring"}, Kinds: []string{"metric"}, Operations: []string{metricOperation}, Status: "available", Notes: "read-only metric time-series connector; metric selectors and live permissions remain provider-specific"},
		{Provider: p, Source: model.SourceCosts, Domains: []string{"cost"}, Kinds: []string{"cost_record"}, Operations: []string{costOperation}, Status: "available", Notes: "read-only billing connector; data freshness and retention remain provider-specific"},
	}
}

type metricSelector struct {
	Raw        string
	Parts      []string
	Dimensions map[string]string
}

var selectorPartPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/ -]{0,255}$`)
var selectorDimensionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)
var nativeIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}$`)
var cloudRegionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func parseMetricSelector(raw string, minParts, maxParts int) (metricSelector, error) {
	selector := metricSelector{Raw: raw, Dimensions: map[string]string{}}
	metricPart, queryPart, _ := strings.Cut(raw, "?")
	selector.Parts = strings.Split(metricPart, "::")
	if len(selector.Parts) < minParts || len(selector.Parts) > maxParts {
		return metricSelector{}, fmt.Errorf("metric selector must contain %d to %d ::-separated parts", minParts, maxParts)
	}
	for _, part := range selector.Parts {
		if !selectorPartPattern.MatchString(part) || strings.TrimSpace(part) != part {
			return metricSelector{}, fmt.Errorf("metric selector contains an invalid identifier")
		}
	}
	if queryPart == "" {
		return selector, nil
	}
	values, err := url.ParseQuery(queryPart)
	if err != nil {
		return metricSelector{}, fmt.Errorf("metric selector dimensions are invalid")
	}
	if len(values) > 16 {
		return metricSelector{}, fmt.Errorf("metric selector supports at most 16 dimensions")
	}
	for name, items := range values {
		if !selectorDimensionPattern.MatchString(name) || len(items) != 1 || !validSelectorValue(items[0]) {
			return metricSelector{}, fmt.Errorf("metric selector contains an invalid dimension")
		}
		selector.Dimensions[name] = items[0]
	}
	return selector, nil
}

func validSelectorValue(value string) bool {
	if value == "" || len(value) > 256 || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func metricRow(p model.Provider, profile, selector, service, resourceID, region, scopeID string, timestamp time.Time, value float64, unit string, dimensions map[string]string) map[string]any {
	dimensionValues := make(map[string]any, len(dimensions))
	for name, item := range dimensions {
		dimensionValues[name] = item
	}
	row := baseRow(p, profile, resourceID, resourceID, service, "metric", region, scopeID, timestamp)
	row["domain"] = "monitoring"
	row["kind"] = "metric"
	row["metric"] = selector
	row["timestamp"] = timestamp.UTC().Format(time.RFC3339Nano)
	row["value"] = value
	row["unit"] = unit
	row["dimensions"] = dimensionValues
	row["observed_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	return row
}

func costRow(p model.Provider, profile, recordID, date, account, service, region string, amount float64, currency string) map[string]any {
	observed := time.Now().UTC()
	row := baseRow(p, profile, recordID, service, service, "cost_record", region, account, observed)
	if p == model.ProviderHuawei {
		scope := row["scope"].(map[string]any)
		delete(scope, "project_id")
		scope["account_id"] = scopeTail(account)
	}
	row["domain"] = "cost"
	row["kind"] = "cost_record"
	row["date"] = date
	row["amount"] = amount
	row["currency"] = currency
	return row
}

func parseAmount(value string) (float64, error) {
	amount, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return 0, fmt.Errorf("provider returned an invalid amount")
	}
	return amount, nil
}

type periodCursor struct {
	Period string `json:"p"`
	Offset int    `json:"o,omitempty"`
	Page   int    `json:"n,omitempty"`
}

func encodePeriodCursor(cursor periodCursor) string {
	data, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodePeriodCursor(token string) (periodCursor, error) {
	if token == "" {
		return periodCursor{}, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(data) > 256 {
		return periodCursor{}, fmt.Errorf("invalid provider cursor")
	}
	var cursor periodCursor
	if err := json.Unmarshal(data, &cursor); err != nil || cursor.Offset < 0 || cursor.Page < 0 {
		return periodCursor{}, fmt.Errorf("invalid provider cursor")
	}
	return cursor, nil
}

func monthStart(value time.Time) time.Time {
	value = value.UTC()
	return time.Date(value.Year(), value.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func nextMonth(value time.Time) time.Time { return monthStart(value).AddDate(0, 1, 0) }

func periodInRange(period string, start, end time.Time) (time.Time, error) {
	month, err := time.Parse("2006-01", period)
	if err != nil || month.Before(monthStart(start)) || !month.Before(end.UTC()) {
		return time.Time{}, fmt.Errorf("provider cursor is outside the requested date range")
	}
	return month, nil
}

func rangeRequired(req provider.QueryRequest, operation string) (time.Time, time.Time, error) {
	if req.Start == nil || req.End == nil || !req.Start.Before(*req.End) {
		return time.Time{}, time.Time{}, &provider.Error{Code: "invalid_range", Operation: operation, Message: "a valid start and end time are required"}
	}
	return req.Start.UTC(), req.End.UTC(), nil
}

func operation(name string, p model.Provider, service, description string, parameters map[string]any) provider.Operation {
	return provider.Operation{Name: name, Provider: p, Service: service, Description: description, Parameters: parameters}
}

func readiness(name string, p model.Provider, profile config.Profile, requiredEnv []string) model.ProfileStatus {
	status := model.ProfileStatus{Name: name, Provider: p, Ready: true, Status: "configured_unverified", Regions: append([]string(nil), profile.Regions...), Scopes: map[string]any{"organizations": profile.Scopes.Organizations, "accounts": profile.Scopes.Accounts, "projects": profile.Scopes.Projects, "subscriptions": profile.Scopes.Subscriptions, "tenants": profile.Scopes.Tenants}}
	if profile.Credential.Source != "env" {
		return status
	}
	for _, logical := range requiredEnv {
		envName := profile.Credential.Env[logical]
		if envName == "" {
			envName = logical
		}
		if os.Getenv(envName) == "" {
			status.Ready = false
			status.Status = "missing_credentials"
			status.Error = fmt.Sprintf("required environment variable %s is not set", envName)
			return status
		}
	}
	return status
}

func envValue(profile config.Profile, logical, fallback string) string {
	name := profile.Credential.Env[logical]
	if name == "" {
		name = fallback
	}
	return os.Getenv(name)
}

func baseRow(p model.Provider, profile, id, name, service, nativeType, region, account string, observed time.Time) map[string]any {
	domain, kind := classify(service, nativeType)
	scope := map[string]any{}
	switch p {
	case model.ProviderGCP:
		scope["project_id"] = scopeTail(account)
	case model.ProviderAzure:
		scope["subscription_id"] = scopeTail(account)
	case model.ProviderHuawei:
		scope["project_id"] = scopeTail(account)
	default:
		scope["account_id"] = scopeTail(account)
	}
	return map[string]any{
		"provider": string(p), "profile": profile, "domain": domain, "service": service, "kind": kind,
		"id": id, "name": name, "scope": scope, "region": region, "zone": "", "state": "",
		"tags": map[string]any{}, "observed_at": observed.UTC().Format(time.RFC3339Nano), "attributes": map[string]any{},
		"native": map[string]any{"resource_type": nativeType},
	}
}

func classify(service, nativeType string) (string, string) {
	v := strings.ToLower(service + " " + nativeType)
	native := strings.ToLower(nativeType)
	serviceName := strings.ToLower(service)
	switch {
	case containsAny(v, "iam", "ram", "cam", "role", "policy", "serviceaccount", "service_account"):
		if strings.Contains(v, "policy") {
			return "iam", "policy"
		}
		if strings.Contains(v, "role") {
			return "iam", "role"
		}
		if strings.Contains(v, "serviceaccount") || strings.Contains(v, "service_account") {
			return "iam", "service_account"
		}
		return "iam", "user"
	case containsAny(v, "kubernetes", "eks", "gke", "aks", "ack", "cce", "tke", "vke", "containerservice", "container.googleapis.com", "managedcluster", "::cs::cluster"):
		if containsAny(v, "nodepool", "node_pool", "node-pool", "nodegroup", "node_group", "agentpool") {
			return "kubernetes", "node_pool"
		}
		return "kubernetes", "cluster"
	case containsAny(v, "rds", "sql", "database", "dbinstance", "polardb", "gaussdb", "alloydb", "spanner", "dbforpostgresql", "dbformysql", "qcs::cdb"):
		return "database", "database"
	case containsAny(v, "redis", "elasticache", "memorystore", "dcs", "microsoft.cache"):
		return "database", "cache"
	case containsAny(native, "disk", "volume", "ebs", "evs", "cbs") || containsAny(serviceName, "disk", "volume", "evs", "cbs"):
		return "compute", "disk"
	case containsAny(v, "s3", "storage", "bucket", "oss", "obs", "cos", "tos"):
		return "storage", "bucket"
	case containsAny(v, "vpc", "network", "subnet", "securitygroup", "security_group", "firewall", "eip", "publicip", "public_ip", "loadbalancer", "load_balancer", "elb", "clb", "slb"):
		switch {
		case containsAny(v, "subnet", "vswitch"):
			return "network", "subnet"
		case containsAny(v, "securitygroup", "security_group", "firewall"):
			return "network", "security_group"
		case containsAny(v, "eip", "publicip", "public_ip"):
			return "network", "public_ip"
		case containsAny(v, "loadbalancer", "load_balancer", "elb", "clb", "slb"):
			return "network", "load_balancer"
		default:
			return "network", "network"
		}
	case containsAny(v, "cloudwatch", "monitor", "alarm", "alertpolicy", "metricalert", "scheduledqueryrule", "activitylogalert", "cloud eye", "ces"):
		return "monitoring", "alarm"
	case containsAny(v, "log", "logging", "sls", "lts", "cls", "tls", "operationalinsights", "applicationinsights", "cloudtrail"):
		switch {
		case containsAny(v, "index"):
			return "logging", "log_index"
		case containsAny(v, "sink", "delivery", "export"):
			return "logging", "log_sink"
		case containsAny(v, "project", "workspace"):
			return "logging", "log_project"
		default:
			return "logging", "log_group"
		}
	case containsAny(v, "ec2", "compute", "instance", "ecs", "cvm", "virtualmachine", "virtual_machine"):
		return "compute", "instance"
	default:
		return "other", "resource"
	}
}

func containsAny(value string, values ...string) bool {
	for _, candidate := range values {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}

func stringIn(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func hasExactRegion(regions []string) bool {
	for _, region := range regions {
		if region != "" && region != "*" {
			return true
		}
	}
	return false
}

func scopeTail(value string) string {
	if index := strings.LastIndex(value, "/"); index >= 0 && index+1 < len(value) {
		return value[index+1:]
	}
	return value
}

func lastName(id string) string {
	id = strings.TrimRight(id, "/")
	for _, separator := range []string{"/", ":"} {
		if i := strings.LastIndex(id, separator); i >= 0 && i+1 < len(id) {
			id = id[i+1:]
		}
	}
	return id
}

func unsupportedSource(source model.Source, operation string) error {
	return &provider.Error{Code: "capability_unavailable", Operation: operation, Message: fmt.Sprintf("source %s is not implemented by this provider adapter", source)}
}

func domainPage(page provider.Page, domain string) provider.Page {
	rows := page.Rows[:0]
	for _, row := range page.Rows {
		if row["domain"] == domain {
			rows = append(rows, row)
		}
	}
	page.Rows = rows
	return page
}

func nativeString(params map[string]any, key string) (string, error) {
	value, ok := params[key]
	if !ok {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("parameter %s must be a string", key)
	}
	return text, nil
}

func nativeStrings(params map[string]any, key string) ([]string, error) {
	value, ok := params[key]
	if !ok {
		return nil, nil
	}
	switch items := value.(type) {
	case []string:
		return append([]string(nil), items...), nil
	case []any:
		out := make([]string, 0, len(items))
		for _, item := range items {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("parameter %s values must be strings", key)
			}
			out = append(out, text)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("parameter %s must be an array", key)
	}
}

func nativeIdentifier(params map[string]any, key string) (string, error) {
	value, err := nativeString(params, key)
	if err != nil || value == "" {
		return value, err
	}
	return validateNativeIdentifier(key, value)
}

func validateNativeIdentifier(key, value string) (string, error) {
	if !nativeIdentifierPattern.MatchString(value) {
		return "", fmt.Errorf("parameter %s contains an invalid identifier", key)
	}
	return value, nil
}

func nativeLiteral(params map[string]any, key string) (string, error) {
	value, err := nativeString(params, key)
	if err != nil || value == "" {
		return value, err
	}
	if len(value) > 1024 || strings.ContainsAny(value, "\r\n\x00") {
		return "", fmt.Errorf("parameter %s contains an invalid value", key)
	}
	return value, nil
}

func restrictValues(allowed, requested []string, operation, label string) ([]string, error) {
	if len(allowed) == 0 {
		return append([]string(nil), requested...), nil
	}
	if len(requested) == 0 {
		return append([]string(nil), allowed...), nil
	}
	for _, value := range requested {
		found := false
		for _, candidate := range allowed {
			if value == candidate {
				found = true
				break
			}
		}
		if !found {
			return nil, &provider.Error{Code: "scope_not_allowed", Operation: operation, Message: fmt.Sprintf("%s %q is outside the profile allowlist", label, value)}
		}
	}
	return append([]string(nil), requested...), nil
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
