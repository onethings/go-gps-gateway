package expr

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

type tokenKind int

const (
	tokenEOF tokenKind = iota
	tokenNumber
	tokenString
	tokenIdent
	tokenOperator
	tokenLeftParen
	tokenRightParen
	tokenComma
)

type token struct {
	kind     tokenKind
	text     string
	number   float64
	position int
}

func lex(source string) ([]token, error) {
	runes := []rune(source)
	tokens := make([]token, 0, len(runes))
	for index := 0; index < len(runes); {
		character := runes[index]
		switch {
		case unicode.IsSpace(character):
			index++
		case unicode.IsDigit(character) ||
			(character == '.' && index+1 < len(runes) && unicode.IsDigit(runes[index+1])):
			tokens, index = lexNumber(runes, index, tokens)
		case character == '"' || character == '\'':
			var next token
			var err error
			next, index, err = lexString(runes, index)
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, next)
		case unicode.IsLetter(character) || character == '_':
			start := index
			for index < len(runes) && (unicode.IsLetter(runes[index]) ||
				unicode.IsDigit(runes[index]) || runes[index] == '_') {
				index++
			}
			tokens = append(tokens, token{kind: tokenIdent,
				text: string(runes[start:index]), position: start})
		default:
			next, consumed, err := lexOperator(runes, index)
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, next)
			index += consumed
		}
	}
	tokens = append(tokens, token{kind: tokenEOF, position: len(runes)})
	return tokens, nil
}

func lexNumber(runes []rune, index int, tokens []token) ([]token, int) {
	start := index
	seenDot := false
	for index < len(runes) && (unicode.IsDigit(runes[index]) ||
		(runes[index] == '.' && !seenDot)) {
		if runes[index] == '.' {
			seenDot = true
		}
		index++
	}
	// An exponent, so 1e3 works the way it does in JEXL.
	if index < len(runes) && (runes[index] == 'e' || runes[index] == 'E') {
		index++
		if index < len(runes) && (runes[index] == '+' || runes[index] == '-') {
			index++
		}
		for index < len(runes) && unicode.IsDigit(runes[index]) {
			index++
		}
	}
	text := string(runes[start:index])
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		// The lexer only accepts a numeric shape, so this cannot normally
		// happen; report it as zero rather than losing the rest of the input.
		value = 0
	}
	return append(tokens, token{kind: tokenNumber, text: text, number: value,
		position: start}), index
}

func lexString(runes []rune, index int) (token, int, error) {
	quote := runes[index]
	start := index
	index++
	var builder strings.Builder
	closed := false
	for index < len(runes) {
		current := runes[index]
		if current == '\\' && index+1 < len(runes) {
			index++
			switch runes[index] {
			case 'n':
				builder.WriteRune('\n')
			case 't':
				builder.WriteRune('\t')
			default:
				builder.WriteRune(runes[index])
			}
			index++
			continue
		}
		if current == quote {
			closed = true
			index++
			break
		}
		builder.WriteRune(current)
		index++
	}
	if !closed {
		return token{}, index, fmt.Errorf("expr: unterminated string at %d", start)
	}
	return token{kind: tokenString, text: builder.String(), position: start}, index, nil
}

func lexOperator(runes []rune, index int) (token, int, error) {
	start := index
	two := ""
	if index+1 < len(runes) {
		two = string(runes[index : index+2])
	}
	switch two {
	case "&&", "||", "==", "!=", "<=", ">=":
		return token{kind: tokenOperator, text: two, position: start}, 2, nil
	}
	character := runes[index]
	switch character {
	case '(', ')', ',', '+', '-', '*', '/', '%', '<', '>', '!', '?', ':':
		kind := tokenOperator
		switch character {
		case '(':
			kind = tokenLeftParen
		case ')':
			kind = tokenRightParen
		case ',':
			kind = tokenComma
		}
		return token{kind: kind, text: string(character), position: start}, 1, nil
	}
	return token{}, 0, fmt.Errorf("expr: unexpected character %q at %d", character, start)
}
