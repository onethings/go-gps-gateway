// Package codec defines the protocol independent interfaces that every
// implementation in internal/protocol satisfies.
//
// The decoders are deliberately pure: they take the raw bytes of one complete
// frame and return the parsed positions plus any bytes that must be written
// back to the device. All I/O lives in internal/gateway.
package codec

import (
	"time"

	"traccar-server_go/go_server/internal/model"
)

// DeviceSession identifies a device that has been recognised by the gateway.
type DeviceSession struct {
	DeviceID int64
	UniqueID string
	// Model is the device model as stored on the device row. Protocols that
	// change their decode path per model read it here. Empty when the device
	// has no model set, which selects the generic path.
	Model string
}

// DeviceResolver maps a device unique identifier (IMEI and friends) to a
// session. A nil session with a nil error means "unknown device".
type DeviceResolver interface {
	Resolve(uniqueID string) (*DeviceSession, error)
}

// LastLocationProvider supplies the most recent known location, which several
// protocols reuse when a frame only carries cell tower information.
type LastLocationProvider interface {
	LastLocation(deviceID int64) *model.Position
}

// UniqueIDProvider maps a numeric device id back to its protocol identifier.
// Encoders need this to address commands to the right device.
type UniqueIDProvider interface {
	UniqueID(deviceID int64) (string, error)
}

// CommandAck is a device's answer to a command this server sent. Only the
// protocols that can recognise one fill it in.
type CommandAck struct {
	// Code is the protocol result code. Zero means the device accepted the
	// command; any other value means it refused it.
	Code int
}

// Result is the outcome of decoding a single frame.
type Result struct {
	// Positions holds every position decoded from the frame, in device order.
	Positions []*model.Position
	// Responses holds raw payloads that must be written back to the device.
	Responses [][]byte
	// Consumed reports whether the frame was recognised. A frame that is not
	// recognised yields no positions and no responses.
	Consumed bool
	// UniqueID is set by handshake frames that carry the device identifier
	// (GT06 login, Teltonika IMEI packet, JT808 register). The gateway binds
	// it to the connection and resolves the device through DeviceResolver.
	UniqueID string
	// CommandAck is set when the frame answers a command. The gateway closes
	// the command record of the connection's device with it.
	CommandAck *CommandAck
}

// NewResult creates an empty successful result.
func NewResult() *Result {
	return &Result{Consumed: true}
}

// AddPosition appends a decoded position.
func (r *Result) AddPosition(position *model.Position) {
	if position != nil {
		r.Positions = append(r.Positions, position)
	}
}

// AddResponse appends a payload to send back to the device.
func (r *Result) AddResponse(data []byte) {
	if len(data) > 0 {
		r.Responses = append(r.Responses, data)
	}
}

// FrameDecoder splits a TCP stream into protocol frames. Implementations keep
// whatever state they need (for example a negotiated message length).
type FrameDecoder interface {
	// DecodeFrame inspects data and returns one complete frame together with
	// the number of bytes consumed. When data does not yet contain a complete
	// frame it returns (nil, 0).
	DecodeFrame(data []byte) (frame []byte, consumed int)
}

// Decoder converts a single frame into positions.
type Decoder interface {
	// Name returns the protocol name, e.g. "teltonika".
	Name() string
	// Decode parses one complete frame. The unique ID in the frame is resolved
	// through the configured DeviceResolver.
	Decode(frame []byte) (*Result, error)
}

// SessionDecoder is implemented by protocols whose frames do not carry the
// device identifier after the handshake, so the gateway must supply the
// session bound to the connection. Implementations also implement Decoder,
// where Decode behaves like DecodeWithSession(frame, nil).
type SessionDecoder interface {
	Decoder
	DecodeWithSession(frame []byte, session *DeviceSession) (*Result, error)
}

// Encoder converts a high level command into device specific bytes.
type Encoder interface {
	// EncodeCommand returns the payload for the given command.
	EncodeCommand(command *model.Command) ([]byte, error)
}

// Base carries the shared decoder dependencies.
type Base struct {
	Resolver     DeviceResolver
	LastLocation LastLocationProvider
	UniqueIDs    UniqueIDProvider
	// Now is injectable for deterministic tests; nil means time.Now.
	Now func() time.Time
	// Ack enables the optional acknowledgement frames that some protocols only
	// send when explicitly enabled by configuration.
	Ack bool
}

// Time returns the current time, honouring the injected clock.
func (b *Base) Time() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}
