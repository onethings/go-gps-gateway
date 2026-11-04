package jt1078

import (
	"encoding/binary"
	"time"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/helper"
	"traccar-server_go/go_server/internal/protocol/jt808"
)

// Frame type codes from the high nibble of the type byte.
const (
	typeIFrame = 0
	typePFrame = 1
	typeBFrame = 2
)

// Sub-package markers from the low nibble of the type byte.
const (
	subpackageAtomic = 0
	subpackageFirst  = 1
	subpackageLast   = 2
	subpackageMiddle = 3
)

// Payload types from the M/PT byte. The M bit is masked off before comparing.
const (
	payloadH264 = 98
	payloadH265 = 99
)

// maxAccessUnit caps the reassembly buffer of one sub-packaged frame. A broken
// terminal must not grow it without bound.
const maxAccessUnit = 8 << 20

// Decoder turns JT1078 packets into media frames for the sink. Sub-packaged
// access units are reassembled per channel, because one connection may carry
// the main and the sub stream at once.
type Decoder struct {
	base       codec.Base
	assemblies map[int]*assembly
	deviceID   int64
	uniqueID   string
}

// NewDecoder creates a decoder bound to the shared dependencies.
func NewDecoder(base codec.Base) *Decoder {
	return &Decoder{base: base, assemblies: make(map[int]*assembly)}
}

// Name implements codec.Decoder.
func (d *Decoder) Name() string { return ProtocolName }

// Decode implements codec.Decoder.
func (d *Decoder) Decode(frame []byte) (*codec.Result, error) {
	return d.decode(frame, nil)
}

// DecodeWithSession implements codec.SessionDecoder.
func (d *Decoder) DecodeWithSession(frame []byte, session *codec.DeviceSession) (*codec.Result, error) {
	return d.decode(frame, session)
}

func (d *Decoder) decode(frame []byte, session *codec.DeviceSession) (*codec.Result, error) {
	packet, ok := parsePacket(frame)
	if !ok || packet.dataType > typeBFrame || packet.codec == codec.MediaCodecUnknown {
		return &codec.Result{}, nil
	}

	unit, complete := d.assemble(packet)
	if !complete {
		return &codec.Result{}, nil
	}

	deviceID, err := d.resolve(packet.uniqueID, session)
	if err != nil {
		return nil, err
	}
	result := codec.NewResult()
	if deviceID == 0 {
		return result, nil
	}
	result.UniqueID = packet.uniqueID

	if d.base.Media != nil {
		d.base.Media.WriteFrame(codec.MediaFrame{
			DeviceID:  deviceID,
			Channel:   packet.channel,
			Codec:     unit.codec,
			Timestamp: unit.timestamp,
			KeyFrame:  unit.keyFrame,
			Payload:   unit.payload,
		})
	}
	return result, nil
}

// resolve keeps the device of the connection so a busy stream does not look it
// up once per packet.
func (d *Decoder) resolve(uniqueID string, session *codec.DeviceSession) (int64, error) {
	if session != nil && session.DeviceID != 0 {
		return session.DeviceID, nil
	}
	if d.deviceID != 0 && d.uniqueID == uniqueID {
		return d.deviceID, nil
	}
	if d.base.Resolver == nil {
		return 0, nil
	}
	resolved, err := d.base.Resolver.Resolve(uniqueID)
	if err != nil || resolved == nil {
		return 0, err
	}
	d.deviceID = resolved.DeviceID
	d.uniqueID = uniqueID
	return d.deviceID, nil
}

// accessUnit is one complete frame ready for the sink.
type accessUnit struct {
	payload   []byte
	codec     codec.MediaCodec
	timestamp time.Time
	keyFrame  bool
}

// assembly collects the sub-packages of one frame.
type assembly struct {
	payload   []byte
	codec     codec.MediaCodec
	timestamp time.Time
	keyFrame  bool
}

// assemble returns a complete access unit, either the atomic packet itself or
// the buffer a first, middle and last packet built.
func (d *Decoder) assemble(packet packet) (accessUnit, bool) {
	switch packet.subpackage {
	case subpackageAtomic:
		return accessUnit{
			payload:   packet.body,
			codec:     packet.codec,
			timestamp: packet.timestamp,
			keyFrame:  packet.dataType == typeIFrame,
		}, true

	case subpackageFirst:
		if len(packet.body) > maxAccessUnit {
			return accessUnit{}, false
		}
		d.assemblies[packet.channel] = &assembly{
			payload:   append([]byte(nil), packet.body...),
			codec:     packet.codec,
			timestamp: packet.timestamp,
			keyFrame:  packet.dataType == typeIFrame,
		}
		return accessUnit{}, false

	case subpackageMiddle:
		part := d.assemblies[packet.channel]
		if part == nil || len(part.payload)+len(packet.body) > maxAccessUnit {
			return accessUnit{}, false
		}
		part.payload = append(part.payload, packet.body...)
		return accessUnit{}, false

	case subpackageLast:
		part := d.assemblies[packet.channel]
		delete(d.assemblies, packet.channel)
		if part == nil || len(part.payload)+len(packet.body) > maxAccessUnit {
			return accessUnit{}, false
		}
		part.payload = append(part.payload, packet.body...)
		return accessUnit{
			payload:   part.payload,
			codec:     part.codec,
			timestamp: part.timestamp,
			keyFrame:  part.keyFrame,
		}, true
	}
	return accessUnit{}, false
}

// packet is the decoded header of one JT1078 packet.
type packet struct {
	uniqueID   string
	channel    int
	dataType   int
	subpackage int
	codec      codec.MediaCodec
	timestamp  time.Time
	body       []byte
}

func parsePacket(frame []byte) (packet, bool) {
	if len(frame) < 18 || binary.BigEndian.Uint32(frame) != magicValue {
		return packet{}, false
	}

	buf := helper.NewBuffer(frame)
	buf.ReadUnsignedInt()  // magic
	buf.ReadUnsignedByte() // V/P/X/CC
	payloadType := buf.ReadUnsignedByte() & 0x7f
	buf.ReadUnsignedShort() // packet index

	idLength := 6
	if buf.GetUnsignedShort(0) == 0 {
		idLength = 10
	}
	uniqueID := jt808.DecodeID(buf.ReadSlice(idLength))
	channel := buf.ReadUnsignedByte()

	rawType := buf.ReadUnsignedByte()
	dataType := rawType >> 4
	subpackage := rawType & 0x0f

	timestamp := time.UnixMilli(buf.ReadLong())
	if dataType <= typeBFrame {
		buf.SkipBytes(4) // last I-frame interval and last frame interval
	}

	bodyLength := buf.ReadUnsignedShort()
	body := buf.ReadSlice(bodyLength)
	if len(body) != bodyLength {
		return packet{}, false
	}

	videoCodec := codec.MediaCodecUnknown
	switch payloadType {
	case payloadH264:
		videoCodec = codec.MediaCodecH264
	case payloadH265:
		videoCodec = codec.MediaCodecH265
	}

	return packet{
		uniqueID:   uniqueID,
		channel:    channel,
		dataType:   dataType,
		subpackage: subpackage,
		codec:      videoCodec,
		timestamp:  timestamp,
		body:       body,
	}, true
}
