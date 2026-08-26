package provider

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"mcpcloud/internal/config"
	"mcpcloud/internal/model"
)

type Capability struct {
	Provider   model.Provider `json:"provider"`
	Profile    string         `json:"profile,omitempty"`
	Source     model.Source   `json:"source"`
	Domains    []string       `json:"domains,omitempty"`
	Kinds      []string       `json:"kinds,omitempty"`
	Operations []string       `json:"operations,omitempty"`
	Status     string         `json:"status"`
	Notes      string         `json:"notes,omitempty"`
}

type Operation struct {
	Name        string         `json:"name"`
	Provider    model.Provider `json:"provider"`
	Service     string         `json:"service"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

func (o Operation) ValidateParams(params map[string]any) error {
	if len(params) > 64 {
		return fmt.Errorf("operation %q accepts at most 64 parameters", o.Name)
	}
	totalSize := 0
	for name, value := range params {
		rawSchema, ok := o.Parameters[name]
		if !ok {
			return fmt.Errorf("operation %q does not accept parameter %q", o.Name, name)
		}
		schema, ok := rawSchema.(map[string]any)
		if !ok {
			return fmt.Errorf("operation %q has an invalid schema for parameter %q", o.Name, name)
		}
		typeName, _ := schema["type"].(string)
		switch typeName {
		case "string":
			text, ok := value.(string)
			if !ok {
				return fmt.Errorf("parameter %q must be a string", name)
			}
			if len(text) > 4096 || strings.ContainsRune(text, '\x00') {
				return fmt.Errorf("parameter %q is too large or contains an invalid character", name)
			}
			totalSize += len(text)
		case "array":
			kind := reflect.Invalid
			if value != nil {
				kind = reflect.ValueOf(value).Kind()
			}
			if kind != reflect.Array && kind != reflect.Slice {
				return fmt.Errorf("parameter %q must be an array", name)
			}
			if reflect.ValueOf(value).Len() > 128 {
				return fmt.Errorf("parameter %q accepts at most 128 values", name)
			}
			items, _ := schema["items"].(string)
			if items == "string" {
				v := reflect.ValueOf(value)
				for i := 0; i < v.Len(); i++ {
					text, ok := v.Index(i).Interface().(string)
					if !ok {
						return fmt.Errorf("parameter %q values must be strings", name)
					}
					if len(text) > 4096 || strings.ContainsRune(text, '\x00') {
						return fmt.Errorf("parameter %q contains a value that is too large or invalid", name)
					}
					totalSize += len(text)
				}
			}
		default:
			return fmt.Errorf("operation %q has unsupported parameter type %q", o.Name, typeName)
		}
		if totalSize > 1<<20 {
			return fmt.Errorf("operation %q parameter data exceeds 1 MiB", o.Name)
		}
	}
	return nil
}

type QueryRequest struct {
	Source    model.Source
	Region    string
	Accounts  []string
	Metrics   []string
	Start     *time.Time
	End       *time.Time
	Step      time.Duration
	Limit     int
	PageToken string
}

type Page struct {
	Rows      []map[string]any
	NextToken string
	Scanned   int
	Requests  int
}

type NativeRequest struct {
	Operation string
	Region    string
	Params    map[string]any
	PageToken string
	Limit     int
}

type Adapter interface {
	Provider() model.Provider
	Profile() string
	Capabilities() []Capability
	Operations() []Operation
	Readiness(context.Context) model.ProfileStatus
	Query(context.Context, QueryRequest) (Page, error)
	NativeRead(context.Context, NativeRequest) (Page, error)
}

type Error struct {
	Code      string
	Message   string
	Operation string
	Retryable bool
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

type Registry struct {
	adapters map[string]Adapter
}

func NewRegistry(adapters ...Adapter) (*Registry, error) {
	r := &Registry{adapters: map[string]Adapter{}}
	for _, adapter := range adapters {
		if adapter == nil {
			continue
		}
		name := adapter.Profile()
		if name == "" {
			return nil, fmt.Errorf("provider adapter has an empty profile")
		}
		if _, exists := r.adapters[name]; exists {
			return nil, fmt.Errorf("duplicate provider profile %q", name)
		}
		r.adapters[name] = adapter
	}
	return r, nil
}

func (r *Registry) Get(profile string) (Adapter, bool) {
	adapter, ok := r.adapters[profile]
	return adapter, ok
}

func (r *Registry) Profiles() []string {
	names := make([]string, 0, len(r.adapters))
	for name := range r.adapters {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (r *Registry) Capabilities() []Capability {
	var capabilities []Capability
	for _, name := range r.Profiles() {
		for _, capability := range r.adapters[name].Capabilities() {
			capability.Profile = name
			capabilities = append(capabilities, capability)
		}
	}
	return capabilities
}

// BaselineCapabilities describes connectors compiled into the binary. It is
// intentionally separate from Registry.Capabilities, whose statuses also
// reflect each configured profile's prerequisites.
func BaselineCapabilities() []Capability {
	result := make([]Capability, 0, len(model.Providers)*len(model.Sources))
	for _, cloud := range model.Providers {
		result = append(result,
			Capability{Provider: cloud, Source: model.SourceResources, Domains: append([]string(nil), CoreDomains[:8]...), Kinds: append([]string(nil), InventoryKinds...), Status: "inventory", Notes: "compiled read-only control-plane inventory connector; live coverage depends on indexing, permissions, and account configuration"},
			Capability{Provider: cloud, Source: model.SourceIAM, Domains: []string{"iam"}, Kinds: []string{"user", "role", "service_account", "policy", "binding"}, Status: "inventory", Notes: "compiled cloud-native IAM inventory connector; enterprise directories are excluded"},
			Capability{Provider: cloud, Source: model.SourceMetrics, Domains: []string{"monitoring"}, Kinds: []string{"metric"}, Status: "available", Notes: "compiled read-only metric connector; inspect profile capabilities for provider-specific prerequisites"},
			Capability{Provider: cloud, Source: model.SourceCosts, Domains: []string{"cost"}, Kinds: []string{"cost_record"}, Status: "available", Notes: "compiled read-only billing connector; inspect profile capabilities for provider-specific prerequisites"},
		)
	}
	return result
}

func (r *Registry) Operations() []Operation {
	seen := map[string]bool{}
	var operations []Operation
	for _, name := range r.Profiles() {
		for _, operation := range r.adapters[name].Operations() {
			if !seen[operation.Name] {
				seen[operation.Name] = true
				operations = append(operations, operation)
			}
		}
	}
	sort.Slice(operations, func(i, j int) bool { return operations[i].Name < operations[j].Name })
	return operations
}

func (r *Registry) Statuses(ctx context.Context) []model.ProfileStatus {
	statuses := make([]model.ProfileStatus, 0, len(r.adapters))
	for _, name := range r.Profiles() {
		statuses = append(statuses, r.adapters[name].Readiness(ctx))
	}
	return statuses
}

type Factory func(name string, profile config.Profile) (Adapter, error)

var factories = map[model.Provider]Factory{}

func RegisterFactory(provider model.Provider, factory Factory) {
	if factory == nil {
		panic("provider factory cannot be nil")
	}
	if _, exists := factories[provider]; exists {
		panic("provider factory already registered: " + string(provider))
	}
	factories[provider] = factory
}

func FromConfig(cfg config.Config) (*Registry, error) {
	adapters := make([]Adapter, 0, len(cfg.Profiles))
	for _, name := range cfg.ProfileNames() {
		profile := cfg.Profiles[name]
		factory, ok := factories[profile.Provider]
		if !ok {
			return nil, fmt.Errorf("provider %q has no adapter factory", profile.Provider)
		}
		adapter, err := factory(name, profile)
		if err != nil {
			return nil, fmt.Errorf("profile %q: %w", name, err)
		}
		adapters = append(adapters, newScopedAdapter(adapter, profile))
	}
	return NewRegistry(adapters...)
}

var CoreDomains = []string{"compute", "storage", "network", "iam", "database", "kubernetes", "monitoring", "logging", "cost"}

var CoreKinds = []string{
	"instance", "disk", "bucket", "network", "subnet", "security_group", "public_ip", "load_balancer",
	"user", "role", "service_account", "policy", "binding", "database", "cache", "cluster", "node_pool",
	"metric", "alarm", "log_project", "log_group", "log_index", "log_sink", "cost_record",
}

var InventoryKinds = []string{
	"instance", "disk", "bucket", "network", "subnet", "security_group", "public_ip", "load_balancer",
	"user", "role", "service_account", "policy", "binding", "database", "cache", "cluster", "node_pool",
	"alarm", "log_project", "log_group", "log_index", "log_sink",
}
