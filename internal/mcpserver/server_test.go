package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpcloud/internal/config"
	"mcpcloud/internal/engine"
	"mcpcloud/internal/model"
	"mcpcloud/internal/probe"
	"mcpcloud/internal/provider"
)

type mcpTestAdapter struct {
	profile     string
	provider    model.Provider
	rows        []map[string]any
	operations  []provider.Operation
	status      model.ProfileStatus
	nativePage  provider.Page
	queryCalls  int
	nativeCalls int
	lastNative  provider.NativeRequest
	mu          sync.Mutex
}

func (a *mcpTestAdapter) Provider() model.Provider { return a.provider }
func (a *mcpTestAdapter) Profile() string          { return a.profile }
func (a *mcpTestAdapter) Capabilities() []provider.Capability {
	return []provider.Capability{{
		Provider:   a.provider,
		Source:     model.SourceResources,
		Domains:    []string{"compute"},
		Kinds:      []string{"instance"},
		Operations: operationNames(a.operations),
		Status:     "available",
	}}
}
func (a *mcpTestAdapter) Operations() []provider.Operation { return a.operations }
func (a *mcpTestAdapter) Readiness(context.Context) model.ProfileStatus {
	if a.status.Name == "" {
		return model.ProfileStatus{Name: a.profile, Provider: a.provider, Ready: true, Status: "ready", Scopes: map[string]any{"accounts": []string{"account-1"}}}
	}
	return a.status
}
func (a *mcpTestAdapter) Query(context.Context, provider.QueryRequest) (provider.Page, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.queryCalls++
	rows := make([]map[string]any, 0, len(a.rows))
	for _, row := range a.rows {
		copyRow := make(map[string]any, len(row))
		for key, value := range row {
			copyRow[key] = value
		}
		rows = append(rows, copyRow)
	}
	return provider.Page{Rows: rows, Scanned: len(rows), Requests: 1}, nil
}
func (a *mcpTestAdapter) NativeRead(_ context.Context, request provider.NativeRequest) (provider.Page, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nativeCalls++
	a.lastNative = request
	return a.nativePage, nil
}

func operationNames(operations []provider.Operation) []string {
	result := make([]string, 0, len(operations))
	for _, operation := range operations {
		result = append(result, operation.Name)
	}
	return result
}

func newMCPTestServer(t *testing.T) (*Server, *mcpTestAdapter, *mcpTestAdapter) {
	t.Helper()
	aws := &mcpTestAdapter{
		profile:  "aws-prod",
		provider: model.ProviderAWS,
		rows:     []map[string]any{{"provider": "aws", "profile": "aws-prod", "domain": "compute", "id": "i-1"}},
		operations: []provider.Operation{{
			Name:        "aws.inventory.search",
			Provider:    model.ProviderAWS,
			Service:     "inventory",
			Description: "search resources",
			Parameters:  map[string]any{"query": map[string]any{"type": "string"}},
		}},
		nativePage: provider.Page{Rows: []map[string]any{{"id": "i-1"}}, Scanned: 1, Requests: 1},
	}
	gcp := &mcpTestAdapter{
		profile:  "gcp-prod",
		provider: model.ProviderGCP,
		status:   model.ProfileStatus{Name: "gcp-prod", Provider: model.ProviderGCP, Ready: false, Status: "missing_credentials", Error: "credential environment variable is not set"},
		operations: []provider.Operation{{
			Name:        "gcp.inventory.search",
			Provider:    model.ProviderGCP,
			Service:     "inventory",
			Description: "search resources",
			Parameters:  map[string]any{"query": map[string]any{"type": "string"}},
		}},
	}
	registry, err := provider.NewRegistry(aws, gcp)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	return New(config.Default(), registry, nil), aws, gcp
}

func connectMCP(t *testing.T, server *Server) (*mcp.ClientSession, context.Context) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	client := mcp.NewClient(&mcp.Implementation{Name: "mcpcloud-test-client", Version: "test"}, nil)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.mcp.Connect(ctx, serverTransport, nil)
	if err != nil {
		cancel()
		t.Fatalf("server Connect() error = %v", err)
	}
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		_ = serverSession.Close()
		cancel()
		t.Fatalf("client Connect() error = %v", err)
	}
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = serverSession.Close()
		cancel()
	})
	return clientSession, ctx
}

func decodeStructured[T any](t *testing.T, result *mcp.CallToolResult) T {
	t.Helper()
	var output T
	payload, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if err := json.Unmarshal(payload, &output); err != nil {
		t.Fatalf("unmarshal structured content %s: %v", payload, err)
	}
	return output
}

func resultText(result *mcp.CallToolResult) string {
	var parts []string
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

type readyzTestRunner struct {
	profiles []string
	report   probe.Report
	err      error
}

func (r readyzTestRunner) Profiles() []string { return append([]string(nil), r.profiles...) }
func (r readyzTestRunner) Run(context.Context, []string) (probe.Report, error) {
	return r.report, r.err
}

func TestReadyzReportsReadyDegradedAndNotReadyProbeStates(t *testing.T) {
	tests := []struct {
		name         string
		report       probe.Report
		runnerErr    error
		wantStatus   string
		wantCode     int
		wantReady    int
		wantDegraded int
		wantNotReady int
	}{
		{
			name:       "ready",
			report:     probe.Report{Status: probe.StatusReady, Profiles: []probe.ProfileResult{{Status: probe.StatusReady}, {Status: probe.StatusReady}}},
			wantStatus: probe.StatusReady, wantCode: http.StatusOK, wantReady: 2,
		},
		{
			name:       "degraded",
			report:     probe.Report{Status: probe.StatusDegraded, Profiles: []probe.ProfileResult{{Status: probe.StatusReady}, {Status: probe.StatusDegraded}}},
			wantStatus: probe.StatusDegraded, wantCode: http.StatusOK, wantReady: 1, wantDegraded: 1,
		},
		{
			name:       "not ready",
			report:     probe.Report{Status: probe.StatusNotReady, Profiles: []probe.ProfileResult{{Status: probe.StatusNotReady}}},
			wantStatus: probe.StatusNotReady, wantCode: http.StatusServiceUnavailable, wantNotReady: 1,
		},
		{
			name:       "probe error",
			runnerErr:  errors.New("sensitive provider response"),
			wantStatus: probe.StatusNotReady, wantCode: http.StatusServiceUnavailable,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, _, _ := newMCPTestServer(t)
			server.readiness = probe.NewCoordinator(readyzTestRunner{profiles: []string{"aws-prod"}, report: tt.report, err: tt.runnerErr}, time.Minute, time.Second)
			recorder := httptest.NewRecorder()
			server.readyz(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			if recorder.Code != tt.wantCode {
				t.Fatalf("readyz status code = %d, want %d body=%s", recorder.Code, tt.wantCode, recorder.Body.String())
			}
			var output map[string]any
			if err := json.Unmarshal(recorder.Body.Bytes(), &output); err != nil {
				t.Fatalf("readyz JSON = %q: %v", recorder.Body.String(), err)
			}
			if output["status"] != tt.wantStatus {
				t.Fatalf("readyz status = %#v, want %q", output["status"], tt.wantStatus)
			}
			if tt.runnerErr != nil {
				if output["error_code"] != "probe_failed" {
					t.Fatalf("readyz error_code = %#v, want probe_failed", output["error_code"])
				}
				return
			}
			if int(output["ready"].(float64)) != tt.wantReady || int(output["degraded"].(float64)) != tt.wantDegraded || int(output["not_ready"].(float64)) != tt.wantNotReady {
				t.Fatalf("readyz counts = %#v, want ready=%d degraded=%d not_ready=%d", output, tt.wantReady, tt.wantDegraded, tt.wantNotReady)
			}
		})
	}
}

func requireToolError(t *testing.T, result *mcp.CallToolResult, err error, want string) {
	t.Helper()
	if err != nil {
		t.Fatalf("CallTool() protocol error = %v, want tool error result", err)
	}
	if !result.IsError {
		t.Fatalf("CallTool() result = %#v, want IsError", result)
	}
	if !strings.Contains(resultText(result), want) {
		t.Fatalf("tool error text = %q, want substring %q", resultText(result), want)
	}
}

func TestServerRegistersReadOnlyToolsAndRejectsCloudQueryInputAmbiguity(t *testing.T) {
	server, _, _ := newMCPTestServer(t)
	session, ctx := connectMCP(t, server)

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	gotNames := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		gotNames = append(gotNames, tool.Name)
	}
	sort.Strings(gotNames)
	wantNames := []string{"cloud_explain", "cloud_help", "cloud_native_read", "cloud_profiles", "cloud_query", "cloud_schema"}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("registered tools = %#v, want %#v", gotNames, wantNames)
	}

	for _, arguments := range []map[string]any{
		{"query": "resources", "cursor": "opaque-cursor"},
		{},
	} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "cloud_query", Arguments: arguments})
		requireToolError(t, result, err, "exactly one of query or cursor")
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "cloud_query", Arguments: map[string]any{"query": `resources | scope profile in ("aws-prod")`}})
	if err != nil || result.IsError {
		t.Fatalf("valid cloud_query result = (%#v, %v), want success", result, err)
	}
	output := decodeStructured[model.QueryResult](t, result)
	if output.Status != "ok" || len(output.Rows) != 1 || output.Rows[0]["id"] != "i-1" {
		t.Fatalf("cloud_query output = %#v, want one successful row", output)
	}
}

func TestCloudHelpRegistersSixthToolAndReturnsIndexWithoutProviderCalls(t *testing.T) {
	server, aws, gcp := newMCPTestServer(t)
	session, ctx := connectMCP(t, server)

	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "cloud_help", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("cloud_help empty result = (%#v, %v), want index success", result, err)
	}
	help := decodeStructured[map[string]any](t, result)
	if help["topic"] != "index" || strings.TrimSpace(stringValue(help["summary"])) == "" {
		t.Fatalf("cloud_help empty output = %#v, want index topic and summary", help)
	}
	if len(anyStrings(help["topics"])) == 0 || len(anyStrings(help["recommended_flow"])) == 0 {
		t.Fatalf("cloud_help empty output = %#v, want topics and recommended flow", help)
	}
	assertNoProviderCalls(t, aws, gcp)
}

func TestCloudHelpQuickstartAndFilteredExamplesUsePublicInputs(t *testing.T) {
	server, aws, gcp := newMCPTestServer(t)
	session, ctx := connectMCP(t, server)

	quickstart := callCloudHelp(t, session, ctx, map[string]any{"topic": "quickstart"})
	if quickstart["topic"] != "quickstart" {
		t.Fatalf("quickstart topic = %#v, want quickstart", quickstart["topic"])
	}
	flow := strings.Join(anyStrings(quickstart["recommended_flow"]), "\n")
	last := -1
	for _, expected := range []string{"profiles", "schema", "explain", "query"} {
		position := strings.Index(strings.ToLower(flow), expected)
		if position < 0 || position <= last {
			t.Fatalf("quickstart recommended_flow = %q, want profiles -> schema -> explain -> query", flow)
		}
		last = position
	}
	if !strings.Contains(mustJSON(t, quickstart), "aws-prod") {
		t.Fatalf("quickstart output = %#v, want an existing aws-prod profile example", quickstart)
	}

	filtered := callCloudHelp(t, session, ctx, map[string]any{"topic": "resources", "provider": "aws", "source": "resources"})
	encoded := mustJSON(t, filtered)
	if filtered["topic"] != "resources" || !strings.Contains(encoded, "aws") || !strings.Contains(encoded, "resources") {
		t.Fatalf("filtered resources help = %s, want provider=aws and source=resources in examples", encoded)
	}
	examples, ok := filtered["examples"].([]any)
	if !ok {
		t.Fatalf("filtered resources examples = %#v, want structured examples", filtered["examples"])
	}
	queryWithFilters := false
	for _, item := range examples {
		example, ok := item.(map[string]any)
		if !ok {
			continue
		}
		arguments, ok := example["arguments"].(map[string]any)
		if !ok {
			continue
		}
		query, _ := arguments["query"].(string)
		if strings.HasPrefix(query, "resources") && strings.Contains(query, `provider == "aws"`) {
			queryWithFilters = true
			break
		}
	}
	if !queryWithFilters {
		t.Fatalf("filtered resources examples = %s, want a resources query constrained to provider=aws", encoded)
	}
	assertNoProviderCalls(t, aws, gcp)
}

func TestCloudHelpIAMAndCostsQueriesAreValidDSLForTheirSource(t *testing.T) {
	server, aws, gcp := newMCPTestServer(t)
	session, ctx := connectMCP(t, server)
	for _, tt := range []struct {
		topic  string
		source model.Source
	}{
		{topic: "iam", source: model.SourceIAM},
		{topic: "costs", source: model.SourceCosts},
	} {
		t.Run(tt.topic, func(t *testing.T) {
			help := callCloudHelp(t, session, ctx, map[string]any{"topic": tt.topic})
			query := firstHelpQuery(help)
			if query == "" {
				t.Fatalf("%s help = %#v, want a generated cloud_explain query", tt.topic, help)
			}
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "cloud_explain", Arguments: map[string]any{"query": query}})
			if err != nil || result.IsError {
				t.Fatalf("cloud_explain(%s help query) = (%#v, %v), want valid DSL", tt.topic, result, err)
			}
			explain := decodeStructured[engine.ExplainResult](t, result)
			if explain.Source != tt.source {
				t.Fatalf("%s help query = %q, explain source = %q, want %q", tt.topic, query, explain.Source, tt.source)
			}
		})
	}
	assertNoProviderCalls(t, aws, gcp)
}

func TestCloudHelpRejectsUnknownTopicProviderAndSource(t *testing.T) {
	server, aws, gcp := newMCPTestServer(t)
	session, ctx := connectMCP(t, server)
	for _, tt := range []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "topic", args: map[string]any{"topic": "not-a-topic"}, want: "topic"},
		{name: "explicit index", args: map[string]any{"topic": "index"}, want: "topic"},
		{name: "provider", args: map[string]any{"provider": "not-a-provider"}, want: "provider"},
		{name: "source", args: map[string]any{"source": "not-a-source"}, want: "source"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "cloud_help", Arguments: tt.args})
			requireToolError(t, result, err, tt.want)
		})
	}
	assertNoProviderCalls(t, aws, gcp)
}

func callCloudHelp(t *testing.T, session *mcp.ClientSession, ctx context.Context, arguments map[string]any) map[string]any {
	t.Helper()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "cloud_help", Arguments: arguments})
	if err != nil || result.IsError {
		t.Fatalf("cloud_help(%#v) = (%#v, %v), want success", arguments, result, err)
	}
	return decodeStructured[map[string]any](t, result)
}

func anyStrings(value any) []string {
	items, ok := value.([]any)
	if !ok {
		if strings.TrimSpace(stringValue(value)) == "" {
			return nil
		}
		return []string{stringValue(value)}
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		text := strings.TrimSpace(stringValue(item))
		if object, ok := item.(map[string]any); ok {
			for _, key := range []string{"name", "tool", "topic"} {
				if candidate := strings.TrimSpace(stringValue(object[key])); candidate != "" {
					text = candidate
					break
				}
			}
		}
		if text != "" {
			result = append(result, text)
		}
	}
	return result
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal help output: %v", err)
	}
	return string(payload)
}

func firstHelpQuery(value any) string {
	switch item := value.(type) {
	case map[string]any:
		if arguments, ok := item["arguments"].(map[string]any); ok {
			if query, ok := arguments["query"].(string); ok && query != "" {
				return query
			}
		}
		for _, nested := range item {
			if query := firstHelpQuery(nested); query != "" {
				return query
			}
		}
	case []any:
		for _, nested := range item {
			if query := firstHelpQuery(nested); query != "" {
				return query
			}
		}
	}
	return ""
}

func assertNoProviderCalls(t *testing.T, adapters ...*mcpTestAdapter) {
	t.Helper()
	for _, adapter := range adapters {
		adapter.mu.Lock()
		queryCalls, nativeCalls := adapter.queryCalls, adapter.nativeCalls
		adapter.mu.Unlock()
		if queryCalls != 0 || nativeCalls != 0 {
			t.Fatalf("cloud_help triggered %s provider calls: Query=%d NativeRead=%d", adapter.profile, queryCalls, nativeCalls)
		}
	}
}

func TestCloudExplainDoesNotCallProviderQuery(t *testing.T) {
	server, aws, _ := newMCPTestServer(t)
	session, ctx := connectMCP(t, server)

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "cloud_explain",
		Arguments: map[string]any{"query": `resources | scope profile in ("aws-prod") | where state == "running"`},
	})
	if err != nil || result.IsError {
		t.Fatalf("cloud_explain result = (%#v, %v), want success", result, err)
	}
	explain := decodeStructured[engine.ExplainResult](t, result)
	if explain.Source != model.SourceResources || len(explain.Targets) != 1 || len(explain.Residual) != 1 {
		t.Fatalf("cloud_explain output = %#v, want target and residual filter", explain)
	}
	aws.mu.Lock()
	queryCalls := aws.queryCalls
	aws.mu.Unlock()
	if queryCalls != 0 {
		t.Fatalf("provider Query() calls during cloud_explain = %d, want 0", queryCalls)
	}
}

func TestCloudProfilesAndSchemaExposeMetadataWithoutCredentialValues(t *testing.T) {
	server, _, _ := newMCPTestServer(t)
	session, ctx := connectMCP(t, server)

	profilesResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "cloud_profiles",
		Arguments: map[string]any{"provider": "aws"},
	})
	if err != nil || profilesResult.IsError {
		t.Fatalf("cloud_profiles result = (%#v, %v), want success", profilesResult, err)
	}
	profiles := decodeStructured[ProfilesOutput](t, profilesResult)
	if len(profiles.Profiles) != 1 || profiles.Profiles[0].Name != "aws-prod" || profiles.Profiles[0].Provider != model.ProviderAWS {
		t.Fatalf("cloud_profiles output = %#v, want filtered AWS profile", profiles)
	}
	if strings.Contains(resultText(profilesResult), "AWS_SECRET_ACCESS_KEY") || strings.Contains(resultText(profilesResult), "actual-secret") {
		t.Fatalf("cloud_profiles response contains credential material: %q", resultText(profilesResult))
	}

	schemaResult, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "cloud_schema", Arguments: map[string]any{}})
	if err != nil || schemaResult.IsError {
		t.Fatalf("cloud_schema result = (%#v, %v), want success", schemaResult, err)
	}
	schema := decodeStructured[SchemaOutput](t, schemaResult)
	if !reflect.DeepEqual(schema.Sources, model.Sources) {
		t.Fatalf("cloud_schema sources = %#v, want %#v", schema.Sources, model.Sources)
	}
	if len(schema.Fields) == 0 || len(schema.Operators) == 0 || len(schema.Stages) == 0 {
		t.Fatalf("cloud_schema omitted DSL metadata: %#v", schema)
	}
	operationNames := make([]string, 0, len(schema.Operations))
	for _, operation := range schema.Operations {
		operationNames = append(operationNames, operation.Name)
		if strings.Contains(strings.ToLower(operation.Name), "delete") || strings.Contains(strings.ToLower(operation.Name), "write") {
			t.Fatalf("cloud_schema exposed write-like operation %q", operation.Name)
		}
	}
	if !reflect.DeepEqual(operationNames, []string{"aws.inventory.search", "gcp.inventory.search"}) {
		t.Fatalf("cloud_schema operations = %#v, want registered read operations", operationNames)
	}
}

func TestCloudSchemaProvidesBaselineMatrixAndFiltersBothCapabilitySets(t *testing.T) {
	server, _, _ := newMCPTestServer(t)
	session, ctx := connectMCP(t, server)

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "cloud_schema",
		Arguments: map[string]any{
			"source":   "resources",
			"provider": "aws",
			"kind":     "instance",
		},
	})
	if err != nil || result.IsError {
		t.Fatalf("filtered cloud_schema result = (%#v, %v), want success", result, err)
	}
	filtered := decodeStructured[SchemaOutput](t, result)
	if len(filtered.CapabilityMatrix) != 1 || filtered.CapabilityMatrix[0].Provider != model.ProviderAWS || filtered.CapabilityMatrix[0].Source != model.SourceResources {
		t.Fatalf("filtered capability matrix = %#v, want one AWS resources baseline", filtered.CapabilityMatrix)
	}
	if len(filtered.Capabilities) != 1 || filtered.Capabilities[0].Provider != model.ProviderAWS || filtered.Capabilities[0].Source != model.SourceResources || filtered.Capabilities[0].Profile != "aws-prod" {
		t.Fatalf("filtered profile capabilities = %#v, want one AWS resources profile capability", filtered.Capabilities)
	}

	emptyRegistry, err := provider.NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry(empty) error = %v", err)
	}
	emptyServer := New(config.Default(), emptyRegistry, nil)
	emptySession, emptyCtx := connectMCP(t, emptyServer)
	emptyResult, err := emptySession.CallTool(emptyCtx, &mcp.CallToolParams{Name: "cloud_schema", Arguments: map[string]any{}})
	if err != nil || emptyResult.IsError {
		t.Fatalf("empty-registry cloud_schema result = (%#v, %v), want success", emptyResult, err)
	}
	empty := decodeStructured[SchemaOutput](t, emptyResult)
	wantMatrix := len(model.Providers) * len(model.Sources)
	if len(empty.CapabilityMatrix) != wantMatrix {
		t.Fatalf("empty-registry capability matrix length = %d, want %d", len(empty.CapabilityMatrix), wantMatrix)
	}
	if len(empty.Capabilities) != 0 {
		t.Fatalf("empty-registry profile capabilities = %#v, want none", empty.Capabilities)
	}
	seen := map[string]bool{}
	for _, capability := range empty.CapabilityMatrix {
		key := string(capability.Provider) + "/" + string(capability.Source)
		if seen[key] {
			t.Fatalf("baseline capability matrix contains duplicate %s", key)
		}
		seen[key] = true
	}
}

func TestCloudSchemaAndNativeReadExposeRegisteredProductOperation(t *testing.T) {
	productName := "aws.compute.list_instances"
	adapter := &mcpTestAdapter{
		profile:  "aws-prod",
		provider: model.ProviderAWS,
		operations: []provider.Operation{{
			Name:       productName,
			Provider:   model.ProviderAWS,
			Service:    "resourceexplorer2",
			Parameters: map[string]any{},
		}},
		nativePage: provider.Page{
			Rows:      []map[string]any{{"domain": "compute", "kind": "instance", "id": "i-1"}},
			NextToken: "product-next",
			Scanned:   7,
			Requests:  2,
		},
	}
	registry, err := provider.NewRegistry(adapter)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	session, ctx := connectMCP(t, New(config.Default(), registry, nil))

	schemaResult, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "cloud_schema", Arguments: map[string]any{}})
	if err != nil || schemaResult.IsError {
		t.Fatalf("cloud_schema result = (%#v, %v), want success", schemaResult, err)
	}
	schema := decodeStructured[SchemaOutput](t, schemaResult)
	var product provider.Operation
	found := false
	for _, operation := range schema.Operations {
		if operation.Name == productName {
			product = operation
			found = true
			break
		}
	}
	if !found || product.Service != "resourceexplorer2" {
		t.Fatalf("cloud_schema product operation = %#v, want registered AWS resourceexplorer2 operation", product)
	}

	nativeResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "cloud_native_read",
		Arguments: map[string]any{
			"profile":   "aws-prod",
			"operation": productName,
			"page_size": 1,
		},
	})
	if err != nil || nativeResult.IsError {
		t.Fatalf("cloud_native_read product result = (%#v, %v), want success", nativeResult, err)
	}
	native := decodeStructured[NativeOutput](t, nativeResult)
	if native.RequestID == "" || len(native.Rows) != 1 || native.Rows[0]["kind"] != "instance" || native.NextCursor != "product-next" || native.Scanned != 7 || native.Requests != 2 {
		t.Fatalf("cloud_native_read product output = %#v, want product row/cursor/stats", native)
	}
	adapter.mu.Lock()
	lastRequest := adapter.lastNative
	callCount := adapter.nativeCalls
	adapter.mu.Unlock()
	if callCount != 1 || lastRequest.Operation != productName || lastRequest.Limit != 1 {
		t.Fatalf("product NativeRead request = %#v calls=%d, want typed registered request", lastRequest, callCount)
	}
}

func TestCloudSchemaAndNativeReadExposeRegisteredInstanceDetailOperation(t *testing.T) {
	detailName := "aws.ec2.describe_instance"
	adapter := &mcpTestAdapter{
		profile:  "aws-prod",
		provider: model.ProviderAWS,
		operations: []provider.Operation{{
			Name:       detailName,
			Provider:   model.ProviderAWS,
			Service:    "ec2",
			Parameters: map[string]any{"instance_id": map[string]any{"type": "string", "required": true}},
		}},
		nativePage: provider.Page{
			Rows:      []map[string]any{{"provider": "aws", "domain": "compute", "kind": "instance", "id": "i-1"}},
			NextToken: "detail-next",
			Scanned:   1,
			Requests:  1,
		},
	}
	registry, err := provider.NewRegistry(adapter)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	session, ctx := connectMCP(t, New(config.Default(), registry, nil))

	schemaResult, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "cloud_schema", Arguments: map[string]any{}})
	if err != nil || schemaResult.IsError {
		t.Fatalf("cloud_schema detail result = (%#v, %v), want success", schemaResult, err)
	}
	schema := decodeStructured[SchemaOutput](t, schemaResult)
	var detail provider.Operation
	found := false
	for _, operation := range schema.Operations {
		if operation.Name == detailName {
			detail = operation
			found = true
			break
		}
	}
	if !found || detail.Service != "ec2" || detail.Parameters["instance_id"] == nil {
		t.Fatalf("cloud_schema detail operation = %#v, want ec2 schema with instance_id", detail)
	}
	for _, forbidden := range []string{"query", "url", "action", "userData", "customData", "metadata", "password", "console", "vnc"} {
		if _, exposed := detail.Parameters[forbidden]; exposed {
			t.Fatalf("cloud_schema detail operation exposes sensitive/arbitrary parameter %q", forbidden)
		}
	}

	nativeResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "cloud_native_read",
		Arguments: map[string]any{
			"profile":   "aws-prod",
			"operation": detailName,
			"region":    "ap-southeast-1",
			"params":    map[string]any{"instance_id": "i-1"},
			"page_size": 1,
		},
	})
	if err != nil || nativeResult.IsError {
		t.Fatalf("cloud_native_read detail result = (%#v, %v), want success", nativeResult, err)
	}
	native := decodeStructured[NativeOutput](t, nativeResult)
	if len(native.Rows) != 1 || native.Rows[0]["id"] != "i-1" || native.NextCursor != "detail-next" || native.Scanned != 1 || native.Requests != 1 {
		t.Fatalf("cloud_native_read detail output = %#v, want row/cursor/stats", native)
	}
	adapter.mu.Lock()
	lastRequest := adapter.lastNative
	callCount := adapter.nativeCalls
	adapter.mu.Unlock()
	if callCount != 1 || lastRequest.Operation != detailName || lastRequest.Region != "ap-southeast-1" || lastRequest.Limit != 1 || lastRequest.Params["instance_id"] != "i-1" {
		t.Fatalf("provider detail NativeRead request = %#v calls=%d, want typed registered request", lastRequest, callCount)
	}
}

func TestCloudSchemaAndNativeReadExposeRegisteredDatabasePostureOperation(t *testing.T) {
	operationName := "aws.rds.describe_db_instance"
	adapter := &mcpTestAdapter{
		profile:  "aws-prod",
		provider: model.ProviderAWS,
		operations: []provider.Operation{{
			Name:       operationName,
			Provider:   model.ProviderAWS,
			Service:    "rds",
			Parameters: map[string]any{"db_instance_identifier": map[string]any{"type": "string", "required": true}},
		}},
		nativePage: provider.Page{
			Rows:     []map[string]any{{"provider": "aws", "domain": "database", "kind": "database", "id": "db-1", "attributes": map[string]any{"posture": map[string]any{"backup_enabled": true}}}},
			Scanned:  1,
			Requests: 1,
		},
	}
	registry, err := provider.NewRegistry(adapter)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	session, ctx := connectMCP(t, New(config.Default(), registry, nil))

	schemaResult, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "cloud_schema", Arguments: map[string]any{}})
	if err != nil || schemaResult.IsError {
		t.Fatalf("cloud_schema database result = (%#v, %v), want success", schemaResult, err)
	}
	schema := decodeStructured[SchemaOutput](t, schemaResult)
	var operation provider.Operation
	found := false
	for _, candidate := range schema.Operations {
		if candidate.Name == operationName {
			operation = candidate
			found = true
			break
		}
	}
	if !found || operation.Service != "rds" || operation.Parameters["db_instance_identifier"] == nil {
		t.Fatalf("cloud_schema database operation = %#v, want rds schema with identifier", operation)
	}
	for _, forbidden := range []string{"query", "url", "action", "view_arn", "password", "connection_string", "endpoint", "kubeconfig", "certificate", "token", "secret", "user_data", "metadata"} {
		if _, exposed := operation.Parameters[forbidden]; exposed {
			t.Fatalf("cloud_schema database operation exposes forbidden parameter %q", forbidden)
		}
	}

	nativeResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "cloud_native_read",
		Arguments: map[string]any{
			"profile":   "aws-prod",
			"operation": operationName,
			"region":    "us-east-1",
			"params":    map[string]any{"db_instance_identifier": "db-1"},
			"page_size": 1,
		},
	})
	if err != nil || nativeResult.IsError {
		t.Fatalf("cloud_native_read database result = (%#v, %v), want success", nativeResult, err)
	}
	native := decodeStructured[NativeOutput](t, nativeResult)
	if len(native.Rows) != 1 || native.Rows[0]["kind"] != "database" || native.Rows[0]["id"] != "db-1" || native.NextCursor != "" || native.Scanned != 1 || native.Requests != 1 {
		t.Fatalf("cloud_native_read database output = %#v, want one non-paginated row/stats", native)
	}
	adapter.mu.Lock()
	lastRequest := adapter.lastNative
	callCount := adapter.nativeCalls
	adapter.mu.Unlock()
	if callCount != 1 || lastRequest.Operation != operationName || lastRequest.Region != "us-east-1" || lastRequest.Limit != 1 || lastRequest.Params["db_instance_identifier"] != "db-1" {
		t.Fatalf("database NativeRead request = %#v calls=%d, want typed registered request", lastRequest, callCount)
	}
}

func TestCloudNativeReadUsesAllowlistAndRejectsArbitraryOperationOrURL(t *testing.T) {
	server, aws, _ := newMCPTestServer(t)
	session, ctx := connectMCP(t, server)

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "cloud_native_read",
		Arguments: map[string]any{
			"profile":   "aws-prod",
			"operation": "aws.inventory.search",
			"region":    "ap-southeast-1",
			"params":    map[string]any{"query": "compute"},
			"page_size": 1,
		},
	})
	if err != nil || result.IsError {
		t.Fatalf("registered cloud_native_read result = (%#v, %v), want success", result, err)
	}
	native := decodeStructured[NativeOutput](t, result)
	if native.Scanned != 1 || native.Requests != 1 || len(native.Rows) != 1 {
		t.Fatalf("cloud_native_read output = %#v, want one native row", native)
	}
	aws.mu.Lock()
	lastRequest := aws.lastNative
	nativeCalls := aws.nativeCalls
	aws.mu.Unlock()
	if nativeCalls != 1 || lastRequest.Operation != "aws.inventory.search" || lastRequest.Region != "ap-southeast-1" || lastRequest.Limit != 1 {
		t.Fatalf("provider native request = %#v calls=%d, want typed registered request", lastRequest, nativeCalls)
	}

	for _, operation := range []string{"aws.inventory.delete", "https://169.254.169.254/latest/meta-data", "ListResources"} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      "cloud_native_read",
			Arguments: map[string]any{"profile": "aws-prod", "operation": operation},
		})
		requireToolError(t, result, err, "not registered")
	}
	aws.mu.Lock()
	nativeCalls = aws.nativeCalls
	aws.mu.Unlock()
	if nativeCalls != 1 {
		t.Fatalf("provider native calls after rejected operations = %d, want 1", nativeCalls)
	}

	for name, params := range map[string]map[string]any{
		"undeclared parameter": {"unexpected": "value"},
		"wrong parameter type": {"query": 42},
	} {
		t.Run(name, func(t *testing.T) {
			result, err := session.CallTool(ctx, &mcp.CallToolParams{
				Name: "cloud_native_read",
				Arguments: map[string]any{
					"profile":   "aws-prod",
					"operation": "aws.inventory.search",
					"params":    params,
				},
			})
			requireToolError(t, result, err, "parameter")
		})
	}
	aws.mu.Lock()
	nativeCalls = aws.nativeCalls
	aws.mu.Unlock()
	if nativeCalls != 1 {
		t.Fatalf("provider native calls after invalid parameters = %d, want 1", nativeCalls)
	}
}
