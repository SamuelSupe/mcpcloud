package dsl

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"mcpcloud/internal/model"
)

func Parse(input string, now time.Time) (Query, error) {
	if len(input) > 64<<10 {
		return Query{}, fmt.Errorf("query exceeds 64 KiB")
	}
	segments, err := splitPipeline(input)
	if err != nil {
		return Query{}, err
	}
	if len(segments) == 0 {
		return Query{}, fmt.Errorf("query is empty")
	}
	q := Query{Source: model.Source(strings.TrimSpace(segments[0])), Limit: 100}
	if !validSource(q.Source) {
		return Query{}, fmt.Errorf("unsupported source %q", q.Source)
	}
	seen := map[string]bool{}
	for _, segment := range segments[1:] {
		stage := strings.TrimSpace(segment)
		name, rest := cutWord(stage)
		if name == "" {
			return Query{}, fmt.Errorf("empty pipeline stage")
		}
		if name != "where" && seen[name] {
			return Query{}, fmt.Errorf("stage %q may only appear once", name)
		}
		seen[name] = true
		switch name {
		case "scope":
			q.Scope, err = parseScope(rest)
		case "where":
			var expression Expr
			expression, err = parseExpression(rest)
			if err == nil {
				if q.Filter == nil {
					q.Filter = expression
				} else {
					q.Filter = LogicalExpr{Operator: "and", Left: q.Filter, Right: expression}
				}
				if q.FilterText == "" {
					q.FilterText = strings.TrimSpace(rest)
				} else {
					q.FilterText += " and " + strings.TrimSpace(rest)
				}
			}
		case "range":
			q.Range, err = parseRange(rest, now)
		case "step":
			q.Step, err = time.ParseDuration(strings.TrimSpace(rest))
			if err == nil && q.Step <= 0 {
				err = fmt.Errorf("step must be positive")
			}
		case "include":
			if strings.TrimSpace(rest) != "native" {
				err = fmt.Errorf("include only supports native")
			} else {
				q.IncludeNative = true
			}
		case "fields":
			q.Fields, err = parseFields(rest)
		case "summarize":
			q.Aggregates, q.GroupBy, err = parseSummarize(rest)
		case "sort":
			q.Sort, err = parseSort(rest)
		case "limit":
			q.Limit, err = parseLimit(rest)
		default:
			err = fmt.Errorf("unsupported stage %q", name)
		}
		if err != nil {
			return Query{}, fmt.Errorf("%s: %w", name, err)
		}
	}
	if err := Validate(q); err != nil {
		return Query{}, err
	}
	return q, nil
}

func validSource(s model.Source) bool {
	for _, candidate := range model.Sources {
		if s == candidate {
			return true
		}
	}
	return false
}

func splitPipeline(input string) ([]string, error) {
	var parts []string
	start, depth := 0, 0
	var quote rune
	for i, r := range input {
		if quote != 0 {
			if r == quote && (i == 0 || input[i-1] != '\\') {
				quote = 0
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
		case '(', '[':
			depth++
		case ')', ']':
			depth--
			if depth < 0 {
				return nil, fmt.Errorf("unbalanced delimiter")
			}
		case '|':
			if depth == 0 {
				parts = append(parts, strings.TrimSpace(input[start:i]))
				start = i + 1
			}
		}
	}
	if quote != 0 || depth != 0 {
		return nil, fmt.Errorf("unterminated quote or delimiter")
	}
	parts = append(parts, strings.TrimSpace(input[start:]))
	return parts, nil
}

func cutWord(s string) (string, string) {
	s = strings.TrimSpace(s)
	for i, r := range s {
		if unicode.IsSpace(r) {
			return strings.ToLower(s[:i]), strings.TrimSpace(s[i:])
		}
	}
	return strings.ToLower(s), ""
}

func parseScope(rest string) (Scope, error) {
	var scope Scope
	clauses, err := splitComma(rest)
	if err != nil {
		return scope, err
	}
	seen := map[string]bool{}
	for _, clause := range clauses {
		parts := strings.SplitN(strings.TrimSpace(clause), " in ", 2)
		if len(parts) != 2 {
			return scope, fmt.Errorf("scope clause must use <field> in (...)")
		}
		field := strings.TrimSpace(parts[0])
		if seen[field] {
			return scope, fmt.Errorf("duplicate scope field %q", field)
		}
		seen[field] = true
		values, err := parseStringList(parts[1])
		if err != nil {
			return scope, err
		}
		switch field {
		case "profile":
			scope.Profiles = values
		case "provider":
			for _, value := range values {
				scope.Providers = append(scope.Providers, model.Provider(value))
			}
		case "account":
			scope.Accounts = values
		case "region":
			scope.Regions = values
		default:
			return scope, fmt.Errorf("unsupported scope field %q", field)
		}
	}
	return scope, nil
}

func parseStringList(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '(' || s[len(s)-1] != ')' {
		return nil, fmt.Errorf("list must be enclosed in parentheses")
	}
	items, err := splitComma(s[1 : len(s)-1])
	if err != nil {
		return nil, err
	}
	values := make([]string, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		value, err := strconv.Unquote(strings.TrimSpace(item))
		if err != nil {
			return nil, fmt.Errorf("scope values must be quoted strings")
		}
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("scope values cannot be empty")
		}
		if seen[value] {
			return nil, fmt.Errorf("duplicate scope value %q", value)
		}
		seen[value] = true
		values = append(values, value)
	}
	return values, nil
}

func parseFields(rest string) ([]string, error) {
	items, err := splitComma(rest)
	if err != nil {
		return nil, err
	}
	fields := make([]string, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		field := strings.TrimSpace(item)
		if !FieldAllowed(field) {
			return nil, fmt.Errorf("unsupported field %q", field)
		}
		if seen[field] {
			return nil, fmt.Errorf("duplicate field %q", field)
		}
		seen[field] = true
		fields = append(fields, field)
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("at least one field is required")
	}
	return fields, nil
}

func parseSort(rest string) ([]SortField, error) {
	items, err := splitComma(rest)
	if err != nil {
		return nil, err
	}
	result := make([]SortField, 0, len(items))
	for _, item := range items {
		parts := strings.Fields(item)
		if len(parts) == 0 || len(parts) > 2 || !FieldAllowed(parts[0]) {
			return nil, fmt.Errorf("invalid sort expression %q", item)
		}
		direction := "asc"
		if len(parts) == 2 {
			direction = strings.ToLower(parts[1])
		}
		if direction != "asc" && direction != "desc" {
			return nil, fmt.Errorf("sort direction must be asc or desc")
		}
		result = append(result, SortField{Field: parts[0], Direction: direction})
	}
	return result, nil
}

func parseLimit(rest string) (int, error) {
	limit, err := strconv.Atoi(strings.TrimSpace(rest))
	if err != nil || limit <= 0 || limit > 50_000 {
		return 0, fmt.Errorf("limit must be between 1 and 50000")
	}
	return limit, nil
}

func parseRange(rest string, now time.Time) (*TimeRange, error) {
	rest = strings.TrimSpace(rest)
	if strings.HasPrefix(rest, "last ") {
		duration, err := time.ParseDuration(strings.TrimSpace(strings.TrimPrefix(rest, "last ")))
		if err != nil || duration <= 0 {
			return nil, fmt.Errorf("invalid relative duration")
		}
		return &TimeRange{Start: now.Add(-duration), End: now}, nil
	}
	parts := strings.SplitN(rest, " to ", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("range must be 'last <duration>' or '<start> to <end>'")
	}
	start, err := parseTime(strings.TrimSpace(parts[0]))
	if err != nil {
		return nil, err
	}
	end, err := parseTime(strings.TrimSpace(parts[1]))
	if err != nil {
		return nil, err
	}
	if !start.Before(end) {
		return nil, fmt.Errorf("range start must be before end")
	}
	return &TimeRange{Start: start, End: end}, nil
}

func parseTime(value string) (time.Time, error) {
	value = strings.Trim(value, "\"")
	for _, layout := range []string{time.RFC3339, time.DateOnly} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("time %q must be RFC3339 or YYYY-MM-DD", value)
}

func parseSummarize(rest string) ([]Aggregate, []string, error) {
	parts := strings.SplitN(rest, " by ", 2)
	aggParts, err := splitComma(parts[0])
	if err != nil {
		return nil, nil, err
	}
	aggs := make([]Aggregate, 0, len(aggParts))
	aliases := map[string]bool{}
	for _, item := range aggParts {
		item = strings.TrimSpace(item)
		open, close := strings.Index(item, "("), strings.Index(item, ")")
		if open <= 0 || close < open || strings.TrimSpace(item[close+1:]) != "" {
			return nil, nil, fmt.Errorf("invalid aggregate %q", item)
		}
		fn, field := strings.ToLower(strings.TrimSpace(item[:open])), strings.TrimSpace(item[open+1:close])
		if fn != "count" && fn != "sum" && fn != "avg" && fn != "min" && fn != "max" {
			return nil, nil, fmt.Errorf("unsupported aggregate %q", fn)
		}
		if fn == "count" && field == "" {
			field = "*"
		}
		if field != "*" && !FieldAllowed(field) {
			return nil, nil, fmt.Errorf("unsupported aggregate field %q", field)
		}
		alias := fn
		if field != "*" {
			alias += "_" + sanitizeAlias(field)
		}
		if aliases[alias] {
			return nil, nil, fmt.Errorf("duplicate aggregate output %q", alias)
		}
		aliases[alias] = true
		aggs = append(aggs, Aggregate{Function: fn, Field: field, Alias: alias})
	}
	var groupBy []string
	if len(parts) == 2 {
		groupBy, err = parseFields(parts[1])
	}
	return aggs, groupBy, err
}

func sanitizeAlias(field string) string {
	var result strings.Builder
	lastUnderscore := false
	for _, r := range field {
		valid := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if valid {
			result.WriteRune(r)
			lastUnderscore = false
		} else if !lastUnderscore {
			result.WriteByte('_')
			lastUnderscore = true
		}
	}
	return strings.Trim(result.String(), "_")
}

func splitComma(input string) ([]string, error) {
	var parts []string
	start, depth := 0, 0
	var quote rune
	for i, r := range input {
		if quote != 0 {
			if r == quote && (i == 0 || input[i-1] != '\\') {
				quote = 0
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		case ',':
			if depth == 0 {
				item := strings.TrimSpace(input[start:i])
				if item == "" {
					return nil, fmt.Errorf("empty comma-separated item")
				}
				parts = append(parts, item)
				start = i + 1
			}
		}
		if depth < 0 {
			return nil, fmt.Errorf("unbalanced delimiter")
		}
	}
	if quote != 0 || depth != 0 {
		return nil, fmt.Errorf("unterminated quote or delimiter")
	}
	last := strings.TrimSpace(input[start:])
	if last == "" {
		if len(parts) > 0 {
			return nil, fmt.Errorf("trailing comma is not allowed")
		}
		return nil, fmt.Errorf("at least one item is required")
	}
	parts = append(parts, last)
	return parts, nil
}
