package notify

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"traccar-server_go/go_server/internal/model"
)

// Alert attribute keys the formatter reads back from the engine snapshot.
const (
	attrLatitude  = "latitude"
	attrLongitude = "longitude"
	attrAltitude  = "altitude"
	attrSpeed     = "speed"
	attrLimit     = "limit"
	attrPower     = "power"
	attrThreshold = "threshold"
	attrGeofence  = "geofence"
)

// Format renders an alert into the subject and body every channel sends. Both
// channels share the rendering so mail and webhook consumers see the same
// facts.
func Format(alert *model.Alert, device *model.Device) Message {
	message := Message{Alert: alert, Device: device}
	if alert == nil {
		return message
	}

	label := deviceLabel(device)
	message.Subject = "[traccar] " + alert.Type
	if label != "" {
		message.Subject += ": " + label
	}

	var body strings.Builder
	if label != "" {
		fmt.Fprintf(&body, "Device: %s\n", label)
	} else if alert.DeviceID != 0 {
		fmt.Fprintf(&body, "Device: %d\n", alert.DeviceID)
	}
	fmt.Fprintf(&body, "Type: %s\n", alert.Type)
	if !alert.Time.IsZero() {
		fmt.Fprintf(&body, "Time: %s\n", alert.Time.UTC().Format(time.RFC3339))
	}
	if summary := Summary(alert); summary != "" {
		fmt.Fprintf(&body, "Event: %s\n", summary)
	}
	if location := locationLine(alert.Attributes); location != "" {
		fmt.Fprintf(&body, "Location: %s\n", location)
	}
	if attributes := attributeLines(alert.Attributes); attributes != "" {
		body.WriteString("Attributes:\n")
		body.WriteString(attributes)
	}

	message.Body = strings.TrimRight(body.String(), "\n")
	return message
}

// Summary describes the rule verdict in one line. It returns an empty string
// when the alert does not carry the attributes the rule normally sets.
func Summary(alert *model.Alert) string {
	if alert == nil {
		return ""
	}

	switch alert.Type {
	case model.AlertOverspeed:
		speed, okSpeed := number(alert.Attributes[attrSpeed])
		limit, okLimit := number(alert.Attributes[attrLimit])
		if !okSpeed || !okLimit {
			return ""
		}
		return fmt.Sprintf("Speed %s kn above limit %s kn", formatNumber(speed), formatNumber(limit))
	case model.AlertPowerCut:
		power, okPower := number(alert.Attributes[attrPower])
		threshold, okThreshold := number(alert.Attributes[attrThreshold])
		if !okPower || !okThreshold {
			return ""
		}
		return fmt.Sprintf("Power %s V below threshold %s V", formatNumber(power), formatNumber(threshold))
	case model.AlertPowerRestored:
		power, ok := number(alert.Attributes[attrPower])
		if !ok {
			return ""
		}
		return fmt.Sprintf("Power restored at %s V", formatNumber(power))
	case model.AlertGeofenceEnter, model.AlertGeofenceExit:
		name, _ := alert.Attributes[attrGeofence].(string)
		if name == "" {
			return ""
		}
		if alert.Type == model.AlertGeofenceEnter {
			return "Entered geofence " + name
		}
		return "Exited geofence " + name
	default:
		return ""
	}
}

func deviceLabel(device *model.Device) string {
	if device == nil {
		return ""
	}
	switch {
	case device.Name != "" && device.UniqueID != "":
		return device.Name + " (" + device.UniqueID + ")"
	case device.Name != "":
		return device.Name
	default:
		return device.UniqueID
	}
}

func locationLine(attributes map[string]any) string {
	latitude, okLatitude := number(attributes[attrLatitude])
	longitude, okLongitude := number(attributes[attrLongitude])
	if !okLatitude || !okLongitude {
		return ""
	}
	line := fmt.Sprintf("%.6f, %.6f", latitude, longitude)
	if altitude, ok := number(attributes[attrAltitude]); ok {
		line += fmt.Sprintf(" (altitude %s m)", formatNumber(altitude))
	}
	return line
}

// attributeLines renders the raw attributes sorted by key so that a message is
// reproducible and diffs stay readable.
func attributeLines(attributes map[string]any) string {
	if len(attributes) == 0 {
		return ""
	}

	keys := make([]string, 0, len(attributes))
	for key := range attributes {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var builder strings.Builder
	for _, key := range keys {
		fmt.Fprintf(&builder, "  %s: %s\n", key, formatValue(attributes[key]))
	}
	return builder.String()
}

func formatValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case float64:
		return formatNumber(typed)
	case float32:
		return formatNumber(float64(typed))
	case int:
		return strconv.Itoa(typed)
	case int32:
		return strconv.FormatInt(int64(typed), 10)
	case int64:
		return strconv.FormatInt(typed, 10)
	case json.Number:
		return typed.String()
	case time.Time:
		return typed.UTC().Format(time.RFC3339)
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprint(typed)
		}
		return string(encoded)
	}
}

func formatNumber(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

// number converts the numeric attribute types the decoders and JSONB produce.
func number(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}
