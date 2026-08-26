package mcpserver

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"mcpcloud/internal/model"
)

type HelpInput struct {
	Topic    string         `json:"topic,omitempty" jsonschema:"Help topic: quickstart, dsl, profiles, resources, iam, metrics, costs, native, pagination, errors, or security; omit to list topics"`
	Provider model.Provider `json:"provider,omitempty" jsonschema:"Optional provider filter used in discovery steps and examples"`
	Source   model.Source   `json:"source,omitempty" jsonschema:"Optional DSL source used in examples: resources, iam, metrics, or costs"`
}

type HelpTopic struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type HelpStep struct {
	Tool      string         `json:"tool"`
	Purpose   string         `json:"purpose"`
	Arguments map[string]any `json:"arguments"`
}

type HelpExample struct {
	Name      string         `json:"name"`
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments"`
}

type HelpOutput struct {
	Topic           string        `json:"topic"`
	Summary         string        `json:"summary"`
	Topics          []HelpTopic   `json:"topics,omitempty"`
	RecommendedFlow []HelpStep    `json:"recommended_flow,omitempty"`
	Rules           []string      `json:"rules,omitempty"`
	Examples        []HelpExample `json:"examples,omitempty"`
	RelatedTopics   []string      `json:"related_topics,omitempty"`
}

var helpTopics = []HelpTopic{
	{Name: "quickstart", Description: "Discover profiles and schema, explain a DSL query, then execute it."},
	{Name: "dsl", Description: "Learn the bounded pipeline DSL sources, stages, and query shape."},
	{Name: "profiles", Description: "Interpret configured profile scope and readiness without exposing credentials."},
	{Name: "resources", Description: "Query normalized control-plane resource inventory."},
	{Name: "iam", Description: "Query cloud-native IAM metadata without enterprise directory access."},
	{Name: "metrics", Description: "Build provider-specific metric selector queries with a required time range."},
	{Name: "costs", Description: "Query original-currency cost records with a required date range."},
	{Name: "native", Description: "Discover and call registered typed native reads when the DSL is insufficient."},
	{Name: "pagination", Description: "Continue opaque short-lived cloud_query and native inventory cursors safely."},
	{Name: "errors", Description: "Interpret readiness, partial results, capability gaps, and retryable failures."},
	{Name: "security", Description: "Review the read-only and sensitive-data boundaries."},
}

func (s *Server) help(input HelpInput) (HelpOutput, error) {
	if len(input.Topic) > 64 {
		return HelpOutput{}, fmt.Errorf("help topic exceeds the allowed size")
	}
	if input.Provider != "" && !slices.Contains(model.Providers, input.Provider) {
		return HelpOutput{}, fmt.Errorf("unsupported provider %q", input.Provider)
	}
	if input.Source != "" && !slices.Contains(model.Sources, input.Source) {
		return HelpOutput{}, fmt.Errorf("unsupported source %q", input.Source)
	}
	topic := strings.TrimSpace(input.Topic)
	if topic == "" {
		topic = "index"
	} else if !helpTopicExists(topic) {
		return HelpOutput{}, fmt.Errorf("unsupported help topic %q", topic)
	}

	profile, exampleProvider := s.helpProfile(input.Provider)
	source := input.Source
	if source == "" {
		switch topic {
		case "iam":
			source = model.SourceIAM
		case "metrics":
			source = model.SourceMetrics
		case "costs":
			source = model.SourceCosts
		default:
			source = model.SourceResources
		}
	}
	query := helpQuery(source, profile, exampleProvider)
	discovery := s.helpDiscoveryFlow(input, query)

	switch topic {
	case "index":
		return HelpOutput{
			Topic:           topic,
			Summary:         "Choose a topic or follow the safe default workflow for a read-only cloud query.",
			Topics:          append([]HelpTopic(nil), helpTopics...),
			RecommendedFlow: discovery,
			Rules: []string{
				"Prefer cloud_query and the provider-neutral DSL for normal discovery.",
				"Use cloud_native_read only when cloud_schema lists a required typed operation and normalized DSL fields are insufficient.",
				"PROFILE_NAME and uppercase resource identifiers are placeholders when no matching configured value is available.",
				"Readiness and schema metadata are not proof of live cloud access; confirm with a narrow read and provider audit evidence.",
			},
			RelatedTopics: []string{"quickstart", "dsl", "security"},
		}, nil
	case "quickstart":
		return HelpOutput{
			Topic: topic, Summary: "Discover configuration and capability metadata, validate a query without cloud calls, then execute the narrow read.",
			RecommendedFlow: discovery,
			Rules: []string{
				"cloud_profiles reports configuration readiness and never returns credential contents.",
				"cloud_schema is authoritative for fields, capabilities, native allow-lists, and registered operations.",
				"Replace PROFILE_NAME and uppercase resource identifiers when the server cannot select a matching configured value.",
				"Call cloud_explain before a new or broad cloud_query.",
			},
			RelatedTopics: []string{"profiles", "dsl", "errors"},
		}, nil
	case "dsl":
		return HelpOutput{
			Topic: topic, Summary: "The DSL starts with resources, iam, metrics, or costs and applies a bounded pipeline.",
			Rules: []string{
				"Supported stages are scope, where, range, step, include native, fields, summarize, sort, and limit.",
				"Joins, subqueries, unions, regular expressions, custom functions, provider-native query strings, and writes are unsupported.",
				"Use include native before referencing an allow-listed native field.",
			},
			Examples:      helpQueryExamples(query),
			RelatedTopics: []string{"resources", "iam", "metrics", "costs"},
		}, nil
	case "profiles":
		arguments := map[string]any{}
		if input.Provider != "" {
			arguments["provider"] = input.Provider
		}
		return HelpOutput{
			Topic: topic, Summary: "Inspect named profiles, allowed scopes and regions, and credential configuration status without a cloud request.",
			RecommendedFlow: []HelpStep{{Tool: "cloud_profiles", Purpose: "List configured profile readiness metadata.", Arguments: arguments}},
			Rules: []string{
				"ready or configured_unverified does not prove permissions, service enablement, or representative data.",
				"Credentials and environment values are never returned.",
				"Confirm a profile with cloud_schema followed by a narrow cloud_query.",
			},
			RelatedTopics: []string{"quickstart", "errors", "security"},
		}, nil
	case "resources", "iam":
		return HelpOutput{
			Topic: topic, Summary: "Use the DSL to query normalized " + topic + " control-plane metadata across allowed profiles and scopes.",
			RecommendedFlow: []HelpStep{
				{Tool: "cloud_explain", Purpose: "Validate target expansion and residual filters without cloud calls.", Arguments: map[string]any{"query": query}},
				{Tool: "cloud_query", Purpose: "Execute the validated read-only query.", Arguments: map[string]any{"query": query}},
			},
			Rules:         []string{"Inventory coverage depends on provider indexing, permissions, and profile allow-lists.", "An empty page may still have a next_cursor."},
			Examples:      helpQueryExamples(query),
			RelatedTopics: []string{"dsl", "pagination", "errors"},
		}, nil
	case "metrics":
		metricQuery := helpQuery(model.SourceMetrics, profile, exampleProvider)
		return HelpOutput{
			Topic: topic, Summary: "Metrics are DSL-only reads with provider-specific selectors and a required bounded time range.",
			RecommendedFlow: []HelpStep{
				{Tool: "cloud_schema", Purpose: "Check the selected profile metric capability and prerequisites.", Arguments: helpSchemaArguments(input.Provider, model.SourceMetrics)},
				{Tool: "cloud_explain", Purpose: "Validate the selector, range, target scope, and region plan.", Arguments: map[string]any{"query": metricQuery}},
				{Tool: "cloud_query", Purpose: "Execute the metric query after replacing selector placeholders.", Arguments: map[string]any{"query": metricQuery}},
			},
			Rules:         []string{"Every OR branch requires an exact metric selector constraint.", "Metric ranges are limited to 31 days and step cannot be below one minute.", "Replace placeholder resource or dimension values in the example with values from the selected provider."},
			RelatedTopics: []string{"dsl", "profiles", "errors"},
		}, nil
	case "costs":
		costQuery := helpQuery(model.SourceCosts, profile, exampleProvider)
		return HelpOutput{
			Topic: topic, Summary: "Costs are DSL-only reads with a required date range and original provider currency.",
			RecommendedFlow: []HelpStep{
				{Tool: "cloud_schema", Purpose: "Check billing capability prerequisites for the selected profile.", Arguments: helpSchemaArguments(input.Provider, model.SourceCosts)},
				{Tool: "cloud_explain", Purpose: "Validate date range, target expansion, and currency grouping.", Arguments: map[string]any{"query": costQuery}},
				{Tool: "cloud_query", Purpose: "Execute the validated cost query.", Arguments: map[string]any{"query": costQuery}},
			},
			Rules:         []string{"Cost ranges are limited to 400 days.", "Amounts stay in the source currency; non-count amount aggregates must group by currency.", "GCP, Huawei, and Tencent have profile-specific billing prerequisites reported by cloud_schema."},
			RelatedTopics: []string{"dsl", "profiles", "errors"},
		}, nil
	case "native":
		schemaArgs := helpSchemaArguments(input.Provider, input.Source)
		return HelpOutput{
			Topic: topic, Summary: "Use a registered typed native read only when normalized DSL results are insufficient.",
			RecommendedFlow: []HelpStep{
				{Tool: "cloud_schema", Purpose: "Discover the exact registered operation and closed parameter schema.", Arguments: schemaArgs},
				{Tool: "cloud_native_read", Purpose: "Call the selected operation after replacing schema-derived placeholders.", Arguments: map[string]any{"profile": profile, "operation": "OPERATION_FROM_CLOUD_SCHEMA", "params": map[string]any{"REQUIRED_PARAMETER": "VALUE_FROM_USER_SCOPE"}}},
			},
			Rules:         []string{"Never invent an operation or parameter; both must come from cloud_schema.operations.", "Arbitrary URLs, HTTP methods, provider actions, raw queries, and write operations are rejected.", "Service detail operations return allow-listed normalized metadata and reject cursors."},
			RelatedTopics: []string{"resources", "pagination", "security"},
		}, nil
	case "pagination":
		return HelpOutput{
			Topic: topic, Summary: "Continue opaque cursors exactly as returned; do not inspect, combine, or convert them into provider tokens.",
			Examples: []HelpExample{
				{Name: "Initial query", Tool: "cloud_query", Arguments: map[string]any{"query": query, "page_size": 100}},
				{Name: "Next query page", Tool: "cloud_query", Arguments: map[string]any{"cursor": "NEXT_CURSOR_FROM_PREVIOUS_RESULT"}},
			},
			Rules:         []string{"A cloud_query continuation accepts only cursor.", "Cursors are short-lived, process-local, and invalid after expiry or restart.", "Native inventory cursors are passed back through cloud_native_read; fixed service detail operations do not paginate."},
			RelatedTopics: []string{"dsl", "native", "errors"},
		}, nil
	case "errors":
		return HelpOutput{
			Topic: topic, Summary: "Use structured status, coverage, per-target errors, and retryability instead of inferring success from rows alone.",
			Rules:         []string{"ok means all planned targets succeeded; partial preserves successful rows and identifies failed targets; failed means no successful result.", "not_configured and capability_unavailable require profile scope/options or service prerequisites, not blind retries.", "Retry only errors marked retryable, within server request and timeout bounds.", "configured_unverified is configuration metadata, not a live identity probe."},
			RelatedTopics: []string{"profiles", "quickstart", "security"},
		}, nil
	case "security":
		return HelpOutput{
			Topic: topic, Summary: "All tools are control-plane read-only and return normalized allow-listed metadata.",
			Rules:         []string{"Never pass credentials, tokens, private keys, passwords, connection strings, or service-account JSON as tool arguments.", "Object bodies, database rows and endpoints, log or message bodies, kubeconfigs, certificates, secrets, user data, and complete SDK responses are outside the contract.", "Use profile, account/project/subscription, region, and service allow-lists together with provider IAM.", "cloud_help, cloud_profiles, cloud_schema, and cloud_explain do not call cloud APIs."},
			RelatedTopics: []string{"profiles", "native", "errors"},
		}, nil
	default:
		return HelpOutput{}, fmt.Errorf("unsupported help topic %q", topic)
	}
}

func (s *Server) helpDiscoveryFlow(input HelpInput, query string) []HelpStep {
	profilesArgs := map[string]any{}
	if input.Provider != "" {
		profilesArgs["provider"] = input.Provider
	}
	return []HelpStep{
		{Tool: "cloud_profiles", Purpose: "Inspect configured profile scope and readiness metadata.", Arguments: profilesArgs},
		{Tool: "cloud_schema", Purpose: "Discover current capabilities, fields, and registered native operations.", Arguments: helpSchemaArguments(input.Provider, input.Source)},
		{Tool: "cloud_explain", Purpose: "Validate and plan the DSL without a cloud API call.", Arguments: map[string]any{"query": query}},
		{Tool: "cloud_query", Purpose: "Execute the validated read-only DSL query.", Arguments: map[string]any{"query": query}},
	}
}

func (s *Server) helpProfile(cloud model.Provider) (string, model.Provider) {
	for _, name := range s.registry.Profiles() {
		adapter, _ := s.registry.Get(name)
		if cloud == "" || adapter.Provider() == cloud {
			return name, adapter.Provider()
		}
	}
	return "PROFILE_NAME", cloud
}

func helpTopicExists(topic string) bool {
	for _, candidate := range helpTopics {
		if candidate.Name == topic {
			return true
		}
	}
	return false
}

func helpSchemaArguments(cloud model.Provider, source model.Source) map[string]any {
	arguments := map[string]any{}
	if cloud != "" {
		arguments["provider"] = cloud
	}
	if source != "" {
		arguments["source"] = source
	}
	return arguments
}

func helpQuery(source model.Source, profile string, cloud model.Provider) string {
	scope := " | scope profile in (" + strconv.Quote(profile) + ")"
	switch source {
	case model.SourceIAM:
		return "iam" + scope + " | fields provider, profile, principal, principal_type, resource_id, roles | limit 20"
	case model.SourceMetrics:
		return "metrics" + scope + " | range last 6h | where metric == " + strconv.Quote(helpMetricSelector(cloud)) + " | step 5m | summarize avg(value) by provider, profile, region | limit 100"
	case model.SourceCosts:
		return "costs" + scope + " | range last 168h | summarize sum(amount) by provider, currency, service | sort provider asc, currency asc | limit 100"
	default:
		filter := "domain == \"compute\" and kind == \"instance\""
		if cloud != "" {
			filter = "provider == " + strconv.Quote(string(cloud)) + " and " + filter
		}
		return "resources" + scope + " | where " + filter + " | fields provider, profile, region, id, name, state | sort provider asc, name asc | limit 20"
	}
}

func helpMetricSelector(cloud model.Provider) string {
	switch cloud {
	case model.ProviderGCP:
		return "compute.googleapis.com/instance/cpu/utilization?resource.instance_id=INSTANCE_ID"
	case model.ProviderVolcengine:
		return "NAMESPACE::SUBNAMESPACE::METRIC?resource_id=RESOURCE_ID"
	default:
		return "NAMESPACE::METRIC?resource_id=RESOURCE_ID"
	}
}

func helpQueryExamples(query string) []HelpExample {
	return []HelpExample{
		{Name: "Validate the query", Tool: "cloud_explain", Arguments: map[string]any{"query": query}},
		{Name: "Execute the query", Tool: "cloud_query", Arguments: map[string]any{"query": query}},
	}
}
