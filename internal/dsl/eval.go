package dsl

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

func ValueAt(row map[string]any, path string) (any, bool) {
	if value, ok := row[path]; ok {
		return value, true
	}
	parts := splitPath(path)
	var current any = row
	for _, part := range parts {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func splitPath(path string) []string {
	path = strings.ReplaceAll(path, "[\"", ".")
	path = strings.ReplaceAll(path, "['", ".")
	path = strings.ReplaceAll(path, "\"]", "")
	path = strings.ReplaceAll(path, "']", "")
	parts := strings.Split(path, ".")
	out := parts[:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func compare(actual any, op string, expected any) (bool, error) {
	if op == "in" {
		values, ok := expected.([]any)
		if !ok {
			return false, fmt.Errorf("in requires a value list")
		}
		for _, value := range values {
			equal, _ := compare(actual, "==", value)
			if equal {
				return true, nil
			}
		}
		return false, nil
	}
	if op == "==" || op == "!=" {
		equal := valuesEqual(actual, expected)
		if op == "!=" {
			equal = !equal
		}
		return equal, nil
	}
	as, aok := scalarString(actual)
	es, eok := scalarString(expected)
	if !aok || !eok {
		return false, fmt.Errorf("operator %s requires scalar values", op)
	}
	switch op {
	case "contains":
		return strings.Contains(as, es), nil
	case "starts_with":
		return strings.HasPrefix(as, es), nil
	case "ends_with":
		return strings.HasSuffix(as, es), nil
	}
	af, aerr := strconv.ParseFloat(as, 64)
	ef, eerr := strconv.ParseFloat(es, 64)
	if aerr == nil && eerr == nil {
		switch op {
		case ">":
			return af > ef, nil
		case ">=":
			return af >= ef, nil
		case "<":
			return af < ef, nil
		case "<=":
			return af <= ef, nil
		}
	}
	switch op {
	case ">":
		return as > es, nil
	case ">=":
		return as >= es, nil
	case "<":
		return as < es, nil
	case "<=":
		return as <= es, nil
	default:
		return false, fmt.Errorf("unsupported operator %q", op)
	}
}

func valuesEqual(a, b any) bool {
	if af, ok := number(a); ok {
		if bf, ok := number(b); ok {
			return af == bf
		}
	}
	if reflect.DeepEqual(a, b) {
		return true
	}
	as, aok := scalarString(a)
	bs, bok := scalarString(b)
	return aok && bok && as == bs
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case float32:
		return float64(n), true
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func scalarString(v any) (string, bool) {
	switch value := v.(type) {
	case string:
		return value, true
	case bool:
		return strconv.FormatBool(value), true
	case fmt.Stringer:
		return value.String(), true
	default:
		if n, ok := number(v); ok {
			return strconv.FormatFloat(n, 'f', -1, 64), true
		}
		return "", false
	}
}
