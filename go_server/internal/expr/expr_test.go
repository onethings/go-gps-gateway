package expr

import (
	"math"
	"testing"
)

func evaluate(t *testing.T, source string, values map[string]any) any {
	t.Helper()
	result, err := Eval(source, Variables(values))
	if err != nil {
		t.Fatalf("eval %q: %v", source, err)
	}
	return result
}

func TestArithmeticPrecedence(t *testing.T) {
	cases := map[string]float64{
		"1 + 2 * 3":       7,
		"(1 + 2) * 3":     9,
		"10 / 4":          2.5,
		"10 % 3":          1,
		"-2 + 3":          1,
		"2 * -3":          -6,
		"1e2 + 1":         101,
		"abs(-3)":         3,
		"round(2.6)":      3,
		"min(3, 1, 2)":    1,
		"max(3, 1, 2)":    3,
		"pow(2, 5)":       32,
		"sqrt(9)":         3,
		"1 < 2 ? 10 : 20": 10,
	}
	for source, want := range cases {
		got := evaluate(t, source, nil)
		number, ok := got.(float64)
		if !ok || math.Abs(number-want) > 1e-9 {
			t.Errorf("%q = %v (%T), want %v", source, got, got, want)
		}
	}
}

func TestVariablesAndStrings(t *testing.T) {
	values := map[string]any{"fuel": 12.5, "ignition": true, "name": "van"}
	cases := []struct {
		source string
		want   any
	}{
		{"fuel * 2", 25.0},
		{"ignition", true},
		{"name + \"-1\"", "van-1"},
		{"fuel > 10 && ignition", true},
		{"fuel > 20 || ignition", true},
		{"!ignition", false},
		{"coalesce(fuel, name)", 12.5},
		{"length(name)", 3.0},
	}
	for _, test := range cases {
		got := evaluate(t, test.source, values)
		if got != test.want {
			t.Errorf("%q = %v (%T), want %v", test.source, got, got, test.want)
		}
	}
}

func TestUnknownVariableIsAnError(t *testing.T) {
	if _, err := Eval("fuel + 1", Variables(map[string]any{})); err == nil {
		t.Fatal("expected an error for an unknown variable")
	}
}

func TestMalformedExpressionIsAnError(t *testing.T) {
	for _, source := range []string{"1 +", "(1 + 2", "1 / 0", "\"open", "1 @ 2", "unknown(1)"} {
		if _, err := Eval(source, nil); err == nil {
			t.Errorf("%q: expected an error", source)
		}
	}
}

func TestComparisonOfStrings(t *testing.T) {
	got := evaluate(t, "name == \"van\"", map[string]any{"name": "van"})
	if got != true {
		t.Fatalf("equality = %v, want true", got)
	}
}
