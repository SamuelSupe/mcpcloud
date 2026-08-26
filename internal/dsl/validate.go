package dsl

import (
	"fmt"
	"strings"
	"time"

	"mcpcloud/internal/model"
)

func Validate(q Query) error {
	if q.Limit <= 0 || q.Limit > 50_000 {
		return fmt.Errorf("limit must be between 1 and 50000")
	}
	if q.Source == model.SourceMetrics {
		if q.Range == nil {
			return fmt.Errorf("metrics queries require a range stage")
		}
		selectors, constrained := MetricSelectors(q.Filter)
		if !constrained || len(selectors) == 0 {
			return fmt.Errorf("metrics queries require an exact metric == or metric in predicate on every OR branch")
		}
		if len(selectors) > 20 {
			return fmt.Errorf("metrics queries support at most 20 metric selectors")
		}
		if q.Range.End.Sub(q.Range.Start) > 31*24*time.Hour {
			return fmt.Errorf("metrics range cannot exceed 31 days")
		}
		if q.Step != 0 && q.Step < time.Minute {
			return fmt.Errorf("metrics step cannot be shorter than 1m")
		}
		if q.Step > q.Range.End.Sub(q.Range.Start) {
			return fmt.Errorf("metrics step cannot exceed the query range")
		}
	}
	for _, selected := range q.Scope.Providers {
		valid := false
		for _, candidate := range model.Providers {
			if selected == candidate {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("unsupported scoped provider %q", selected)
		}
	}
	if q.Source == model.SourceCosts {
		if q.Range == nil {
			return fmt.Errorf("costs queries require a range stage")
		}
		if q.Range.End.Sub(q.Range.Start) > 400*24*time.Hour {
			return fmt.Errorf("costs range cannot exceed 400 days")
		}
		if hasAmountAggregate(q.Aggregates) && !contains(q.GroupBy, "currency") {
			return fmt.Errorf("cost amount aggregation must group by currency")
		}
	}
	if (q.Source == model.SourceResources || q.Source == model.SourceIAM) && q.Range != nil {
		return fmt.Errorf("range is only supported for metrics and costs")
	}
	if q.Source != model.SourceMetrics && q.Step != 0 {
		return fmt.Errorf("step is only supported for metrics")
	}
	for _, field := range queryFields(q) {
		if !fieldAllowedForSource(q.Source, field) {
			return fmt.Errorf("field %q is not available for source %s", field, q.Source)
		}
		if err := validateNativeField(field, q.IncludeNative); err != nil {
			return err
		}
	}
	aggregateAliases := map[string]bool{}
	for _, aggregate := range q.Aggregates {
		if aggregate.Function != "count" && !numericAggregateField(aggregate.Field) {
			return fmt.Errorf("aggregate %s requires a numeric value, amount, attributes.*, or native.* field", aggregate.Function)
		}
		aggregateAliases[aggregate.Alias] = true
	}
	for _, field := range q.Fields {
		if aggregateOutputField(field) && !aggregateAliases[field] {
			return fmt.Errorf("aggregate output field %q is not produced by summarize", field)
		}
	}
	for _, field := range q.GroupBy {
		if aggregateOutputField(field) {
			return fmt.Errorf("aggregate output field %q cannot be a group key", field)
		}
	}
	for _, sortField := range q.Sort {
		if aggregateOutputField(sortField.Field) && !aggregateAliases[sortField.Field] {
			return fmt.Errorf("aggregate output field %q is not produced by summarize", sortField.Field)
		}
	}
	if err := validateNativeExpression(q.Filter, q.IncludeNative); err != nil {
		return err
	}
	if err := validateSourceExpression(q.Source, q.Filter); err != nil {
		return err
	}
	if referencesAggregateOutput(q.Filter) {
		return fmt.Errorf("where cannot reference aggregate output fields")
	}
	return nil
}

func numericAggregateField(field string) bool {
	parts := splitPath(field)
	if len(parts) == 0 {
		return false
	}
	return parts[0] == "value" || parts[0] == "amount" || (len(parts) > 1 && (parts[0] == "attributes" || parts[0] == "native"))
}

func fieldAllowedForSource(source model.Source, field string) bool {
	parts := splitPath(field)
	if len(parts) == 0 {
		return false
	}
	root := parts[0]
	if aggregateOutputField(root) {
		return true
	}
	common := map[string]bool{
		"provider": true, "profile": true, "domain": true, "service": true, "kind": true,
		"id": true, "name": true, "scope": true, "region": true, "zone": true,
		"state": true, "tags": true, "created_at": true, "updated_at": true,
		"observed_at": true, "attributes": true, "native": true,
	}
	if common[root] {
		return true
	}
	switch source {
	case model.SourceIAM:
		return map[string]bool{"principal": true, "principal_type": true, "resource_id": true, "roles": true, "actions": true, "effect": true, "condition": true}[root]
	case model.SourceMetrics:
		return map[string]bool{"metric": true, "timestamp": true, "value": true, "unit": true, "dimensions": true}[root]
	case model.SourceCosts:
		return map[string]bool{"date": true, "amount": true, "currency": true}[root]
	default:
		return false
	}
}

func validateSourceExpression(source model.Source, expr Expr) error {
	switch value := expr.(type) {
	case nil:
		return nil
	case Predicate:
		if !fieldAllowedForSource(source, value.Field) {
			return fmt.Errorf("field %q is not available for source %s", value.Field, source)
		}
		return nil
	case LogicalExpr:
		if err := validateSourceExpression(source, value.Left); err != nil {
			return err
		}
		return validateSourceExpression(source, value.Right)
	case NotExpr:
		return validateSourceExpression(source, value.Inner)
	default:
		return nil
	}
}

func queryFields(q Query) []string {
	fields := append(append([]string{}, q.Fields...), q.GroupBy...)
	for _, field := range q.Sort {
		fields = append(fields, field.Field)
	}
	for _, aggregate := range q.Aggregates {
		if aggregate.Field != "*" {
			fields = append(fields, aggregate.Field)
		}
	}
	return fields
}

func validateNativeExpression(expr Expr, includeNative bool) error {
	switch value := expr.(type) {
	case nil:
		return nil
	case Predicate:
		return validateNativeField(value.Field, includeNative)
	case LogicalExpr:
		if err := validateNativeExpression(value.Left, includeNative); err != nil {
			return err
		}
		return validateNativeExpression(value.Right, includeNative)
	case NotExpr:
		return validateNativeExpression(value.Inner, includeNative)
	default:
		return nil
	}
}

func validateNativeField(field string, includeNative bool) error {
	parts := splitPath(field)
	if len(parts) == 0 || parts[0] != "native" {
		return nil
	}
	if !includeNative {
		return fmt.Errorf("native fields require 'include native'")
	}
	if len(parts) == 1 {
		return nil
	}
	if len(parts) != 2 || !model.NativeFieldAllowed(parts[1]) {
		return fmt.Errorf("native field %q is not allow-listed", field)
	}
	return nil
}

// MetricSelectors returns the finite set of selectors that constrains a metric
// expression. OR is safe only when both branches constrain metric; AND needs a
// constraint on either branch because the other branch is evaluated locally.
func MetricSelectors(expr Expr) ([]string, bool) {
	switch value := expr.(type) {
	case Predicate:
		if value.Field != "metric" {
			return nil, false
		}
		switch value.Operator {
		case "==":
			selector, ok := value.Value.(string)
			if !ok || strings.TrimSpace(selector) == "" {
				return nil, false
			}
			return []string{selector}, true
		case "in":
			var selectors []string
			switch items := value.Value.(type) {
			case []any:
				for _, item := range items {
					selector, ok := item.(string)
					if !ok || strings.TrimSpace(selector) == "" {
						return nil, false
					}
					selectors = appendUnique(selectors, selector)
				}
			case []string:
				for _, selector := range items {
					if strings.TrimSpace(selector) == "" {
						return nil, false
					}
					selectors = appendUnique(selectors, selector)
				}
			default:
				return nil, false
			}
			return selectors, len(selectors) > 0
		default:
			return nil, false
		}
	case LogicalExpr:
		left, leftOK := MetricSelectors(value.Left)
		right, rightOK := MetricSelectors(value.Right)
		if value.Operator == "or" && (!leftOK || !rightOK) {
			return nil, false
		}
		if !leftOK && !rightOK {
			return nil, false
		}
		for _, selector := range right {
			left = appendUnique(left, selector)
		}
		return left, true
	case NotExpr:
		return nil, false
	default:
		return nil, false
	}
}

func appendUnique(values []string, value string) []string {
	for _, current := range values {
		if current == value {
			return values
		}
	}
	return append(values, value)
}

func hasAmountAggregate(aggregates []Aggregate) bool {
	for _, aggregate := range aggregates {
		if aggregate.Field == "amount" && aggregate.Function != "count" {
			return true
		}
	}
	return false
}

func contains(items []string, wanted string) bool {
	for _, item := range items {
		if item == wanted {
			return true
		}
	}
	return false
}

func referencesAggregateOutput(expr Expr) bool {
	switch value := expr.(type) {
	case nil:
		return false
	case Predicate:
		return aggregateOutputField(value.Field)
	case LogicalExpr:
		return referencesAggregateOutput(value.Left) || referencesAggregateOutput(value.Right)
	case NotExpr:
		return referencesAggregateOutput(value.Inner)
	default:
		return false
	}
}
