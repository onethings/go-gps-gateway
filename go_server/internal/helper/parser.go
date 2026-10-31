package helper

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// CoordinateFormat enumerates the coordinate encodings understood by Parser,
// mirroring Parser.CoordinateFormat.
type CoordinateFormat int

// Supported coordinate formats.
const (
	DegDeg CoordinateFormat = iota
	DegDegHem
	DegHem
	DegMinMin
	DegMinHem
	DegMinMinHem
	HemDegMinMin
	HemDeg
	HemDegMin
	HemDegMinHem
)

// DateTimeFormat enumerates the date/time orderings understood by Parser.
type DateTimeFormat int

// Supported date/time formats.
const (
	HMS DateTimeFormat = iota
	SMH
	HMSYMD
	HMSDMY
	SMHYMD
	SMHDMY
	DMYHMS
	DMYHMSS
	YMDHMS
	YMDHMSS
)

// Parser is a port of org.traccar.helper.Parser: it walks the capture groups of
// a regular expression in order, exactly like the Java decoders expect.
type Parser struct {
	groups   []string
	position int
	matched  bool
}

// NewParser creates a parser for input, evaluating the pattern immediately.
func NewParser(pattern *regexp.Regexp, input string) *Parser {
	groups := pattern.FindStringSubmatch(input)
	return &Parser{groups: groups, matched: groups != nil}
}

// NewParserFromGroups creates a parser from an already matched group slice.
func NewParserFromGroups(groups []string) *Parser {
	return &Parser{groups: groups, matched: len(groups) > 0}
}

// Matches reports whether the pattern matched.
func (p *Parser) Matches() bool {
	p.position = 1
	return p.matched
}

// Skip advances the group cursor without returning values.
func (p *Parser) Skip(number int) {
	p.position += number
}

// Reset moves the cursor back to the first group.
func (p *Parser) Reset() {
	p.position = 1
}

func (p *Parser) group(index int) string {
	if index < 0 || index >= len(p.groups) {
		return ""
	}
	return p.groups[index]
}

// HasNext reports whether the next capture group holds a value.
func (p *Parser) HasNext() bool {
	return p.HasNextN(1)
}

// HasNextN reports whether the next number of capture groups all hold values.
// The cursor is advanced past them when any of them is empty, matching the Java
// semantics.
func (p *Parser) HasNextN(number int) bool {
	for i := p.position; i < p.position+number; i++ {
		if p.group(i) == "" {
			p.position += number
			return false
		}
	}
	return true
}

// HasNextAny reports whether any of the next number of groups holds a value.
func (p *Parser) HasNextAny(number int) bool {
	for i := p.position; i < p.position+number; i++ {
		if p.group(i) != "" {
			return true
		}
	}
	p.position += number
	return false
}

// Next returns the next capture group and advances the cursor.
func (p *Parser) Next() string {
	v := p.group(p.position)
	p.position++
	return v
}

// NextInt returns the next group parsed as a base 10 integer.
func (p *Parser) NextInt(defaultValue int) int {
	if p.HasNext() {
		v, err := strconv.Atoi(strings.TrimSpace(p.Next()))
		if err == nil {
			return v
		}
	}
	return defaultValue
}

// NextIntPtr returns the next group as an optional integer.
func (p *Parser) NextIntPtr() *int {
	if !p.HasNext() {
		return nil
	}
	v, err := strconv.Atoi(strings.TrimSpace(p.Next()))
	if err != nil {
		return nil
	}
	return &v
}

// NextHexInt returns the next group parsed as a hexadecimal integer.
func (p *Parser) NextHexInt(defaultValue int) int {
	if p.HasNext() {
		v, err := strconv.ParseInt(strings.TrimSpace(p.Next()), 16, 64)
		if err == nil {
			return int(v)
		}
	}
	return defaultValue
}

// NextHexLong returns the next group parsed as a hexadecimal 64 bit integer.
func (p *Parser) NextHexLong(defaultValue int64) int64 {
	if p.HasNext() {
		v, err := strconv.ParseInt(strings.TrimSpace(p.Next()), 16, 64)
		if err == nil {
			return v
		}
	}
	return defaultValue
}

// NextBinInt returns the next group parsed as a binary integer.
func (p *Parser) NextBinInt(defaultValue int) int {
	if p.HasNext() {
		v, err := strconv.ParseInt(strings.TrimSpace(p.Next()), 2, 64)
		if err == nil {
			return int(v)
		}
	}
	return defaultValue
}

// NextLong returns the next group parsed as a 64 bit integer in the given radix.
func (p *Parser) NextLong(radix int, defaultValue int64) int64 {
	if p.HasNext() {
		v, err := strconv.ParseInt(strings.TrimSpace(p.Next()), radix, 64)
		if err == nil {
			return v
		}
	}
	return defaultValue
}

// NextDouble returns the next group parsed as a floating point number.
func (p *Parser) NextDouble(defaultValue float64) float64 {
	if p.HasNext() {
		v, err := strconv.ParseFloat(strings.TrimSpace(p.Next()), 64)
		if err == nil {
			return v
		}
	}
	return defaultValue
}

// NextCoordinate decodes the next capture groups according to the format.
func (p *Parser) NextCoordinate(format CoordinateFormat) float64 {
	var coordinate float64
	hemisphere := ""

	switch format {
	case DegDeg:
		coordinate = parseJoinedDecimal(p.Next(), p.Next())
	case DegDegHem:
		coordinate = parseJoinedDecimal(p.Next(), p.Next())
		hemisphere = p.Next()
	case DegHem:
		coordinate = p.NextDouble(0)
		hemisphere = p.Next()
	case DegMinMin:
		coordinate = float64(p.NextInt(0))
		coordinate += parseJoinedDecimal(p.Next(), p.Next()) / 60
	case DegMinMinHem:
		coordinate = float64(p.NextInt(0))
		coordinate += parseJoinedDecimal(p.Next(), p.Next()) / 60
		hemisphere = p.Next()
	case HemDeg:
		hemisphere = p.Next()
		coordinate = p.NextDouble(0)
	case HemDegMin:
		hemisphere = p.Next()
		coordinate = float64(p.NextInt(0))
		coordinate += p.NextDouble(0) / 60
	case HemDegMinHem:
		hemisphere = p.Next()
		coordinate = float64(p.NextInt(0))
		coordinate += p.NextDouble(0) / 60
		if p.HasNext() {
			hemisphere = p.Next()
		}
	case HemDegMinMin:
		hemisphere = p.Next()
		coordinate = float64(p.NextInt(0))
		coordinate += parseJoinedDecimal(p.Next(), p.Next()) / 60
	default: // DegMinHem
		coordinate = float64(p.NextInt(0))
		coordinate += p.NextDouble(0) / 60
		hemisphere = p.Next()
	}

	if hemisphere == "S" || hemisphere == "W" || hemisphere == "-" {
		coordinate = -absFloat(coordinate)
	}

	return coordinate
}

// NextDateTime builds a time value from the next capture groups.
func (p *Parser) NextDateTime(format DateTimeFormat) time.Time {
	var year, month, day int
	var hour, minute, second, millisecond int

	switch format {
	case HMS:
		hour = p.NextInt(0)
		minute = p.NextInt(0)
		second = p.NextInt(0)
	case SMH:
		second = p.NextInt(0)
		minute = p.NextInt(0)
		hour = p.NextInt(0)
	case HMSYMD:
		hour = p.NextInt(0)
		minute = p.NextInt(0)
		second = p.NextInt(0)
		year = p.NextInt(0)
		month = p.NextInt(0)
		day = p.NextInt(0)
	case HMSDMY:
		hour = p.NextInt(0)
		minute = p.NextInt(0)
		second = p.NextInt(0)
		day = p.NextInt(0)
		month = p.NextInt(0)
		year = p.NextInt(0)
	case SMHYMD:
		second = p.NextInt(0)
		minute = p.NextInt(0)
		hour = p.NextInt(0)
		year = p.NextInt(0)
		month = p.NextInt(0)
		day = p.NextInt(0)
	case SMHDMY:
		second = p.NextInt(0)
		minute = p.NextInt(0)
		hour = p.NextInt(0)
		day = p.NextInt(0)
		month = p.NextInt(0)
		year = p.NextInt(0)
	case DMYHMS, DMYHMSS:
		day = p.NextInt(0)
		month = p.NextInt(0)
		year = p.NextInt(0)
		hour = p.NextInt(0)
		minute = p.NextInt(0)
		second = p.NextInt(0)
	default:
		year = p.NextInt(0)
		month = p.NextInt(0)
		day = p.NextInt(0)
		hour = p.NextInt(0)
		minute = p.NextInt(0)
		second = p.NextInt(0)
	}

	if format == YMDHMSS || format == DMYHMSS {
		millisecond = p.NextInt(0)
	}

	if year >= 0 && year < 100 {
		year += 2000
	}

	var builder *DateBuilder
	if format != HMS && format != SMH {
		builder = NewDateBuilder()
		builder.SetDate(year, month, day)
	} else {
		builder = NewDateBuilderFrom(time.Now())
	}

	builder.SetTimeMillis(hour, minute, second, millisecond)
	return builder.Date()
}

func parseJoinedDecimal(left, right string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(left)+"."+strings.TrimSpace(right), 64)
	if err != nil {
		return 0
	}
	return v
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
