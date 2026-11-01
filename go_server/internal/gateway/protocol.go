// Package gateway hosts the network layer: it accepts device connections,
// splits the byte stream into frames, hands them to the protocol decoders and
// writes the protocol responses back. All protocol specific logic lives in
// internal/protocol, so this package stays free of byte layouts.
package gateway

import (
	"fmt"
	"sort"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/protocol/gt06"
	"traccar-server_go/go_server/internal/protocol/h02"
	"traccar-server_go/go_server/internal/protocol/jt808"
	"traccar-server_go/go_server/internal/protocol/mqtt"
	"traccar-server_go/go_server/internal/protocol/osmand"
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
	// NewHTTP is set by the protocols a device reaches over HTTP. Those have no
	// frame decoder and no encoder, because the request is the frame.
	NewHTTP func(base codec.Base) codec.HTTPDecoder
	// TextCommands and DataCommands list the command types the encoder can
	// build. The text channel is the one a device reads as a plain string
	// (SMS-like); the data channel is the protocol's own binary framing.
	TextCommands []string
	DataCommands []string
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

// CommandTypes reports the command types a protocol's encoder accepts on the
// chosen channel. ok is false for a protocol that has no server.
func (r *Registry) CommandTypes(protocol string, textChannel bool) ([]string, bool) {
	factory, ok := r.factories[protocol]
	if !ok {
		return nil, false
	}
	built := factory(codec.Base{})
	if built == nil {
		return nil, true
	}
	if textChannel {
		return append([]string(nil), built.TextCommands...), true
	}
	return append([]string(nil), built.DataCommands...), true
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

// DefaultRegistry registers the implemented protocols.
func DefaultRegistry() *Registry {
	registry := NewRegistry()

	registry.Add(h02.ProtocolName, func(base codec.Base) *Protocol {
		return &Protocol{
			Name:            h02.ProtocolName,
			NewFrameDecoder: func() codec.FrameDecoder { return h02.NewFrameDecoder(0) },
			NewDecoder:      func() codec.Decoder { return h02.NewDecoder(base) },
			NewEncoder:      func() codec.Encoder { return h02.NewEncoder(base) },
			TextCommands: []string{
				model.CommandAlarmArm, model.CommandAlarmDisarm,
				model.CommandEngineStop, model.CommandEngineResume,
				model.CommandPositionPeriodic,
			},
		}
	})

	registry.Add(gt06.ProtocolName, func(base codec.Base) *Protocol {
		return &Protocol{
			Name:            gt06.ProtocolName,
			NewFrameDecoder: func() codec.FrameDecoder { return gt06.NewFrameDecoder() },
			NewDecoder:      func() codec.Decoder { return gt06.NewDecoder(base) },
			NewEncoder:      func() codec.Encoder { return gt06.NewEncoder(base) },
			TextCommands: []string{
				model.CommandEngineStop, model.CommandEngineResume, model.CommandCustom,
			},
		}
	})

	registry.Add(jt808.ProtocolName, func(base codec.Base) *Protocol {
		return &Protocol{
			Name:            jt808.ProtocolName,
			NewFrameDecoder: func() codec.FrameDecoder { return jt808.NewFrameDecoder() },
			NewDecoder:      func() codec.Decoder { return jt808.NewDecoder(base) },
			NewEncoder:      func() codec.Encoder { return jt808.NewEncoder(base) },
			DataCommands: []string{
				model.CommandPositionSingle, model.CommandPositionStop,
				model.CommandRebootDevice, model.CommandPositionPeriodic,
				model.CommandAlarmArm, model.CommandAlarmDisarm, model.CommandAlarmDismiss,
				model.CommandEngineStop, model.CommandEngineResume, model.CommandCustom,
			},
		}
	})

	// OsmAnd is HTTP: the request is the frame, so there is no frame decoder and
	// no encoder.
	registry.Add(osmand.ProtocolName, func(base codec.Base) *Protocol {
		return &Protocol{
			Name:    osmand.ProtocolName,
			NewHTTP: func(inner codec.Base) codec.HTTPDecoder { return osmand.New(inner) },
		}
	})

	// MQTT carries one query string per publish, so it decodes the same payload
	// as OsmAnd and needs no encoder.
	registry.Add(mqtt.ProtocolName, func(base codec.Base) *Protocol {
		return &Protocol{
			Name:            mqtt.ProtocolName,
			NewFrameDecoder: func() codec.FrameDecoder { return mqtt.NewFrameDecoder() },
			NewDecoder:      func() codec.Decoder { return mqtt.New(base) },
		}
	})

	registry.Add(teltonika.ProtocolName, func(base codec.Base) *Protocol {
		return &Protocol{
			Name:            teltonika.ProtocolName,
			NewFrameDecoder: func() codec.FrameDecoder { return teltonika.NewFrameDecoder() },
			NewDecoder:      func() codec.Decoder { return teltonika.NewDecoder(base) },
			NewEncoder:      func() codec.Encoder { return teltonika.NewEncoder(base) },
			DataCommands: []string{
				model.CommandEngineStop, model.CommandEngineResume, model.CommandCustom,
			},
		}
	})

	return registry
}
