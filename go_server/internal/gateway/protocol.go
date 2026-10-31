// Package gateway hosts the network layer: it accepts device connections,
// splits the byte stream into frames, hands them to the protocol decoders and
// writes the protocol responses back. All protocol specific logic lives in
// internal/protocol, so this package stays free of byte layouts.
package gateway

import (
	"fmt"
	"sort"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/protocol/gt06"
	"traccar-server_go/go_server/internal/protocol/h02"
	"traccar-server_go/go_server/internal/protocol/jt808"
	"traccar-server_go/go_server/internal/protocol/teltonika"
)

// Protocol bundles the constructors of one protocol. Constructors are used
// instead of instances because frame decoders carry per connection state
// (negotiated lengths, partial frames).
type Protocol struct {
	Name            string
	NewFrameDecoder func() codec.FrameDecoder
	NewDecoder      func() codec.Decoder
	NewEncoder      func() codec.Encoder
}

// Factory creates the protocol handlers bound to the shared dependencies.
type Factory func(base codec.Base) *Protocol

// Registry holds the available protocol factories.
type Registry struct {
	factories map[string]Factory
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{factories: make(map[string]Factory)}
}

// Add registers a factory under a protocol name.
func (r *Registry) Add(name string, factory Factory) {
	r.factories[name] = factory
}

// Get returns the factory for a protocol name.
func (r *Registry) Get(name string) (Factory, error) {
	factory, ok := r.factories[name]
	if !ok {
		return nil, fmt.Errorf("gateway: unknown protocol %q", name)
	}
	return factory, nil
}

// Names lists the registered protocol names in alphabetical order.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.factories))
	for name := range r.factories {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// DefaultRegistry registers the four implemented protocols.
func DefaultRegistry() *Registry {
	registry := NewRegistry()

	registry.Add(h02.ProtocolName, func(base codec.Base) *Protocol {
		return &Protocol{
			Name:            h02.ProtocolName,
			NewFrameDecoder: func() codec.FrameDecoder { return h02.NewFrameDecoder(0) },
			NewDecoder:      func() codec.Decoder { return h02.NewDecoder(base) },
			NewEncoder:      func() codec.Encoder { return h02.NewEncoder(base) },
		}
	})

	registry.Add(gt06.ProtocolName, func(base codec.Base) *Protocol {
		return &Protocol{
			Name:            gt06.ProtocolName,
			NewFrameDecoder: func() codec.FrameDecoder { return gt06.NewFrameDecoder() },
			NewDecoder:      func() codec.Decoder { return gt06.NewDecoder(base) },
			NewEncoder:      func() codec.Encoder { return gt06.NewEncoder(base) },
		}
	})

	registry.Add(jt808.ProtocolName, func(base codec.Base) *Protocol {
		return &Protocol{
			Name:            jt808.ProtocolName,
			NewFrameDecoder: func() codec.FrameDecoder { return jt808.NewFrameDecoder() },
			NewDecoder:      func() codec.Decoder { return jt808.NewDecoder(base) },
			NewEncoder:      func() codec.Encoder { return jt808.NewEncoder(base) },
		}
	})

	registry.Add(teltonika.ProtocolName, func(base codec.Base) *Protocol {
		return &Protocol{
			Name:            teltonika.ProtocolName,
			NewFrameDecoder: func() codec.FrameDecoder { return teltonika.NewFrameDecoder() },
			NewDecoder:      func() codec.Decoder { return teltonika.NewDecoder(base) },
			NewEncoder:      func() codec.Encoder { return teltonika.NewEncoder(base) },
		}
	})

	return registry
}
