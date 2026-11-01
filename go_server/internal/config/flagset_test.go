package config

import (
	"flag"
	"io"
)

// newFlagSet builds a flag set that returns an error instead of exiting, which
// is what main needs too.
func newFlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("traccar-gateway", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}
