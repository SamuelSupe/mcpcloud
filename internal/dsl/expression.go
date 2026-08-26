package dsl

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

type tokenKind int

const (
	tokenEOF tokenKind = iota
	tokenWord
	tokenString
	tokenNumber
	tokenBool
	tokenOperator
	tokenLeftParen
	tokenRightParen
	tokenComma
)

type token struct {
	kind  tokenKind
	text  string
	value any
}

type expressionParser struct {
	tokens []token
	pos    int
}

func parseExpression(input string) (Expr, error) {
	tokens, err := lexExpression(input)
	if err != nil {
		return nil, err
	}
	p := &expressionParser{tokens: tokens}
	expr, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != tokenEOF {
		return nil, fmt.Errorf("unexpected token %q", p.peek().text)
	}
	return expr, nil
}

func (p *expressionParser) parseOr() (Expr, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.matchWord("or") {
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = LogicalExpr{Operator: "or", Left: left, Right: right}
	}
	return left, nil
}

func (p *expressionParser) parseAnd() (Expr, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for p.matchWord("and") {
		right, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		left = LogicalExpr{Operator: "and", Left: left, Right: right}
	}
	return left, nil
}

func (p *expressionParser) parseUnary() (Expr, error) {
	if p.matchWord("not") {
		inner, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return NotExpr{Inner: inner}, nil
	}
	if p.peek().kind == tokenLeftParen {
		p.pos++
		expr, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if p.peek().kind != tokenRightParen {
			return nil, fmt.Errorf("missing closing parenthesis")
		}
		p.pos++
		return expr, nil
	}
	return p.parsePredicate()
}

func (p *expressionParser) parsePredicate() (Expr, error) {
	field := p.peek()
	if field.kind != tokenWord || !FieldAllowed(field.text) {
		return nil, fmt.Errorf("expected a supported field, got %q", field.text)
	}
	p.pos++
	op := strings.ToLower(p.peek().text)
	if p.peek().kind != tokenOperator && p.peek().kind != tokenWord {
		return nil, fmt.Errorf("expected operator after %s", field.text)
	}
	p.pos++
	if op == "exists" {
		return Predicate{Field: field.text, Operator: op}, nil
	}
	if op == "in" {
		if p.peek().kind != tokenLeftParen {
			return nil, fmt.Errorf("in requires a parenthesized list")
		}
		p.pos++
		var values []any
		if p.peek().kind == tokenRightParen {
			return nil, fmt.Errorf("in requires at least one value")
		}
		for p.peek().kind != tokenRightParen {
			value, err := p.parseValue()
			if err != nil {
				return nil, err
			}
			values = append(values, value)
			if p.peek().kind == tokenComma {
				p.pos++
				if p.peek().kind == tokenRightParen {
					return nil, fmt.Errorf("trailing comma is not allowed in an in list")
				}
				continue
			}
			if p.peek().kind != tokenRightParen {
				return nil, fmt.Errorf("expected comma or closing parenthesis")
			}
		}
		p.pos++
		return Predicate{Field: field.text, Operator: op, Value: values}, nil
	}
	valid := map[string]bool{"==": true, "!=": true, ">": true, ">=": true, "<": true, "<=": true, "contains": true, "starts_with": true, "ends_with": true}
	if !valid[op] {
		return nil, fmt.Errorf("unsupported operator %q", op)
	}
	value, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	return Predicate{Field: field.text, Operator: op, Value: value}, nil
}

func (p *expressionParser) parseValue() (any, error) {
	t := p.peek()
	switch t.kind {
	case tokenString, tokenNumber, tokenBool:
		p.pos++
		return t.value, nil
	default:
		return nil, fmt.Errorf("expected literal value, got %q", t.text)
	}
}

func (p *expressionParser) peek() token {
	if p.pos >= len(p.tokens) {
		return token{kind: tokenEOF}
	}
	return p.tokens[p.pos]
}
func (p *expressionParser) matchWord(word string) bool {
	if p.peek().kind == tokenWord && strings.EqualFold(p.peek().text, word) {
		p.pos++
		return true
	}
	return false
}

func lexExpression(input string) ([]token, error) {
	var tokens []token
	for i := 0; i < len(input); {
		r := rune(input[i])
		if unicode.IsSpace(r) {
			i++
			continue
		}
		switch input[i] {
		case '(':
			tokens = append(tokens, token{kind: tokenLeftParen, text: "("})
			i++
			continue
		case ')':
			tokens = append(tokens, token{kind: tokenRightParen, text: ")"})
			i++
			continue
		case ',':
			tokens = append(tokens, token{kind: tokenComma, text: ","})
			i++
			continue
		case '\'', '"':
			quote, start := input[i], i
			i++
			for i < len(input) && input[i] != quote {
				if input[i] == '\\' {
					i++
				}
				i++
			}
			if i >= len(input) {
				return nil, fmt.Errorf("unterminated string")
			}
			raw := input[start : i+1]
			i++
			if quote == '\'' {
				raw = "\"" + strings.ReplaceAll(raw[1:len(raw)-1], "\"", "\\\"") + "\""
			}
			value, err := strconv.Unquote(raw)
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, token{kind: tokenString, text: raw, value: value})
			continue
		}
		if strings.ContainsRune("=!><", r) {
			start := i
			i++
			if i < len(input) && input[i] == '=' {
				i++
			}
			op := input[start:i]
			if op == "=" || op == "!" {
				return nil, fmt.Errorf("invalid operator %q", op)
			}
			tokens = append(tokens, token{kind: tokenOperator, text: op})
			continue
		}
		if unicode.IsDigit(r) || (input[i] == '-' && i+1 < len(input) && unicode.IsDigit(rune(input[i+1]))) {
			start := i
			i++
			for i < len(input) && (unicode.IsDigit(rune(input[i])) || input[i] == '.') {
				i++
			}
			raw := input[start:i]
			value, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, token{kind: tokenNumber, text: raw, value: value})
			continue
		}
		if unicode.IsLetter(r) || input[i] == '_' {
			start := i
			i++
			for i < len(input) && (unicode.IsLetter(rune(input[i])) || unicode.IsDigit(rune(input[i])) || strings.ContainsRune("_.", rune(input[i]))) {
				i++
			}
			if i < len(input) && input[i] == '[' {
				bracket := i
				for i < len(input) && input[i] != ']' {
					i++
				}
				if i >= len(input) {
					return nil, fmt.Errorf("unterminated field index")
				}
				i++
				if bracket+3 > i || (input[bracket+1] != '"' && input[bracket+1] != '\'') {
					return nil, fmt.Errorf("field index must be quoted")
				}
			}
			word := input[start:i]
			lower := strings.ToLower(word)
			switch lower {
			case "true":
				tokens = append(tokens, token{kind: tokenBool, text: word, value: true})
			case "false":
				tokens = append(tokens, token{kind: tokenBool, text: word, value: false})
			case "in", "contains", "starts_with", "ends_with", "exists":
				tokens = append(tokens, token{kind: tokenOperator, text: lower})
			default:
				tokens = append(tokens, token{kind: tokenWord, text: word})
			}
			continue
		}
		return nil, fmt.Errorf("unexpected character %q", input[i])
	}
	return append(tokens, token{kind: tokenEOF}), nil
}
