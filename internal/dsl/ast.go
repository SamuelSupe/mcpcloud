package dsl

import (
	"fmt"
	"strings"
	"time"

	"mcpcloud/internal/model"
)

type Query struct {
	Source        model.Source  `json:"source"`
	Scope         Scope         `json:"scope,omitempty"`
	Filter        Expr          `json:"-"`
	FilterText    string        `json:"filter,omitempty"`
	Range         *TimeRange    `json:"range,omitempty"`
	Step          time.Duration `json:"step,omitempty"`
	IncludeNative bool          `json:"include_native,omitempty"`
	Fields        []string      `json:"fields,omitempty"`
	Aggregates    []Aggregate   `json:"aggregates,omitempty"`
	GroupBy       []string      `json:"group_by,omitempty"`
	Sort          []SortField   `json:"sort,omitempty"`
	Limit         int           `json:"limit"`
}

type Scope struct {
	Profiles  []string         `json:"profiles,omitempty"`
	Providers []model.Provider `json:"providers,omitempty"`
	Accounts  []string         `json:"accounts,omitempty"`
	Regions   []string         `json:"regions,omitempty"`
}

type TimeRange struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

type Aggregate struct {
	Function string `json:"function"`
	Field    string `json:"field,omitempty"`
	Alias    string `json:"alias"`
}

type SortField struct {
	Field     string `json:"field"`
	Direction string `json:"direction"`
}

type Expr interface {
	Eval(row map[string]any) (bool, error)
	String() string
}

type LogicalExpr struct {
	Operator string
	Left     Expr
	Right    Expr
}

func (e LogicalExpr) String() string {
	return "(" + e.Left.String() + " " + e.Operator + " " + e.Right.String() + ")"
}

func (e LogicalExpr) Eval(row map[string]any) (bool, error) {
	left, err := e.Left.Eval(row)
	if err != nil {
		return false, err
	}
	if e.Operator == "and" {
		if !left {
			return false, nil
		}
		return e.Right.Eval(row)
	}
	if left {
		return true, nil
	}
	return e.Right.Eval(row)
}

type NotExpr struct{ Inner Expr }

func (e NotExpr) String() string { return "not (" + e.Inner.String() + ")" }
func (e NotExpr) Eval(row map[string]any) (bool, error) {
	v, err := e.Inner.Eval(row)
	return !v, err
}

type Predicate struct {
	Field    string
	Operator string
	Value    any
}

func (p Predicate) String() string {
	if p.Operator == "exists" {
		return p.Field + " exists"
	}
	return fmt.Sprintf("%s %s %v", p.Field, p.Operator, p.Value)
}

func (p Predicate) Eval(row map[string]any) (bool, error) {
	actual, exists := ValueAt(row, p.Field)
	if p.Operator == "exists" {
		return exists && actual != nil, nil
	}
	if !exists {
		return false, nil
	}
	return compare(actual, p.Operator, p.Value)
}

func FieldAllowed(field string) bool {
	if field == "" {
		return false
	}
	root := field
	if i := strings.IndexAny(root, ".["); i >= 0 {
		root = root[:i]
	}
	switch root {
	case "provider", "profile", "domain", "service", "kind", "id", "name", "scope", "region", "zone", "state", "tags", "created_at", "updated_at", "observed_at", "attributes", "native", "metric", "timestamp", "value", "unit", "dimensions", "date", "amount", "currency", "principal", "principal_type", "resource_id", "roles", "actions", "effect", "condition":
		return true
	default:
		return aggregateOutputField(root)
	}
}

func aggregateOutputField(field string) bool {
	if field == "count" {
		return true
	}
	validPrefix := false
	for _, prefix := range []string{"count_", "sum_", "avg_", "min_", "max_"} {
		if strings.HasPrefix(field, prefix) && len(field) > len(prefix) {
			validPrefix = true
			break
		}
	}
	if !validPrefix {
		return false
	}
	for _, r := range field {
		if r != '_' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}
