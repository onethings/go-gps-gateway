package helper

import (
	"strconv"
	"strings"
	"time"
)

// ParseStampDMYHMS reads the twelve digit ddmmyyhhmmss stamp the Coban and Xexun
// families send. The year is expanded into the 2000s.
func ParseStampDMYHMS(text string) (time.Time, bool) {
	values, ok := stampDigits(text, 6)
	if !ok {
		return time.Time{}, false
	}
	return NewDateBuilder().
		SetDate(values[2]+2000, values[1], values[0]).
		SetTime(values[3], values[4], values[5]).
		Date(), true
}

// ParseStampYMDHMS reads the fourteen digit yyyymmddhhmmss stamp the Meitrack and
// Wondex families send.
func ParseStampYMDHMS(text string) (time.Time, bool) {
	values, ok := stampDigits(text, 7)
	if !ok {
		return time.Time{}, false
	}
	return NewDateBuilder().
		SetDate(values[0]*100+values[1], values[2], values[3]).
		SetTime(values[4], values[5], values[6]).
		Date(), true
}

// stampDigits splits a fixed width stamp into decimal pairs.
func stampDigits(text string, pairs int) ([]int, bool) {
	trimmed := strings.TrimSpace(text)
	if len(trimmed) != pairs*2 {
		return nil, false
	}
	values := make([]int, pairs)
	for index := 0; index < pairs; index++ {
		value, err := strconv.Atoi(trimmed[index*2 : index*2+2])
		if err != nil {
			return nil, false
		}
		values[index] = value
	}
	return values, true
}

// ParseNumber reads a decimal field, returning zero when the device sent
// something that is not a number. The text protocols all treat an empty or
// malformed field as "no value" instead of failing the whole frame.
func ParseNumber(text string) float64 {
	value, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil {
		return 0
	}
	return value
}
