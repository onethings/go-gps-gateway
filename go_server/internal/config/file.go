package config

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// maxIncludeDepth bounds the config.default chain so a file that includes
// itself fails with a clear message instead of recursing forever.
const maxIncludeDepth = 8

// propertiesFile is the Java properties XML dialect the Java server reads:
// <properties><entry key="a.b">value</entry></properties>.
type propertiesFile struct {
	XMLName xml.Name         `xml:"properties"`
	Entries []propertiesItem `xml:"entry"`
}

type propertiesItem struct {
	Key   string `xml:"key,attr"`
	Value string `xml:",chardata"`
}

// loadFile merges one configuration file into the Config. A missing file is an
// error unless required is false. Values of the file win over the values of the
// file it names through config.default, so the include is loaded first.
func (c *Config) loadFile(path string, required bool, depth int) error {
	if depth > maxIncludeDepth {
		return fmt.Errorf("%s: config.default chain is deeper than %d files", path, maxIncludeDepth)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && !required {
			return nil
		}
		return fmt.Errorf("config: read %s: %w", path, err)
	}

	var parsed propertiesFile
	if err := xml.Unmarshal(content, &parsed); err != nil {
		return fmt.Errorf("config: parse %s: %w", path, err)
	}

	values := make(map[string]string, len(parsed.Entries))
	for _, entry := range parsed.Entries {
		key := strings.TrimSpace(entry.Key)
		if key == "" {
			continue
		}
		values[key] = strings.TrimSpace(entry.Value)
	}

	if included := values[ConfigDefault]; included != "" {
		delete(values, ConfigDefault)
		// A relative path is relative to the file that names it, so an
		// installation directory keeps working from any working directory.
		if !filepath.IsAbs(included) {
			included = filepath.Join(filepath.Dir(path), included)
		}
		if err := c.loadFile(included, true, depth+1); err != nil {
			return err
		}
	}

	for key, value := range values {
		c.file[key] = value
	}
	c.files = append(c.files, path)
	return nil
}
