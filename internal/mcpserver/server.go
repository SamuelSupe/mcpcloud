package mcpserver

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpcloud/internal/config"
	"mcpcloud/internal/engine"
	"mcpcloud/internal/model"
	"mcpcloud/internal/probe"
	"mcpcloud/internal/provider"
)

type Server struct {
	cfg       config.Config
	engine    *engine.Engine
	registry  *provider.Registry
	probe     *probe.Runner
	readiness *probe.Coordinator
	logger    *slog.Logger
	mcp       *mcp.Server
}

type QueryInput struct {
	Query     string `json:"query,omitempty" jsonschema:"New pipeline DSL query; mutually exclusive with cursor"`
	Cursor    string `json:"cursor,omitempty" jsonschema:"Opaque cursor returned by a previous cloud_query call"`
	PageSize  int    `json:"page_size,omitempty" jsonschema:"Rows returned per call, default 100 and maximum 500"`
	TimeoutMS int    `json:"timeout_ms,omitempty" jsonschema:"Provider execution timeout in milliseconds, default 30000 and maximum 120000"`
}

type ExplainInput struct {
	Query string `json:"query" jsonschema:"Pipeline DSL query to parse and explain"`
}
type SchemaInput struct {
	Source   model.Source   `json:"source,omitempty"`
	Provider model.Provider `json:"provider,omitempty"`
	Kind     string         `json:"kind,omitempty"`
}
type ProfilesInput struct {
	Provider model.Provider `json:"provider,omitempty"`
}
type NativeInput struct {
	Profile   string         `json:"profile" jsonschema:"Configured profile name"`
	Operation string         `json:"operation" jsonschema:"Registered read-only operation identifier"`
	Region    string         `json:"region,omitempty"`
	Params    map[string]any `json:"params,omitempty" jsonschema:"Parameters validated by the registered operation"`
	Cursor    string         `json:"cursor,omitempty"`
	PageSize  int            `json:"page_size,omitempty"`
}

type SchemaOutput struct {
	Providers        []model.Provider      `json:"providers"`
	Sources          []model.Source        `json:"sources"`
	Domains          []string              `json:"domains"`
	ResourceKinds    []string              `json:"resource_kinds"`
	Fields           []string              `json:"fields"`
	NativeFields     map[string][]string   `json:"native_fields"`
	Operators        []string              `json:"operators"`
	Stages           []string              `json:"stages"`
	CapabilityMatrix []provider.Capability `json:"capability_matrix"`
	Capabilities     []provider.Capability `json:"profile_capabilities"`
	Operations       []provider.Operation  `json:"operations"`
}
type ProfilesOutput struct {
	Profiles []model.ProfileStatus `json:"profiles"`
}
type NativeOutput struct {
	RequestID  string           `json:"request_id"`
	Rows       []map[string]any `json:"rows"`
	NextCursor string           `json:"next_cursor,omitempty"`
	Scanned    int              `json:"scanned"`
	Requests   int              `json:"requests"`
}

type ReadinessOutput struct {
	ProbeID    string     `json:"probe_id"`
	Status     string     `json:"status"`
	Cached     bool       `json:"cached"`
	ObservedAt time.Time  `json:"observed_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	Profiles   int        `json:"profiles"`
	Ready      int        `json:"ready"`
	Degraded   int        `json:"degraded"`
	NotReady   int        `json:"not_ready"`
}

func New(cfg config.Config, registry *provider.Registry, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(os.Stderr, nil))
	}
	queryEngine := engine.New(registry, cfg.Limits)
	queryEngine.SetAuditSink(engine.NewSlogAuditSink(logger))
	probeRunner := probe.NewRunner(registry, queryEngine)
	probeTimeout := 15 * time.Second
	if cfg.Limits.MaxTimeout > 0 && cfg.Limits.MaxTimeout < probeTimeout {
		probeTimeout = cfg.Limits.MaxTimeout
	}
	s := &Server{cfg: cfg, registry: registry, engine: queryEngine, probe: probeRunner, readiness: probe.NewCoordinator(probeRunner, time.Minute, probeTimeout), logger: logger}
	s.mcp = mcp.NewServer(&mcp.Implementation{Name: "mcpcloud", Version: "0.1.0"}, &mcp.ServerOptions{Logger: logger})
	s.addTools()
	return s
}

func (s *Server) addTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{Name: "cloud_help", Title: "Learn the cloud tools", Description: "Get structured, provider-aware guidance and safe examples before using the read-only cloud tools; omit topic to list help topics."}, func(_ context.Context, _ *mcp.CallToolRequest, input HelpInput) (*mcp.CallToolResult, HelpOutput, error) {
		result, err := s.help(input)
		return nil, result, err
	})
	mcp.AddTool(s.mcp, &mcp.Tool{Name: "cloud_query", Title: "Query cloud environments", Description: "Primary interface for normal discovery: run the read-only multi-cloud pipeline DSL or continue an opaque cursor; use cloud_explain first for new queries."}, func(ctx context.Context, _ *mcp.CallToolRequest, input QueryInput) (*mcp.CallToolResult, model.QueryResult, error) {
		if len(input.Query) > 64<<10 || len(input.Cursor) > 128 {
			return nil, model.QueryResult{}, fmt.Errorf("query or cursor exceeds the allowed size")
		}
		if (strings.TrimSpace(input.Query) == "") == (strings.TrimSpace(input.Cursor) == "") {
			return nil, model.QueryResult{}, fmt.Errorf("provide exactly one of query or cursor")
		}
		if input.Cursor != "" {
			if input.PageSize != 0 || input.TimeoutMS != 0 {
				return nil, model.QueryResult{}, fmt.Errorf("cursor continuation accepts only cursor")
			}
			started := time.Now()
			result, err := s.engine.Next(input.Cursor)
			status := result.Status
			if err != nil {
				status = "failed"
			}
			s.logger.Info("cloud query cursor", slog.String("query_id", result.QueryID), slog.String("operation", "cloud_query_cursor"), slog.String("status", status), slog.Int("rows", len(result.Rows)), slog.Duration("duration", time.Since(started)))
			return nil, result, err
		}
		if input.PageSize < 0 {
			return nil, model.QueryResult{}, fmt.Errorf("page_size cannot be negative")
		}
		if input.TimeoutMS < 0 {
			return nil, model.QueryResult{}, fmt.Errorf("timeout_ms cannot be negative")
		}
		query, err := s.engine.Parse(input.Query)
		if err != nil {
			return nil, model.QueryResult{}, err
		}
		timeout := s.cfg.Limits.DefaultTimeout
		if input.TimeoutMS > 0 {
			if int64(input.TimeoutMS) > s.cfg.Limits.MaxTimeout.Milliseconds() {
				return nil, model.QueryResult{}, fmt.Errorf("timeout_ms exceeds maximum %d", s.cfg.Limits.MaxTimeout.Milliseconds())
			}
			timeout = time.Duration(input.TimeoutMS) * time.Millisecond
		}
		if timeout > s.cfg.Limits.MaxTimeout {
			return nil, model.QueryResult{}, fmt.Errorf("timeout_ms exceeds maximum %d", s.cfg.Limits.MaxTimeout.Milliseconds())
		}
		queryCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		plan, _ := s.engine.Explain(query)
		started := time.Now()
		result, err := s.engine.Query(queryCtx, query, input.PageSize)
		status := result.Status
		if err != nil {
			status = "failed"
		}
		s.logger.Info("cloud query", slog.String("query_id", result.QueryID), slog.String("profiles", strings.Join(plan.Profiles, ",")), slog.String("operation", "cloud_query"), slog.String("status", status), slog.Int("rows", len(result.Rows)), slog.Duration("duration", time.Since(started)))
		return nil, result, err
	})
	mcp.AddTool(s.mcp, &mcp.Tool{Name: "cloud_explain", Title: "Explain cloud query", Description: "Recommended before a new query: parse, validate, and plan the DSL without calling cloud APIs."}, func(ctx context.Context, _ *mcp.CallToolRequest, input ExplainInput) (*mcp.CallToolResult, engine.ExplainResult, error) {
		if len(input.Query) > 64<<10 {
			return nil, engine.ExplainResult{}, fmt.Errorf("query exceeds the allowed size")
		}
		query, err := s.engine.Parse(input.Query)
		if err != nil {
			return nil, engine.ExplainResult{}, err
		}
		result, err := s.engine.Explain(query)
		return nil, result, err
	})
	mcp.AddTool(s.mcp, &mcp.Tool{Name: "cloud_schema", Title: "Discover cloud query schema", Description: "Authoritative discovery for DSL fields, provider capabilities, native allow-lists, and registered typed read operations."}, func(_ context.Context, _ *mcp.CallToolRequest, input SchemaInput) (*mcp.CallToolResult, SchemaOutput, error) {
		if len(input.Kind) > 256 {
			return nil, SchemaOutput{}, fmt.Errorf("kind exceeds the allowed size")
		}
		matrix := provider.BaselineCapabilities()
		capabilities := s.registry.Capabilities()
		operations := s.registry.Operations()
		native := nativeFields()
		if input.Source != "" && !slices.Contains(model.Sources, input.Source) {
			return nil, SchemaOutput{}, fmt.Errorf("unsupported source %q", input.Source)
		}
		if input.Provider != "" && !slices.Contains(model.Providers, input.Provider) {
			return nil, SchemaOutput{}, fmt.Errorf("unsupported provider %q", input.Provider)
		}
		if input.Kind != "" && !slices.Contains(provider.CoreKinds, input.Kind) {
			return nil, SchemaOutput{}, fmt.Errorf("unsupported kind %q", input.Kind)
		}
		capabilities = slices.DeleteFunc(capabilities, func(capability provider.Capability) bool {
			if input.Source != "" && capability.Source != input.Source {
				return true
			}
			if input.Provider != "" && capability.Provider != input.Provider {
				return true
			}
			return input.Kind != "" && !slices.Contains(capability.Kinds, input.Kind)
		})
		matrix = slices.DeleteFunc(matrix, func(capability provider.Capability) bool {
			if input.Source != "" && capability.Source != input.Source {
				return true
			}
			if input.Provider != "" && capability.Provider != input.Provider {
				return true
			}
			return input.Kind != "" && !slices.Contains(capability.Kinds, input.Kind)
		})
		if input.Provider != "" {
			operations = slices.DeleteFunc(operations, func(operation provider.Operation) bool { return operation.Provider != input.Provider })
			for name := range native {
				if name != string(input.Provider) {
					delete(native, name)
				}
			}
		}
		return nil, SchemaOutput{Providers: model.Providers, Sources: model.Sources, Domains: provider.CoreDomains, ResourceKinds: provider.CoreKinds, Fields: commonFields(), NativeFields: native, Operators: []string{"==", "!=", ">", ">=", "<", "<=", "in", "contains", "starts_with", "ends_with", "exists", "and", "or", "not"}, Stages: []string{"scope", "where", "range", "step", "include native", "fields", "summarize", "sort", "limit"}, CapabilityMatrix: matrix, Capabilities: capabilities, Operations: operations}, nil
	})
	mcp.AddTool(s.mcp, &mcp.Tool{Name: "cloud_profiles", Title: "List cloud profiles", Description: "Show configured scope and credential configuration readiness without returning credentials or proving live cloud access."}, func(ctx context.Context, _ *mcp.CallToolRequest, input ProfilesInput) (*mcp.CallToolResult, ProfilesOutput, error) {
		if input.Provider != "" && !slices.Contains(model.Providers, input.Provider) {
			return nil, ProfilesOutput{}, fmt.Errorf("unsupported provider %q", input.Provider)
		}
		statuses := s.registry.Statuses(ctx)
		if input.Provider != "" {
			statuses = slices.DeleteFunc(statuses, func(status model.ProfileStatus) bool { return status.Provider != input.Provider })
		}
		return nil, ProfilesOutput{Profiles: statuses}, nil
	})
	mcp.AddTool(s.mcp, &mcp.Tool{Name: "cloud_native_read", Title: "Run a registered cloud read", Description: "Use only when the DSL is insufficient: invoke one cloud_schema-registered typed read operation; arbitrary actions, URLs, and parameters are rejected."}, func(ctx context.Context, _ *mcp.CallToolRequest, input NativeInput) (*mcp.CallToolResult, NativeOutput, error) {
		if strings.TrimSpace(input.Profile) == "" || strings.TrimSpace(input.Operation) == "" {
			return nil, NativeOutput{}, fmt.Errorf("profile and operation are required")
		}
		if len(input.Profile) > 256 || len(input.Operation) > 256 || len(input.Region) > 256 || len(input.Cursor) > 8192 {
			return nil, NativeOutput{}, fmt.Errorf("native read input exceeds the allowed size")
		}
		if input.PageSize < 0 {
			return nil, NativeOutput{}, fmt.Errorf("page_size cannot be negative")
		}
		nativeCtx, cancel := context.WithTimeout(ctx, s.cfg.Limits.DefaultTimeout)
		defer cancel()
		started := time.Now()
		requestID := engine.NewCorrelationID()
		page, err := s.engine.NativeReadWithID(nativeCtx, requestID, input.Profile, provider.NativeRequest{Operation: input.Operation, Region: input.Region, Params: input.Params, PageToken: input.Cursor, Limit: input.PageSize})
		status := "ok"
		if err != nil {
			status = "failed"
			s.logger.Info("cloud native read", slog.String("request_id", requestID), slog.String("profile", input.Profile), slog.String("operation", input.Operation), slog.String("status", status), slog.Int("rows", 0), slog.Duration("duration", time.Since(started)))
			return nil, NativeOutput{RequestID: requestID}, err
		}
		s.logger.Info("cloud native read", slog.String("request_id", requestID), slog.String("profile", input.Profile), slog.String("operation", input.Operation), slog.String("status", status), slog.Int("rows", len(page.Rows)), slog.Duration("duration", time.Since(started)))
		return nil, NativeOutput{RequestID: requestID, Rows: page.Rows, NextCursor: page.NextToken, Scanned: page.Scanned, Requests: page.Requests}, nil
	})
}

func (s *Server) RunStdio(ctx context.Context) error { return s.mcp.Run(ctx, &mcp.StdioTransport{}) }

func (s *Server) RunHTTP(ctx context.Context) error {
	token := os.Getenv(s.cfg.Server.AuthTokenEnv)
	if token == "" {
		return fmt.Errorf("HTTP transport requires %s", s.cfg.Server.AuthTokenEnv)
	}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.mcp }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, Logger: s.logger, MaxRequestBodyBytes: 1 << 20, PropagateRequestCancellation: true})
	protection := http.NewCrossOriginProtection()
	for _, origin := range s.cfg.Server.AllowedOrigins {
		if err := protection.AddTrustedOrigin(origin); err != nil {
			return fmt.Errorf("allowed origin %q: %w", origin, err)
		}
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", bearer(token, hostGuard(s.cfg, protection.Handler(handler))))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/readyz", s.readyz)
	httpServer := &http.Server{Addr: s.cfg.Server.Listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 130 * time.Second, WriteTimeout: 130 * time.Second, IdleTimeout: 60 * time.Second}
	done := make(chan error, 1)
	go func() {
		err := httpServer.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
		return <-done
	}
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	report, err := s.readiness.Current(r.Context())
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": probe.StatusNotReady, "error_code": "probe_failed"})
		return
	}
	if report.Status == probe.StatusNotReady {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	output := ReadinessOutput{ProbeID: report.ProbeID, Status: report.Status, Cached: report.Cached, ObservedAt: report.ObservedAt, ExpiresAt: report.ExpiresAt, Profiles: len(report.Profiles)}
	for _, profile := range report.Profiles {
		switch profile.Status {
		case probe.StatusReady:
			output.Ready++
		case probe.StatusDegraded:
			output.Degraded++
		default:
			output.NotReady++
		}
	}
	_ = json.NewEncoder(w).Encode(output)
}

func bearer(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expected := sha256.Sum256([]byte("Bearer " + token))
		provided := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		if subtle.ConstantTimeCompare(expected[:], provided[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="mcpcloud"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func hostGuard(cfg config.Config, next http.Handler) http.Handler {
	host, port, _ := net.SplitHostPort(cfg.Server.Listen)
	allowed := map[string]bool{net.JoinHostPort(host, port): true, net.JoinHostPort("localhost", port): true, net.JoinHostPort("127.0.0.1", port): true, net.JoinHostPort("::1", port): true}
	for _, item := range cfg.Server.AllowedHosts {
		allowed[strings.ToLower(item)] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowed[strings.ToLower(r.Host)] {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func nativeFields() map[string][]string {
	result := make(map[string][]string, len(model.NativeFields))
	for cloud, fields := range model.NativeFields {
		result[string(cloud)] = append([]string(nil), fields...)
	}
	return result
}
func commonFields() []string {
	return []string{"provider", "profile", "domain", "service", "kind", "id", "name", "scope.organization_id", "scope.tenant_id", "scope.account_id", "scope.project_id", "scope.subscription_id", "scope.resource_group_id", "region", "zone", "state", "tags[\"key\"]", "created_at", "updated_at", "observed_at", "attributes", "native", "metric", "timestamp", "value", "unit", "dimensions", "date", "amount", "currency", "principal", "principal_type", "resource_id", "roles", "actions", "effect", "condition"}
}
