package main

import (
	"traccar-server_go/go_server/internal/config"
)

// mediaSettings is where the uploaded files are stored.
type mediaSettings struct {
	Enabled   bool
	Directory string
}

// readMediaSettings reads media.path. An empty path disables the media routes,
// which is what a deployment without a writable volume wants.
func readMediaSettings(cfg *config.Config) mediaSettings {
	if cfg == nil {
		return mediaSettings{}
	}
	directory := cfg.String(config.MediaPath)
	if directory == "" {
		return mediaSettings{}
	}
	return mediaSettings{Enabled: true, Directory: directory}
}
