// Package mqtt implements the MQTT 3.1.1 endpoint of the gateway. A device
// connects, subscribes and publishes one query string per position; the payload
// is decoded with the same parser the OsmAnd HTTP protocol uses, which is what
// the Java server's MQTT base does as well.
package mqtt

import (
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/protocol/osmand"
)

// ProtocolName is the identifier used in configuration and stored on positions.
const ProtocolName = "mqtt"

// MQTT control packet types, the upper nibble of the fixed header.
const (
	typeConnect    = 1
	typeConnack    = 2
	typePublish    = 3
	typePuback     = 4
	typeSubscribe  = 8
	typeSuback     = 9
	typePingreq    = 12
	typePingresp   = 13
	typeDisconnect = 14
)

// Protocol decodes the MQTT frames of one connection.
type Protocol struct {
	codec.Base
}

// New creates the protocol.
func New(base codec.Base) *Protocol { return &Protocol{Base: base} }

// Name implements codec.Decoder.
func (p *Protocol) Name() string { return ProtocolName }

// NewFrameDecoder implements the frame splitter of the registry.
func NewFrameDecoder() codec.FrameDecoder { return &FrameDecoder{} }

// FrameDecoder splits the MQTT fixed header plus its variable length body.
type FrameDecoder struct{}

// DecodeFrame implements codec.FrameDecoder.
func (d *FrameDecoder) DecodeFrame(data []byte) ([]byte, int) {
	if len(data) < 2 {
		return nil, 0
	}
	length := 0
	multiplier := 1
	index := 1
	for {
		if index >= len(data) {
			return nil, 0
		}
		digit := data[index]
		length += int(digit&0x7f) * multiplier
		index++
		if digit&0x80 == 0 {
			break
		}
		multiplier *= 128
		if multiplier > 128*128*128 {
			// Malformed varint; drop the byte so the stream resynchronises.
			return nil, 1
		}
	}
	if len(data) < index+length {
		return nil, 0
	}
	return data[:index+length], index + length
}

// Decode implements codec.Decoder.
func (p *Protocol) Decode(frame []byte) (*codec.Result, error) {
	result := codec.NewResult()
	if len(frame) < 2 {
		return &codec.Result{}, nil
	}

	packetType := frame[0] >> 4
	body, err := packetBody(frame)
	if err != nil {
		return nil, err
	}

	switch packetType {
	case typeConnect:
		result.AddResponse(connack(false))
	case typePublish:
		decoded, packetID := p.decodePublish(frame[0], body)
		if decoded == nil {
			return &codec.Result{}, nil
		}
		result.Positions = append(result.Positions, decoded.Positions...)
		if packetID != nil {
			// Acknowledge the publish so the device does not resend it.
			result.AddResponse(puback(packetID))
		}
	case typeSubscribe:
		result.AddResponse(suback(body))
	case typePingreq:
		result.AddResponse([]byte{typePingresp << 4, 0})
	case typeDisconnect:
		return &codec.Result{}, nil
	default:
		return &codec.Result{}, nil
	}
	return result, nil
}

// packetBody strips the fixed header and validates the length.
func packetBody(frame []byte) ([]byte, error) {
	length := 0
	multiplier := 1
	index := 1
	for {
		if index >= len(frame) {
			return nil, fmt.Errorf("mqtt: truncated header")
		}
		digit := frame[index]
		length += int(digit&0x7f) * multiplier
		index++
		if digit&0x80 == 0 {
			break
		}
		multiplier *= 128
	}
	if index+length > len(frame) {
		return nil, fmt.Errorf("mqtt: truncated packet")
	}
	return frame[index : index+length], nil
}

// connack answers the CONNECT. The gateway accepts a session-less client: the
// device identifier is in every payload, so state does not matter.
func connack(sessionPresent bool) []byte {
	flags := byte(0)
	if sessionPresent {
		flags = 1
	}
	return []byte{typeConnack << 4, 2, flags, 0}
}

// puback echoes the packet identifier of a QoS 1 publish.
func puback(packetID []byte) []byte {
	if len(packetID) < 2 {
		return nil
	}
	return []byte{typePuback << 4, 2, packetID[0], packetID[1]}
}

// suback grants QoS 0 for every requested topic.
func suback(body []byte) []byte {
	if len(body) < 2 {
		return nil
	}
	// The packet identifier is followed by one (topic, qos) pair per filter, and
	// the answer carries one return code per filter.
	count := 0
	index := 2
	for index+2 <= len(body) {
		topicLength := int(binary.BigEndian.Uint16(body[index:]))
		index += 2 + topicLength + 1
		if index > len(body) {
			break
		}
		count++
	}
	response := []byte{typeSuback << 4, byte(2 + count), body[0], body[1]}
	for index := 0; index < count; index++ {
		response = append(response, 0)
	}
	return response
}

// decodePublish reads the topic, the optional packet identifier and the query
// string of a PUBLISH packet. The topic is ignored: like the Java server, the
// device identifier comes from the payload.
func (p *Protocol) decodePublish(flags byte, body []byte) (*codec.Result, []byte) {
	if len(body) < 2 {
		return nil, nil
	}
	topicLength := int(binary.BigEndian.Uint16(body))
	if len(body) < 2+topicLength {
		return nil, nil
	}
	rest := body[2+topicLength:]

	var packetID []byte
	// QoS is bits 1 and 2 of the fixed header flags; a QoS 1 publish carries the
	// packet identifier before the payload.
	qos := (flags >> 1) & 0x03
	if qos > 0 {
		if len(rest) < 2 {
			return nil, nil
		}
		packetID = rest[:2]
		rest = rest[2:]
	}

	values, err := url.ParseQuery(strings.TrimSpace(string(rest)))
	if err != nil {
		return nil, packetID
	}
	return osmand.Decode(p.Base, values), packetID
}
