// Package devicescene holds recorded device conversations. A scene carries the
// frames a replay needs plus the facts a test can check without a server.
package devicescene

import (
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

//go:embed scenes/*.json
var bundled embed.FS

// Scene is one recorded conversation.
type Scene struct {
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	// UniqueID is the identity the gateway must already know, or the one the
	// handshake creates when Handshake is true.
	UniqueID string `json:"uniqueId"`
	// Handshake reports whether the scene opens with a frame that carries the
	// identifier. H02 has none, it binds from the first position.
	Handshake bool `json:"handshake"`
	// Positions is the number of positions the frames must produce.
	Positions int    `json:"positions"`
	Steps     []Step `json:"steps"`
}

// Step is one frame to write, named so a log line says what failed.
type Step struct {
	Name string `json:"name"`
	Hex  string `json:"hex"`
}

// Load reads and validates one scene file.
func Load(file string) (*Scene, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	return Parse(raw, file)
}

// Parse decodes scene JSON. The label only names the source in errors.
func Parse(raw []byte, label string) (*Scene, error) {
	var scene Scene
	if err := json.Unmarshal(raw, &scene); err != nil {
		return nil, fmt.Errorf("scene %s: %w", label, err)
	}
	if len(scene.Steps) == 0 {
		return nil, fmt.Errorf("scene %s carries no steps", label)
	}
	if scene.Name == "" {
		scene.Name = label
	}
	if scene.Protocol == "" {
		return nil, fmt.Errorf("scene %s names no protocol", label)
	}
	return &scene, nil
}

// All returns every bundled scene in name order.
func All() ([]Scene, error) {
	entries, err := bundled.ReadDir("scenes")
	if err != nil {
		return nil, err
	}

	scenes := make([]Scene, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		raw, err := bundled.ReadFile("scenes/" + entry.Name())
		if err != nil {
			return nil, err
		}
		scene, err := Parse(raw, entry.Name())
		if err != nil {
			return nil, err
		}
		scenes = append(scenes, *scene)
	}

	sort.Slice(scenes, func(i, j int) bool { return scenes[i].Name < scenes[j].Name })
	return scenes, nil
}

// Frames decodes every step payload in order.
func (s *Scene) Frames() ([][]byte, error) {
	frames := make([][]byte, 0, len(s.Steps))
	for i, step := range s.Steps {
		payload, err := hex.DecodeString(step.Hex)
		if err != nil {
			return nil, fmt.Errorf("step %d (%s): %w", i+1, step.Name, err)
		}
		frames = append(frames, payload)
	}
	return frames, nil
}
