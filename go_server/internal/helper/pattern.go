package helper

import (
	"regexp"
	"strings"
)

// PatternBuilder is a port of org.traccar.helper.PatternBuilder. It assembles
// a Java-style regular expression from readable fragments so the Go ports can
// keep the exact same expressions as the Java decoders.
type PatternBuilder struct {
	fragments []string
}

// NewPatternBuilder starts an empty builder.
func NewPatternBuilder() *PatternBuilder {
	return &PatternBuilder{}
}

// Optional makes the last count fragments optional.
func (b *PatternBuilder) Optional(count int) *PatternBuilder {
	index := len(b.fragments) - count
	if index < 0 {
		index = 0
	}
	b.fragments = append(b.fragments[:index], append([]string{"(?:"}, b.fragments[index:]...)...)
	b.fragments = append(b.fragments, ")?")
	return b
}

// OptionalOne makes the last fragment optional.
func (b *PatternBuilder) OptionalOne() *PatternBuilder {
	return b.Optional(1)
}

// Expression inserts a raw regular expression fragment.
func (b *PatternBuilder) Expression(s string) *PatternBuilder {
	b.fragments = append(b.fragments, escapeTrailingPipe(s))
	return b
}

// Text inserts a literal fragment with all regex metacharacters escaped.
func (b *PatternBuilder) Text(s string) *PatternBuilder {
	b.fragments = append(b.fragments, regexp.QuoteMeta(s))
	return b
}

// Number inserts a numeric pattern using the Traccar shorthand: d for a decimal
// digit, x for a hex digit, dd/ddd/dddd and xx/xxx/xxxx for repetitions.
func (b *PatternBuilder) Number(s string) *PatternBuilder {
	s = strings.ReplaceAll(s, "dddd", "d{4}")
	s = strings.ReplaceAll(s, "ddd", "d{3}")
	s = strings.ReplaceAll(s, "dd", "d{2}")
	s = strings.ReplaceAll(s, "xxxx", "x{4}")
	s = strings.ReplaceAll(s, "xxx", "x{3}")
	s = strings.ReplaceAll(s, "xx", "x{2}")

	s = strings.ReplaceAll(s, "d", `\d`)
	s = strings.ReplaceAll(s, "x", "[0-9a-fA-F]")
	s = strings.ReplaceAll(s, ".", `\.`)
	s = escapeTrailingPipe(s)
	s = escapeLeadingPipe(s)

	b.fragments = append(b.fragments, s)
	return b
}

// Any inserts a match-anything fragment.
func (b *PatternBuilder) Any() *PatternBuilder {
	b.fragments = append(b.fragments, ".*")
	return b
}

// Binary inserts a hex string as escaped literal bytes.
func (b *PatternBuilder) Binary(s string) *PatternBuilder {
	re := regexp.MustCompile(`([0-9a-fA-F]{2})`)
	b.fragments = append(b.fragments, re.ReplaceAllString(s, `\$1`))
	return b
}

// Or inserts an alternation.
func (b *PatternBuilder) Or() *PatternBuilder {
	b.fragments = append(b.fragments, "|")
	return b
}

// GroupBegin opens a non-capturing group.
func (b *PatternBuilder) GroupBegin() *PatternBuilder {
	b.fragments = append(b.fragments, "(?:")
	return b
}

// GroupEnd closes a group.
func (b *PatternBuilder) GroupEnd() *PatternBuilder {
	b.fragments = append(b.fragments, ")")
	return b
}

// GroupEndWith closes a group and appends a suffix such as "?".
func (b *PatternBuilder) GroupEndWith(s string) *PatternBuilder {
	b.fragments = append(b.fragments, ")"+s)
	return b
}

// String renders the accumulated regular expression.
func (b *PatternBuilder) String() string {
	return strings.Join(b.fragments, "")
}

// Compile builds a compiled pattern anchored for full-string matching and with
// dot matching newlines, mirroring Pattern.compile(..., Pattern.DOTALL).
func (b *PatternBuilder) Compile() *regexp.Regexp {
	return regexp.MustCompile(`(?s)^(?:` + b.String() + `)$`)
}

func escapeTrailingPipe(s string) string {
	if strings.HasSuffix(s, "|") {
		return s[:len(s)-1] + `\|`
	}
	return s
}

func escapeLeadingPipe(s string) string {
	if strings.HasPrefix(s, "|") {
		return `\|` + s[1:]
	}
	return s
}
