// Package config resolves the gateway configuration from three layers, lowest
// priority first: the compiled-in defaults, a traccar.xml file and the
// environment. Command line flags sit above them: each flag is registered with
// the resolved value as its default, so a flag the operator does not pass keeps
// what the lower layers say.
//
// Keys are named after the Java server's config/Keys.java wherever it has an
// equivalent, and a protocol section can override a global value under
// protocol.<name>.<key> (or <name>.<key>, the spelling default.xml uses).
package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Source names the layer a value came from.
type Source string

const (
	// SourceDefault is the compiled-in value.
	SourceDefault Source = "default"
	// SourceFile is a traccar.xml entry.
	SourceFile Source = "file"
	// SourceEnv is an environment variable.
	SourceEnv Source = "environment"
)

// EnvPrefix is the prefix of every environment variable the loader reads.
const EnvPrefix = "TRACCAR"

// DefaultPath is the file Load reads when no path is given and TRACCAR_CONFIG
// is unset.
const DefaultPath = "traccar.xml"

// Config is the merged view of the defaults, the configuration files and the
// environment.
type Config struct {
	file map[string]string
	env  map[string]string

	// files are the paths that contributed values, in load order, so the
	// startup log can name them.
	files []string
}

// Default returns the compiled-in configuration: no file, no environment.
func Default() *Config {
	return &Config{file: map[string]string{}, env: map[string]string{}}
}

// Load merges the defaults, the file at path and environ, in that order.
//
// An empty path means "the file named by TRACCAR_CONFIG, or ./traccar.xml when
// that exists". A path that comes from the argument or from TRACCAR_CONFIG must
// exist: a typo in a mount path should not start the gateway with defaults.
func Load(path string, environ []string) (*Config, error) {
	resolved, required := ResolvePath(path, environ)

	cfg := Default()
	if resolved != "" {
		if err := cfg.loadFile(resolved, required, 0); err != nil {
			return nil, err
		}
	}
	cfg.loadEnv(environ)
	return cfg, nil
}

// ResolvePath decides which file to read and whether its absence is an error.
// An empty path means TRACCAR_CONFIG, then the default path, which is optional:
// running without a configuration file falls back to the compiled-in defaults.
func ResolvePath(path string, environ []string) (resolved string, required bool) {
	if path == "" {
		for _, entry := range environ {
			name, value, _ := strings.Cut(entry, "=")
			if name == EnvPrefix+"_CONFIG" && value != "" {
				return value, true
			}
		}
		return DefaultPath, false
	}
	return path, true
}

// Root is the last file that was read, the one the operator named, or the
// default path when there was none. It is what -help shows.
func (c *Config) Root() string {
	if len(c.files) == 0 {
		return DefaultPath
	}
	return c.files[len(c.files)-1]
}

// Files lists the configuration files that were read, lowest priority first.
func (c *Config) Files() []string {
	return append([]string(nil), c.files...)
}

// Summary describes where the values came from, for the startup log.
func (c *Config) Summary() string {
	switch {
	case len(c.files) == 0 && len(c.env) == 0:
		return "defaults only"
	case len(c.files) == 0:
		return fmt.Sprintf("%d environment values", len(c.env))
	case len(c.env) == 0:
		return strings.Join(c.files, " + ")
	default:
		return fmt.Sprintf("%s + %d environment values", strings.Join(c.files, " + "), len(c.env))
	}
}

// lookup returns the highest priority value of key together with its layer.
func (c *Config) lookup(key string) (string, Source, bool) {
	if value, ok := c.env[key]; ok {
		return value, SourceEnv, true
	}
	if value, ok := c.file[key]; ok {
		return value, SourceFile, true
	}
	if value, ok := defaults[key]; ok {
		return value, SourceDefault, true
	}
	return "", "", false
}

// Has reports whether key is set by any layer.
func (c *Config) Has(key string) bool {
	_, _, ok := c.lookup(key)
	return ok
}

// Source reports which layer provided key.
func (c *Config) Source(key string) Source {
	_, source, _ := c.lookup(key)
	return source
}

// String returns the value of key, or "" when no layer sets it.
func (c *Config) String(key string) string {
	value, _, _ := c.lookup(key)
	return value
}

// StringOr returns the value of key, or fallback when no layer sets it.
func (c *Config) StringOr(key, fallback string) string {
	if value, _, ok := c.lookup(key); ok {
		return value
	}
	return fallback
}

// List returns the comma separated value of key with the items trimmed and the
// empty ones dropped.
func (c *Config) List(key string) []string {
	return SplitList(c.String(key))
}

// SplitList splits a comma separated value the way the flags did.
func SplitList(value string) []string {
	items := make([]string, 0, 4)
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}

// Int parses the value of key as an integer.
func (c *Config) Int(key string) (int, error) {
	text, _, ok := c.lookup(key)
	if !ok || text == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not an integer", key, text)
	}
	return value, nil
}

// Float parses the value of key as a floating point number.
func (c *Config) Float(key string) (float64, error) {
	text, _, ok := c.lookup(key)
	if !ok || text == "" {
		return 0, nil
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a number", key, text)
	}
	return value, nil
}

// Bool parses the value of key as a boolean.
func (c *Config) Bool(key string) (bool, error) {
	text, _, ok := c.lookup(key)
	if !ok || text == "" {
		return false, nil
	}
	value, err := parseBool(text)
	if err != nil {
		return false, fmt.Errorf("%s: %w", key, err)
	}
	return value, nil
}

// Duration parses the value of key. A bare number is read as seconds, which is
// how the Java server's timeout keys are written; an explicit unit such as 30s
// or 5m works too.
func (c *Config) Duration(key string) (time.Duration, error) {
	text, _, ok := c.lookup(key)
	if !ok || text == "" {
		return 0, nil
	}
	value, err := ParseDuration(text)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return value, nil
}

// ParseDuration reads a duration with an optional unit. A bare number is a
// count of seconds.
func ParseDuration(text string) (time.Duration, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, nil
	}
	if value, err := time.ParseDuration(text); err == nil {
		return value, nil
	}
	seconds, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is neither a duration nor a number of seconds", text)
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

// parseBool accepts the spellings a hand written configuration file uses.
func parseBool(text string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "true", "yes", "on", "1":
		return true, nil
	case "false", "no", "off", "0":
		return false, nil
	}
	if value, err := strconv.ParseBool(strings.TrimSpace(text)); err == nil {
		return value, nil
	}
	return false, fmt.Errorf("%q is not a boolean", text)
}

// Protocol returns the per-protocol view of the configuration.
func (c *Config) Protocol(name string) Protocol {
	return Protocol{cfg: c, Name: name}
}

// Protocol is one protocol section of the configuration. A key resolves through
// protocol.<name>.<key>, then <name>.<key> (the spelling the Java server's
// default.xml uses), then protocol.<key>, then <key>. The first key that any
// layer sets wins, so a protocol section overrides the global value whatever
// layer the global value came from.
type Protocol struct {
	cfg  *Config
	Name string
}

// candidates lists the lookups for one key, most specific first.
func (p Protocol) candidates(key string) []string {
	return []string{
		"protocol." + p.Name + "." + key,
		p.Name + "." + key,
		"protocol." + key,
		key,
	}
}

// Lookup returns the most specific value of key and the key it came from.
func (p Protocol) Lookup(key string) (string, Source, string, bool) {
	for _, candidate := range p.candidates(key) {
		if value, source, ok := p.cfg.lookup(candidate); ok {
			return value, source, candidate, true
		}
	}
	return "", "", "", false
}

// String returns the most specific value of key, or "" when no layer sets it.
func (p Protocol) String(key string) string {
	value, _, _, _ := p.Lookup(key)
	return value
}

// Address returns the listen address of the protocol server and the key it came
// from. An address beats a port; a port alone becomes 0.0.0.0:<port>. An empty
// result with a non-empty key means the section disabled the server.
func (p Protocol) Address() (string, string) {
	if value, _, key, ok := p.Lookup(ProtocolAddress); ok {
		return value, key
	}
	if value, _, key, ok := p.Lookup(ProtocolPort); ok {
		if value == "" {
			return "", key
		}
		return "0.0.0.0:" + strings.TrimSpace(value), key
	}
	if fallback, ok := defaultProtocolAddresses[p.Name]; ok {
		return fallback, ""
	}
	return "", ""
}

// Duration returns the most specific value of key as a duration.
func (p Protocol) Duration(key string) (time.Duration, error) {
	value, _, _, ok := p.Lookup(key)
	if !ok || value == "" {
		return 0, nil
	}
	return ParseDuration(value)
}

// Bool returns the most specific value of key as a boolean. The second result
// reports whether the key was set at all.
func (p Protocol) Bool(key string) (value, set bool, err error) {
	text, _, _, ok := p.Lookup(key)
	if !ok || text == "" {
		return false, false, nil
	}
	parsed, err := parseBool(text)
	if err != nil {
		return false, true, fmt.Errorf("%s.%s: %w", p.Name, key, err)
	}
	return parsed, true, nil
}

// PathFromArgs reads -config or -c out of the raw arguments. The file has to be
// read before the flag set exists, because every other flag defaults to a value
// the file may change.
//
// Because it runs before the flags know which of them take a value, a literal
// -config used as another flag's value is read as the flag. Nothing sane passes
// that, and flag.Parse still rejects the resulting file name.
func PathFromArgs(args []string) (string, bool) {
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			return "", false
		}
		if !strings.HasPrefix(arg, "-") {
			continue
		}

		name := strings.TrimLeft(arg, "-")
		inline, hasInline := "", false
		if before, after, ok := strings.Cut(name, "="); ok {
			name, inline, hasInline = before, after, true
		}
		if name != "config" && name != "c" {
			continue
		}
		if hasInline {
			return inline, true
		}
		if index+1 < len(args) {
			return args[index+1], true
		}
		return "", true
	}
	return "", false
}
