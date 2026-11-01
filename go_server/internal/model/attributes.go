package model

import (
	"fmt"
	"strconv"
	"strings"
)

// Attributes is the JSON object every extended model carries: the device and
// position extras, the geofence limits, the maintenance schedule overrides.
// The accessors follow org.traccar.model.ExtendedModel, with one deliberate
// difference: a value that cannot be parsed returns the default instead of
// throwing, so one malformed attribute cannot reject a whole frame or row.
type Attributes map[string]any

// Has reports whether the key is present, whatever its value. It is what
// ExtendedModel.hasAttribute answers.
func (a Attributes) Has(key string) bool {
	_, ok := a[key]
	return ok
}

// Set stores a value. A nil value and an empty string are skipped, mirroring
// the set(String, Object) and set(String, String) overloads of the Java model.
// The receiver must be non-nil; models create the map with New or the storage
// layer fills it before a write.
func (a Attributes) Set(key string, value any) {
	if value == nil {
		return
	}
	if text, ok := value.(string); ok && text == "" {
		return
	}
	a[key] = value
}

// GetString returns the attribute rendered as text, or defaultValue when the
// key is missing.
func (a Attributes) GetString(key string, defaultValue string) string {
	value, ok := a[key]
	if !ok || value == nil {
		return defaultValue
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

// GetDouble returns a numeric attribute. JSON decoding turns every number into
// a float64, protocol decoders store ints, and configuration written by hand
// stores strings, so all three are accepted.
func (a Attributes) GetDouble(key string, defaultValue float64) float64 {
	value, ok := a[key]
	if !ok || value == nil {
		return defaultValue
	}
	number, ok := numberValue(value)
	if !ok {
		return defaultValue
	}
	return number
}

// GetInteger returns an integer attribute, truncating a fractional value the
// way Number.intValue does.
func (a Attributes) GetInteger(key string, defaultValue int) int {
	value, ok := a[key]
	if !ok || value == nil {
		return defaultValue
	}
	number, ok := integerValue(value)
	if !ok {
		return defaultValue
	}
	return int(number)
}

// GetLong is GetInteger with a 64 bit result, for ids and device times. Whole
// numbers keep their exact value; a float64 that already lost precision cannot
// be recovered.
func (a Attributes) GetLong(key string, defaultValue int64) int64 {
	value, ok := a[key]
	if !ok || value == nil {
		return defaultValue
	}
	number, ok := integerValue(value)
	if !ok {
		return defaultValue
	}
	return number
}

// GetBoolean returns a boolean attribute. Only the literal true (in any case,
// like Boolean.parseBoolean) turns a string into true; a missing key and every
// other value answer false.
func (a Attributes) GetBoolean(key string) bool {
	value, ok := a[key]
	if !ok || value == nil {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}

// Remove deletes an attribute and returns its previous value, nil when the key
// was absent.
func (a Attributes) Remove(key string) any {
	value, ok := a[key]
	if !ok {
		return nil
	}
	delete(a, key)
	return value
}

// integerValue converts the numeric shapes an attribute can hold to int64
// without sending whole numbers through float64 first.
func integerValue(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int8:
		return int64(typed), true
	case int16:
		return int64(typed), true
	case int32:
		return int64(typed), true
	case int64:
		return typed, true
	case uint:
		return int64(typed), true
	case uint8:
		return int64(typed), true
	case uint16:
		return int64(typed), true
	case uint32:
		return int64(typed), true
	case uint64:
		return int64(typed), true
	case float32:
		return int64(typed), true
	case float64:
		return int64(typed), true
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

// numberValue converts the numeric shapes an attribute can hold to float64.
// Booleans are not numbers: the Java model would fail to parse one too.
func numberValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int8:
		return float64(typed), true
	case int16:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint8:
		return float64(typed), true
	case uint16:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case float32:
		return float64(typed), true
	case float64:
		return typed, true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}
