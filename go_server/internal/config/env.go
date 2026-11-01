package config

import (
	"strings"
	"unicode"
)

// EnvName is the environment variable that carries key: the prefix, then the
// key with the dots turned into underscores and the camel humps split, so
// alert.overspeedKnots becomes TRACCAR_ALERT_OVERSPEED_KNOTS and
// api.tokenTTL becomes TRACCAR_API_TOKEN_TTL.
func EnvName(key string) string {
	return EnvPrefix + "_" + snakeUpper(key)
}

// snakeUpper folds a key into the upper case, underscore separated form an
// environment variable uses.
func snakeUpper(key string) string {
	var builder strings.Builder
	builder.Grow(len(key) + 4)

	previousLower := false
	runes := []rune(key)
	for index, r := range runes {
		switch {
		case r == '.' || r == '-':
			builder.WriteByte('_')
			previousLower = false
		case unicode.IsUpper(r):
			// Start a new word at the end of a lower case word and before the
			// last capital of an acronym: tokenTTL is TOKEN_TTL.
			nextLower := index+1 < len(runes) && unicode.IsLower(runes[index+1])
			if previousLower || (nextLower && index > 0 && unicode.IsUpper(runes[index-1])) {
				builder.WriteByte('_')
			}
			builder.WriteRune(r)
			previousLower = false
		default:
			builder.WriteRune(unicode.ToUpper(r))
			previousLower = unicode.IsLower(r) || unicode.IsDigit(r)
		}
	}
	return builder.String()
}

// loadEnv merges the environment into the Config. Only names that map onto a
// known key are read, so an unrelated TRACCAR_* variable cannot be mistaken for
// a setting.
func (c *Config) loadEnv(environ []string) {
	byName := make(map[string]string, len(environ))
	for _, entry := range environ {
		if name, value, ok := strings.Cut(entry, "="); ok {
			byName[name] = value
		}
	}

	// The legacy spellings come first so that the documented canonical name
	// wins when both are present.
	for name, key := range legacyEnvAliases {
		if value, ok := byName[name]; ok {
			c.env[key] = value
		}
	}

	for _, key := range KnownKeys {
		if value, ok := byName[EnvName(key)]; ok {
			c.env[key] = value
		}
	}

	for name, value := range byName {
		if key, ok := protocolKeyFromEnv(name); ok {
			c.env[key] = value
		}
	}
}

// protocolKeyFromEnv maps TRACCAR_PROTOCOL_<NAME>_<KEY> onto
// protocol.<name>.<key>. The protocol name comes from the variable itself, so a
// protocol the gateway does not ship yet can be configured ahead of time.
func protocolKeyFromEnv(envName string) (string, bool) {
	rest, ok := strings.CutPrefix(envName, EnvPrefix+"_PROTOCOL_")
	if !ok || rest == "" {
		return "", false
	}
	for _, key := range ProtocolKeys {
		suffix := "_" + snakeUpper(key)
		if len(rest) <= len(suffix) || !strings.HasSuffix(rest, suffix) {
			continue
		}
		name := strings.ToLower(rest[:len(rest)-len(suffix)])
		if name == "" {
			continue
		}
		return "protocol." + name + "." + key, true
	}
	return "", false
}
