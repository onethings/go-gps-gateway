package notify

import (
	"fmt"
	"reflect"
	"strings"

	"traccar-server_go/go_server/internal/model"
)

// TextTemplateFormatter renders the Velocity subset the Java server's
// notification templates use: $name and ${name} with dotted paths, plus
// #if($name) ... #else ... #end. Anything else is copied verbatim, which keeps
// a template that uses a construct this subset does not support readable
// instead of failing the notification.
type TextTemplateFormatter struct{}

// Format renders a template with the event, device and position in scope, the
// names the Java formatter exposes.
func (TextTemplateFormatter) Format(template string, event *model.Event, device *model.Device, position *model.Position) string {
	values := map[string]any{}
	if event != nil {
		values["event"] = event
	}
	if device != nil {
		values["device"] = device
	}
	if position != nil {
		values["position"] = position
	}
	return RenderTemplate(template, values)
}

// RenderTemplate renders template against values. Exported so a caller can
// build its own context without knowing the internals of the formatter.
func RenderTemplate(template string, values map[string]any) string {
	renderer := templateRenderer{values: values}
	return renderer.render(template)
}

type templateRenderer struct {
	values map[string]any
}

func (r templateRenderer) render(template string) string {
	var builder strings.Builder
	for index := 0; index < len(template); {
		switch {
		case strings.HasPrefix(template[index:], "##"):
			// A comment runs to the end of the line.
			if end := strings.IndexByte(template[index:], '\n'); end >= 0 {
				builder.WriteString(template[index+end:])
				index += end + 1
			} else {
				index = len(template)
			}
		case strings.HasPrefix(template[index:], "#if("):
			end := matchingEnd(template, index)
			if end < 0 {
				builder.WriteString(template[index:])
				index = len(template)
				continue
			}
			condition := template[index+len("#if(") : strings.IndexByte(template[index:], ')')+index]
			body := template[index+len("#if(")+len(condition)+1 : end]
			builder.WriteString(r.renderBranch(body, truthy(lookupPath(r.values, conditionPath(condition)))))
			index = end + len("#end")
		case strings.HasPrefix(template[index:], "$$"):
			builder.WriteByte('$')
			index += 2
		case strings.HasPrefix(template[index:], "${"):
			close := strings.IndexByte(template[index:], '}')
			if close < 0 {
				builder.WriteString(template[index:])
				index = len(template)
				continue
			}
			path := template[index+2 : index+close]
			builder.WriteString(templateValue(lookupPath(r.values, path)))
			index += close + 1
		case template[index] == '$':
			path, length := readPath(template[index+1:])
			if length == 0 {
				builder.WriteByte('$')
				index++
				continue
			}
			builder.WriteString(templateValue(lookupPath(r.values, path)))
			index += 1 + length
		default:
			builder.WriteByte(template[index])
			index++
		}
	}
	return builder.String()
}

// renderBranch renders the if or the else half of a block.
func (r templateRenderer) renderBranch(body string, condition bool) string {
	if elseIndex := topLevelElse(body); elseIndex >= 0 {
		if condition {
			return r.render(body[:elseIndex])
		}
		return r.render(body[elseIndex+len("#else"):])
	}
	if condition {
		return r.render(body)
	}
	return ""
}

// matchingEnd finds the #end that closes the #if starting at start.
func matchingEnd(template string, start int) int {
	depth := 0
	for index := start; index < len(template); {
		switch {
		case strings.HasPrefix(template[index:], "#if("):
			depth++
			index += len("#if(")
		case strings.HasPrefix(template[index:], "#end"):
			depth--
			if depth == 0 {
				return index
			}
			index += len("#end")
		default:
			index++
		}
	}
	return -1
}

// topLevelElse finds the #else that belongs to the block in body, skipping one
// nested #if.
func topLevelElse(body string) int {
	depth := 0
	for index := 0; index < len(body); {
		switch {
		case strings.HasPrefix(body[index:], "#if("):
			depth++
			index += len("#if(")
		case strings.HasPrefix(body[index:], "#end"):
			depth--
			index += len("#end")
		case strings.HasPrefix(body[index:], "#else") && depth == 0:
			return index
		default:
			index++
		}
	}
	return -1
}

// conditionPath strips the Velocity reference syntax from an #if condition.
func conditionPath(condition string) string {
	trimmed := strings.TrimSpace(condition)
	trimmed = strings.TrimPrefix(trimmed, "$")
	trimmed = strings.TrimPrefix(trimmed, "{")
	trimmed = strings.TrimSuffix(trimmed, "}")
	return trimmed
}

// readPath reads the dotted identifier after a dollar sign.
func readPath(value string) (string, int) {
	length := 0
	for length < len(value) {
		c := value[length]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '_' || c == '.' {
			length++
			continue
		}
		break
	}
	return value[:length], length
}

// lookupPath walks a dotted path through maps and structs. Map keys are matched
// exactly first and then case insensitively; struct fields are matched by name
// ignoring case, so "event.type" reaches model.Event.Type.
func lookupPath(values map[string]any, path string) any {
	var current any = values
	for _, part := range strings.Split(strings.TrimSpace(path), ".") {
		if part == "" {
			return nil
		}
		current = lookupOne(current, part)
		if current == nil {
			return nil
		}
	}
	return current
}

func lookupOne(value any, key string) any {
	if value == nil {
		return nil
	}
	switch typed := value.(type) {
	case map[string]any:
		if found, ok := typed[key]; ok {
			return found
		}
		for candidate, found := range typed {
			if strings.EqualFold(candidate, key) {
				return found
			}
		}
		return nil
	case model.Attributes:
		return lookupOne(map[string]any(typed), key)
	case map[string]string:
		converted := make(map[string]any, len(typed))
		for candidate, found := range typed {
			converted[candidate] = found
		}
		return lookupOne(converted, key)
	}

	reflected := reflect.ValueOf(value)
	for reflected.Kind() == reflect.Pointer || reflected.Kind() == reflect.Interface {
		if reflected.IsNil() {
			return nil
		}
		reflected = reflected.Elem()
	}
	if reflected.Kind() != reflect.Struct {
		return nil
	}
	field := reflected.FieldByNameFunc(func(name string) bool {
		return strings.EqualFold(name, key)
	})
	if !field.IsValid() {
		return nil
	}
	return field.Interface()
}

// truthy decides an #if condition the way Velocity does: nil, false, an empty
// string and a zero number are false.
func truthy(value any) bool {
	if value == nil {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return typed != ""
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return reflected.Int() != 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return reflected.Uint() != 0
	case reflect.Float32, reflect.Float64:
		return reflected.Float() != 0
	}
	return true
}

// formatValue renders one value; nil becomes the empty string, which is what a
// template with an absent field should look like.
func templateValue(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}
