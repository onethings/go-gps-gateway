// Package expr evaluates the small expression language the computed
// attributes use. The Java server evaluates those expressions with JEXL; this
// package implements the subset an attribute needs: position fields and
// attributes as variables, arithmetic, comparison, logic, a ternary and a few
// functions. It deliberately has no statement syntax, so a stored expression
// can never do anything but produce a value.
package expr

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Lookup resolves a variable name. A name that is not known is an error rather
// than a zero value, because a typo in a stored attribute should be reported
// instead of silently writing 0 to every position.
type Lookup func(name string) (any, bool)

// Eval parses and evaluates source with the variables lookup resolves.
func Eval(source string, lookup Lookup) (any, error) {
	tokens, err := lex(source)
	if err != nil {
		return nil, err
	}
	parser := &parser{tokens: tokens, lookup: lookup}
	value, err := parser.parseExpression()
	if err != nil {
		return nil, err
	}
	if parser.peek().kind != tokenEOF {
		return nil, fmt.Errorf("expr: unexpected %q at %d", parser.peek().text,
			parser.peek().position)
	}
	return value, nil
}

// Variables adapts a map to a Lookup.
func Variables(values map[string]any) Lookup {
	return func(name string) (any, bool) {
		value, ok := values[name]
		return value, ok
	}
}

type parser struct {
	tokens []token
	index  int
	lookup Lookup
}

func (p *parser) peek() token { return p.tokens[p.index] }

func (p *parser) advance() token {
	current := p.tokens[p.index]
	if current.kind != tokenEOF {
		p.index++
	}
	return current
}

func (p *parser) matchOperator(operators ...string) (string, bool) {
	current := p.peek()
	if current.kind != tokenOperator {
		return "", false
	}
	for _, operator := range operators {
		if current.text == operator {
			p.advance()
			return operator, true
		}
	}
	return "", false
}

func (p *parser) parseExpression() (any, error) {
	return p.parseTernary()
}

func (p *parser) parseTernary() (any, error) {
	condition, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if _, ok := p.matchOperator("?"); !ok {
		return condition, nil
	}
	whenTrue, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	if _, ok := p.matchOperator(":"); !ok {
		return nil, fmt.Errorf("expr: expected ':' at %d", p.peek().position)
	}
	whenFalse, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	if truthy(condition) {
		return whenTrue, nil
	}
	return whenFalse, nil
}

func (p *parser) parseOr() (any, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for {
		if _, ok := p.matchOperator("||"); !ok {
			return left, nil
		}
		// Both sides are parsed either way, so a short circuit still validates
		// the whole expression; only the result uses the left side.
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = truthy(left) || truthy(right)
	}
}

func (p *parser) parseAnd() (any, error) {
	left, err := p.parseEquality()
	if err != nil {
		return nil, err
	}
	for {
		if _, ok := p.matchOperator("&&"); !ok {
			return left, nil
		}
		right, err := p.parseEquality()
		if err != nil {
			return nil, err
		}
		left = truthy(left) && truthy(right)
	}
}

func (p *parser) parseEquality() (any, error) {
	left, err := p.parseComparison()
	if err != nil {
		return nil, err
	}
	for {
		operator, ok := p.matchOperator("==", "!=")
		if !ok {
			return left, nil
		}
		right, err := p.parseComparison()
		if err != nil {
			return nil, err
		}
		equal, err := equals(left, right)
		if err != nil {
			return nil, err
		}
		if operator == "!=" {
			equal = !equal
		}
		left = equal
	}
}

func (p *parser) parseComparison() (any, error) {
	left, err := p.parseAdditive()
	if err != nil {
		return nil, err
	}
	for {
		operator, ok := p.matchOperator("<", "<=", ">", ">=")
		if !ok {
			return left, nil
		}
		right, err := p.parseAdditive()
		if err != nil {
			return nil, err
		}
		comparison, err := compare(left, right)
		if err != nil {
			return nil, err
		}
		switch operator {
		case "<":
			left = comparison < 0
		case "<=":
			left = comparison <= 0
		case ">":
			left = comparison > 0
		case ">=":
			left = comparison >= 0
		}
	}
}

func (p *parser) parseAdditive() (any, error) {
	left, err := p.parseMultiplicative()
	if err != nil {
		return nil, err
	}
	for {
		operator, ok := p.matchOperator("+", "-")
		if !ok {
			return left, nil
		}
		right, err := p.parseMultiplicative()
		if err != nil {
			return nil, err
		}
		left, err = additive(operator, left, right)
		if err != nil {
			return nil, err
		}
	}
}

func (p *parser) parseMultiplicative() (any, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		operator, ok := p.matchOperator("*", "/", "%")
		if !ok {
			return left, nil
		}
		right, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		leftNumber, err := toNumber(left)
		if err != nil {
			return nil, err
		}
		rightNumber, err := toNumber(right)
		if err != nil {
			return nil, err
		}
		switch operator {
		case "*":
			left = leftNumber * rightNumber
		case "/":
			if rightNumber == 0 {
				return nil, fmt.Errorf("expr: division by zero")
			}
			left = leftNumber / rightNumber
		case "%":
			if rightNumber == 0 {
				return nil, fmt.Errorf("expr: division by zero")
			}
			left = math.Mod(leftNumber, rightNumber)
		}
	}
}

func (p *parser) parseUnary() (any, error) {
	if operator, ok := p.matchOperator("!", "-", "+"); ok {
		value, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		switch operator {
		case "!":
			return !truthy(value), nil
		case "-":
			number, err := toNumber(value)
			if err != nil {
				return nil, err
			}
			return -number, nil
		default:
			return toNumber(value)
		}
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (any, error) {
	current := p.advance()
	switch current.kind {
	case tokenNumber:
		return current.number, nil
	case tokenString:
		return current.text, nil
	case tokenIdent:
		switch strings.ToLower(current.text) {
		case "true":
			return true, nil
		case "false":
			return false, nil
		case "null", "nil":
			return nil, nil
		}
		if p.peek().kind == tokenLeftParen {
			return p.parseCall(current)
		}
		if p.lookup == nil {
			return nil, fmt.Errorf("expr: unknown variable %q", current.text)
		}
		value, ok := p.lookup(current.text)
		if !ok {
			return nil, fmt.Errorf("expr: unknown variable %q", current.text)
		}
		return value, nil
	case tokenLeftParen:
		value, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		if p.advance().kind != tokenRightParen {
			return nil, fmt.Errorf("expr: expected ')' at %d", current.position)
		}
		return value, nil
	default:
		return nil, fmt.Errorf("expr: unexpected %q at %d", current.text, current.position)
	}
}

func (p *parser) parseCall(name token) (any, error) {
	p.advance() // consume '('
	var arguments []any
	if p.peek().kind != tokenRightParen {
		for {
			value, err := p.parseExpression()
			if err != nil {
				return nil, err
			}
			arguments = append(arguments, value)
			if p.peek().kind != tokenComma {
				break
			}
			p.advance()
		}
	}
	if p.advance().kind != tokenRightParen {
		return nil, fmt.Errorf("expr: expected ')' after %s at %d", name.text, name.position)
	}
	return call(strings.ToLower(name.text), arguments)
}

func call(name string, arguments []any) (any, error) {
	numbers := func() ([]float64, error) {
		values := make([]float64, len(arguments))
		for index, argument := range arguments {
			number, err := toNumber(argument)
			if err != nil {
				return nil, err
			}
			values[index] = number
		}
		return values, nil
	}

	switch name {
	case "abs", "ceil", "floor", "round", "sqrt":
		values, err := numbers()
		if err != nil {
			return nil, err
		}
		if len(values) != 1 {
			return nil, fmt.Errorf("expr: %s needs one argument", name)
		}
		switch name {
		case "abs":
			return math.Abs(values[0]), nil
		case "ceil":
			return math.Ceil(values[0]), nil
		case "floor":
			return math.Floor(values[0]), nil
		case "round":
			return math.Round(values[0]), nil
		default:
			if values[0] < 0 {
				return nil, fmt.Errorf("expr: sqrt of a negative number")
			}
			return math.Sqrt(values[0]), nil
		}
	case "pow":
		values, err := numbers()
		if err != nil {
			return nil, err
		}
		if len(values) != 2 {
			return nil, fmt.Errorf("expr: pow needs two arguments")
		}
		return math.Pow(values[0], values[1]), nil
	case "min", "max":
		values, err := numbers()
		if err != nil {
			return nil, err
		}
		if len(values) == 0 {
			return nil, fmt.Errorf("expr: %s needs an argument", name)
		}
		result := values[0]
		for _, value := range values[1:] {
			if (name == "min" && value < result) || (name == "max" && value > result) {
				result = value
			}
		}
		return result, nil
	case "coalesce":
		for _, argument := range arguments {
			if argument != nil && argument != "" {
				return argument, nil
			}
		}
		return nil, nil
	case "if":
		if len(arguments) != 3 {
			return nil, fmt.Errorf("expr: if needs three arguments")
		}
		if truthy(arguments[0]) {
			return arguments[1], nil
		}
		return arguments[2], nil
	case "length":
		if len(arguments) != 1 {
			return nil, fmt.Errorf("expr: length needs one argument")
		}
		switch value := arguments[0].(type) {
		case string:
			return float64(len([]rune(value))), nil
		case []any:
			return float64(len(value)), nil
		default:
			return float64(len(fmt.Sprint(value))), nil
		}
	}
	return nil, fmt.Errorf("expr: unknown function %q", name)
}

func additive(operator string, left, right any) (any, error) {
	if operator == "+" && (isString(left) || isString(right)) {
		return stringValue(left) + stringValue(right), nil
	}
	leftNumber, err := toNumber(left)
	if err != nil {
		return nil, err
	}
	rightNumber, err := toNumber(right)
	if err != nil {
		return nil, err
	}
	if operator == "+" {
		return leftNumber + rightNumber, nil
	}
	return leftNumber - rightNumber, nil
}

func isString(value any) bool {
	_, ok := value.(string)
	return ok
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	if number, ok := value.(float64); ok && number == math.Trunc(number) {
		return strconv.FormatFloat(number, 'f', 0, 64)
	}
	return fmt.Sprint(value)
}

func truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case float64:
		return typed != 0
	case float32:
		return typed != 0
	case int:
		return typed != 0
	case int64:
		return typed != 0
	default:
		return true
	}
}

func toNumber(value any) (float64, error) {
	switch typed := value.(type) {
	case nil:
		return 0, nil
	case float64:
		return typed, nil
	case float32:
		return float64(typed), nil
	case int:
		return float64(typed), nil
	case int32:
		return float64(typed), nil
	case int64:
		return float64(typed), nil
	case uint:
		return float64(typed), nil
	case uint32:
		return float64(typed), nil
	case uint64:
		return float64(typed), nil
	case bool:
		if typed {
			return 1, nil
		}
		return 0, nil
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return 0, fmt.Errorf("expr: %q is not a number", typed)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("expr: %T is not a number", value)
	}
}

func equals(left, right any) (bool, error) {
	leftNumber, leftErr := toNumber(left)
	rightNumber, rightErr := toNumber(right)
	if leftErr == nil && rightErr == nil {
		return leftNumber == rightNumber, nil
	}
	leftText, leftIsText := left.(string)
	rightText, rightIsText := right.(string)
	if leftIsText && rightIsText {
		return leftText == rightText, nil
	}
	return truthy(left) == truthy(right), nil
}

func compare(left, right any) (int, error) {
	leftNumber, leftErr := toNumber(left)
	rightNumber, rightErr := toNumber(right)
	if leftErr == nil && rightErr == nil {
		switch {
		case leftNumber < rightNumber:
			return -1, nil
		case leftNumber > rightNumber:
			return 1, nil
		default:
			return 0, nil
		}
	}
	leftText, leftIsText := left.(string)
	rightText, rightIsText := right.(string)
	if leftIsText && rightIsText {
		return strings.Compare(leftText, rightText), nil
	}
	if leftErr != nil {
		return 0, leftErr
	}
	return 0, rightErr
}
