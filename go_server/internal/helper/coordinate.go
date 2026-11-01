package helper

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseCoordinate turns a textual coordinate and its hemisphere into decimal
// degrees. The text protocol decoders share it because they all split their
// frames on a delimiter first and only the string to number conversion is the
// same.
//
// A negative value is kept as it is; the hemisphere only decides the sign when
// the text does not already carry one.
func ParseCoordinate(value, hemisphere string) (float64, error) {
	coordinate, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return 0, fmt.Errorf("coordinate %q: %w", value, err)
	}
	switch strings.ToUpper(strings.TrimSpace(hemisphere)) {
	case "S", "W", "-":
		return -absFloat(coordinate), nil
	default:
		return absFloat(coordinate), nil
	}
}

// ParseDegMinHem parses the "ddmm.mmmm" form the Xexun style trackers use.
func ParseDegMinHem(value, hemisphere string) (float64, error) {
	text := strings.TrimSpace(value)
	dot := strings.IndexByte(text, '.')
	if dot < 0 {
		return 0, fmt.Errorf("coordinate %q has no minutes", value)
	}
	// Minutes are the two digits before the decimal point, degrees everything
	// before them.
	if dot < 3 {
		return 0, fmt.Errorf("coordinate %q is too short", value)
	}
	degrees, err := strconv.ParseFloat(text[:dot-2], 64)
	if err != nil {
		return 0, fmt.Errorf("coordinate %q: %w", value, err)
	}
	minutes, err := strconv.ParseFloat(text[dot-2:], 64)
	if err != nil {
		return 0, fmt.Errorf("coordinate %q: %w", value, err)
	}
	coordinate := degrees + minutes/60
	switch strings.ToUpper(strings.TrimSpace(hemisphere)) {
	case "S", "W", "-":
		return -absFloat(coordinate), nil
	default:
		return absFloat(coordinate), nil
	}
}
