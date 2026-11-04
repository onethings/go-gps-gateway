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
	"traccar-server_go/go_server/internal/protocol/gator"
	"traccar-server_go/go_server/internal/protocol/gps056"
	"traccar-server_go/go_server/internal/protocol/gps103"
	"traccar-server_go/go_server/internal/protocol/gt02"
	"traccar-server_go/go_server/internal/protocol/gt06"
	"traccar-server_go/go_server/internal/protocol/h02"
	"traccar-server_go/go_server/internal/protocol/jt808"
	"traccar-server_go/go_server/internal/protocol/meitrack"
	"traccar-server_go/go_server/internal/protocol/mqtt"
	"traccar-server_go/go_server/internal/protocol/osmand"
	"traccar-server_go/go_server/internal/protocol/ruptela"
	"traccar-server_go/go_server/internal/protocol/suntech"
	"traccar-server_go/go_server/internal/protocol/teltonika"
	"traccar-server_go/go_server/internal/protocol/tk103"
	"traccar-server_go/go_server/internal/protocol/wondex"
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
				model.CommandPositionPeriodic, model.CommandCustom,
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
				model.CommandEngineStop, model.CommandEngineResume,
				model.CommandConfiguration, model.CommandCustom, model.CommandFactoryReset,
				model.CommandGetDeviceStatus, model.CommandGetVersion,
				model.CommandPositionPeriodic, model.CommandPositionSingle, model.CommandPositionStop,
				model.CommandPowerOff, model.CommandRebootDevice,
				model.CommandSetConnection, model.CommandSetPhonebook, model.CommandSOSNumber,
				model.CommandVoiceMonitoring,
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
				model.CommandCustom, model.CommandConfiguration, model.CommandRebootDevice,
				model.CommandPositionPeriodic, model.CommandPositionSingle, model.CommandPositionStop,
				model.CommandAlarmArm, model.CommandAlarmDisarm, model.CommandAlarmDismiss,
				model.CommandEngineStop, model.CommandEngineResume, model.CommandSetConnection,
				model.CommandPowerOff, model.CommandFactoryReset, model.CommandMessage,
				model.CommandVoiceMessage, model.CommandRequestPhoto, model.CommandSetSpeedLimit,
				model.CommandOutputControl, model.CommandVideoStart, model.CommandVideoStop,
			},
		}
	})

	// The four text protocols share the same shape: one sentence per frame and a
	// comma or semicolon field separator.
	registry.Add(gps103.ProtocolName, func(base codec.Base) *Protocol {
		return &Protocol{
			Name:            gps103.ProtocolName,
			NewFrameDecoder: func() codec.FrameDecoder { return &gps103.FrameDecoder{} },
			NewDecoder:      func() codec.Decoder { return gps103.NewDecoder(base) },
			NewEncoder:      func() codec.Encoder { return gps103.NewEncoder(base) },
			TextCommands: []string{
				model.CommandAlarmArm, model.CommandAlarmDisarm,
				model.CommandCustom, model.CommandEngineResume, model.CommandEngineStop,
				model.CommandPositionPeriodic, model.CommandPositionSingle, model.CommandPositionStop,
				model.CommandRequestPhoto,
			},
		}
	})

	registry.Add(tk103.ProtocolName, func(base codec.Base) *Protocol {
		return &Protocol{
			Name:            tk103.ProtocolName,
			NewFrameDecoder: func() codec.FrameDecoder { return &tk103.FrameDecoder{} },
			NewDecoder:      func() codec.Decoder { return tk103.NewDecoder(base) },
			NewEncoder:      func() codec.Encoder { return tk103.NewEncoder(base) },
			TextCommands: []string{
				model.CommandCustom, model.CommandGetVersion, model.CommandPowerOff,
				model.CommandRebootDevice, model.CommandPositionSingle, model.CommandPositionPeriodic,
				model.CommandPositionStop, model.CommandGetDeviceStatus, model.CommandIdentification,
				model.CommandModeDeepSleep, model.CommandModePowerSaving, model.CommandAlarmSOS,
				model.CommandSetConnection, model.CommandSOSNumber, model.CommandSetOdometer,
				model.CommandEngineStop, model.CommandEngineResume, model.CommandOutputControl,
			},
		}
	})

	registry.Add(meitrack.ProtocolName, func(base codec.Base) *Protocol {
		return &Protocol{
			Name:            meitrack.ProtocolName,
			NewFrameDecoder: func() codec.FrameDecoder { return &meitrack.FrameDecoder{} },
			NewDecoder:      func() codec.Decoder { return meitrack.NewDecoder(base) },
			NewEncoder:      func() codec.Encoder { return meitrack.NewEncoder(base) },
			TextCommands: []string{
				model.CommandAlarmArm, model.CommandAlarmDisarm, model.CommandCustom,
				model.CommandEngineResume, model.CommandEngineStop, model.CommandPositionSingle,
				model.CommandRequestPhoto, model.CommandSendSMS,
			},
		}
	})

	registry.Add(wondex.ProtocolName, func(base codec.Base) *Protocol {
		return &Protocol{
			Name:            wondex.ProtocolName,
			NewFrameDecoder: func() codec.FrameDecoder { return &wondex.FrameDecoder{} },
			NewDecoder:      func() codec.Decoder { return wondex.NewDecoder(base) },
			NewEncoder:      func() codec.Encoder { return wondex.NewEncoder(base) },
			TextCommands: []string{
				model.CommandGetDeviceStatus, model.CommandGetModemStatus, model.CommandGetVersion,
				model.CommandIdentification, model.CommandPositionSingle, model.CommandRebootDevice,
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

	// Suntech splits its stream into length delimited binary frames and
	// carriage return terminated text frames. The encoder writes the legacy
	// text command layout; selecting the universal layout needs the live
	// decoder state, which the router does not have.
	registry.Add(suntech.ProtocolName, func(base codec.Base) *Protocol {
		return &Protocol{
			Name:            suntech.ProtocolName,
			NewFrameDecoder: func() codec.FrameDecoder { return suntech.NewFrameDecoder() },
			NewDecoder:      func() codec.Decoder { return suntech.NewDecoder(base) },
			NewEncoder:      func() codec.Encoder { return suntech.NewEncoder(base) },
			TextCommands: []string{
				model.CommandAlarmArm, model.CommandAlarmDisarm,
				model.CommandEngineResume, model.CommandEngineStop, model.CommandOutputControl,
				model.CommandPositionSingle, model.CommandRebootDevice,
			},
		}
	})

	// Ruptela frames are length delimited: the leading two byte length covers
	// the rest of the message.
	registry.Add(ruptela.ProtocolName, func(base codec.Base) *Protocol {
		return &Protocol{
			Name:            ruptela.ProtocolName,
			NewFrameDecoder: func() codec.FrameDecoder { return ruptela.NewFrameDecoder() },
			NewDecoder:      func() codec.Decoder { return ruptela.NewDecoder(base) },
			NewEncoder:      func() codec.Encoder { return ruptela.NewEncoder(base) },
			DataCommands: []string{
				model.CommandCustom, model.CommandEngineStop, model.CommandEngineResume,
				model.CommandRequestPhoto, model.CommandConfiguration,
				model.CommandGetVersion, model.CommandFirmwareUpdate,
				model.CommandOutputControl, model.CommandSetConnection,
				model.CommandSetOdometer,
			},
		}
	})

	registry.Add(gator.ProtocolName, func(base codec.Base) *Protocol {
		return &Protocol{
			Name:            gator.ProtocolName,
			NewFrameDecoder: func() codec.FrameDecoder { return gator.NewFrameDecoder() },
			NewDecoder:      func() codec.Decoder { return gator.NewDecoder(base) },
			NewEncoder:      func() codec.Encoder { return gator.NewEncoder(base) },
			DataCommands: []string{
				model.CommandEngineResume, model.CommandEngineStop,
				model.CommandPositionSingle, model.CommandSetOdometer,
				model.CommandSetSpeedLimit,
			},
		}
	})

	registry.Add(gps056.ProtocolName, func(base codec.Base) *Protocol {
		return &Protocol{
			Name:            gps056.ProtocolName,
			NewFrameDecoder: func() codec.FrameDecoder { return gps056.NewFrameDecoder() },
			NewDecoder:      func() codec.Decoder { return gps056.NewDecoder(base) },
		}
	})

	registry.Add(gt02.ProtocolName, func(base codec.Base) *Protocol {
		return &Protocol{
			Name:            gt02.ProtocolName,
			NewFrameDecoder: func() codec.FrameDecoder { return gt02.NewFrameDecoder() },
			NewDecoder:      func() codec.Decoder { return gt02.NewDecoder(base) },
		}
	})

	return registry
}
