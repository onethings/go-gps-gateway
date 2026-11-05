// Package codec defines the protocol independent interfaces that every
// implementation in internal/protocol satisfies.
//
// The decoders are deliberately pure: they take the raw bytes of one complete
// frame and return the parsed positions plus any bytes that must be written
// back to the device. All I/O lives in internal/gateway.
package codec

import (
	"sync/atomic"
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
	// Serial is the serial of the downlink frame the device is answering, when
	// the protocol echoes one back. It pairs the answer with the exact command
	// that was sent. Protocols without a usable echo leave it nil, and the
	// answer closes the newest sent command of the device instead.
	Serial *int
}

// SerialSource hands out the message serial that protocols with a sequence
// number put into every downlink frame. One source per process keeps two
// outstanding commands from sharing a serial.
type SerialSource struct {
	next atomic.Uint32
}

// NewSerialSource creates a source that starts handing out serials at 1.
func NewSerialSource() *SerialSource { return &SerialSource{} }

// Next returns the next serial in the range 1..0xffff. Zero is skipped because
// model.Command reserves it for "this protocol has no serial".
func (s *SerialSource) Next() int {
	for {
		if value := s.next.Add(1) & 0xffff; value != 0 {
			return int(value)
		}
	}
}

// DefaultSerials is the source an encoder falls back to when its Base carries
// none, which is the normal case outside tests.
var DefaultSerials = NewSerialSource()

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

// MediaCodec is the video codec a media payload carries.
type MediaCodec uint8

const (
	// MediaCodecUnknown marks a payload the decoder could not identify.
	MediaCodecUnknown MediaCodec = iota
	// MediaCodecH264 is H.264/AVC in Annex-B framing.
	MediaCodecH264
	// MediaCodecH265 is H.265/HEVC in Annex-B framing.
	MediaCodecH265
)

// MediaFrame is one complete video access unit a protocol received from a
// device. Payload keeps the Annex-B start codes the terminal sent, so the
// segmenter prepends nothing and reorders nothing.
type MediaFrame struct {
	DeviceID  int64
	Channel   int
	Codec     MediaCodec
	Timestamp time.Time
	KeyFrame  bool
	Payload   []byte
}

// MediaSink consumes the video frames of the protocols that stream media
// instead of reporting positions. Implementations must treat Payload as read
// only and must not retain it after WriteFrame returns.
type MediaSink interface {
	WriteFrame(frame MediaFrame)
}

// Base carries the shared decoder dependencies.
type Base struct {
	Resolver     DeviceResolver
	LastLocation LastLocationProvider
	UniqueIDs    UniqueIDProvider
	// Serials hands out the serial of each downlink frame. Nil means the
	// process wide DefaultSerials, which is what a running gateway wants;
	// tests inject a fresh source to pin the serials they assert on.
	Serials *SerialSource
	// Now is injectable for deterministic tests; nil means time.Now.
	Now func() time.Time
	// Ack enables the optional acknowledgement frames that some protocols only
	// send when explicitly enabled by configuration.
	Ack bool
	// Config resolves one configuration key for the encoder's protocol, already
	// through the documented per protocol chain. It exists for the encoders
	// whose command layout depends on a setting (the JT808 alternative engine
	// control, the address a terminal is told to stream video to). Nil means no
	// configuration is available, which is the case in tests and whenever the
	// command types are listed without a running server.
	Config func(key string) string
	// Media receives the video frames of a media protocol such as JT1078. Nil
	// drops them, which is what a gateway without a configured stream path
	// wants.
	Media MediaSink
}

// NextSerial returns the serial for one downlink frame.
func (b *Base) NextSerial() int {
	if b.Serials != nil {
		return b.Serials.Next()
	}
	return DefaultSerials.Next()
}

// Time returns the current time, honouring the injected clock.
func (b *Base) Time() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}
