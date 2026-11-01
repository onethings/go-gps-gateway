package jt808

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"traccar-server_go/go_server/internal/helper"
)

// Message types, matching the MSG_* constants in Jt808ProtocolDecoder.
const (
	msgGeneralResponse      = 0x8001
	msgGeneralResponse2     = 0x4401
	msgHeartbeat            = 0x0002
	msgTerminalLogout       = 0x0003
	msgHeartbeat2           = 0x0506
	msgTerminalRegister     = 0x0100
	msgTerminalRegisterResp = 0x8100
	msgTerminalAuth         = 0x0102
	msgTerminalAttributes   = 0x0107
	msgLocationReport       = 0x0200
	msgLocationQueryResp    = 0x0201
	msgLocationBatch        = 0x0704
	msgLocationBatch2       = 0x0210
	msgLocationReport2      = 0x5501
	msgLocationReportBlind  = 0x5502
	msgTimeSyncRequest      = 0x0109
	msgTimeSyncResponse     = 0x8109
	msgTimezoneSync         = 0x1007
	msgTerminalControl      = 0x8105
	msgParameterSetting     = 0x0310
	msgLocationQuery        = 0x8201
	msgTemporaryTracking    = 0x8202
	msgAlarmAck             = 0x8203
	msgResultSuccess        = 0x00
)

// msgTerminalGeneralResponse is the answer a terminal sends to a downlink
// message. It is the reverse direction of msgGeneralResponse, which the
// platform sends.
const msgTerminalGeneralResponse = 0x0001

// header is the parsed JT808 message header.
type header struct {
	delimiter   byte
	messageType int
	attribute   int
	bodyLength  int
	version     *byte
	id          []byte
	index       int
	shortIndex  bool
}

func parseHeader(frame []byte) (*header, *helper.Buffer, bool) {
	if len(frame) < 12 {
		return nil, nil, false
	}

	buf := helper.NewBuffer(frame)
	h := &header{delimiter: byte(buf.ReadUnsignedByte())}
	h.messageType = buf.ReadUnsignedShort()
	h.attribute = buf.ReadUnsignedShort()
	h.bodyLength = h.attribute & 0x03ff

	if h.attribute&0x4000 != 0 {
		version := byte(buf.ReadUnsignedByte())
		h.version = &version
	}

	idLength := 6
	if h.version != nil {
		idLength = 10
	} else if h.delimiter == 0xe7 {
		idLength = 7
	}
	if buf.ReadableBytes() < idLength+3 {
		return nil, nil, false
	}
	h.id = buf.ReadSlice(idLength)

	switch h.messageType {
	case msgLocationReport2, msgLocationReportBlind:
		h.shortIndex = true
		h.index = buf.ReadUnsignedByte()
	default:
		h.index = buf.ReadUnsignedShort()
	}

	return h, buf, true
}

// decodeID renders the terminal identifier. Numeric BCD identifiers are used
// as-is; binary identifiers become the decimal IMEI plus its Luhn digit.
func decodeID(id []byte) string {
	serial := hex.EncodeToString(id)
	if isDigits(serial) {
		return serial
	}
	imei := int64(binary.BigEndian.Uint16(id))<<32 | int64(binary.BigEndian.Uint32(id[2:]))
	return strconv.FormatInt(imei, 10) + strconv.Itoa(helper.Luhn(imei))
}

// encodeID builds the terminal identifier bytes in the given width.
func encodeID(uniqueID string, length int) ([]byte, error) {
	if length == 10 {
		padded := fmt.Sprintf("%20s", uniqueID)
		out := []byte(padded)
		for i := range out {
			if out[i] == ' ' {
				out[i] = '0'
			}
		}
		return hex.DecodeString(string(out))
	}
	if len(uniqueID)%2 == 0 {
		return hex.DecodeString(uniqueID)
	}
	imei, err := strconv.ParseInt(uniqueID[:len(uniqueID)-1], 10, 64)
	if err != nil {
		return nil, err
	}
	id := make([]byte, 6)
	binary.BigEndian.PutUint16(id, uint16(imei>>32))
	binary.BigEndian.PutUint32(id[2:], uint32(imei))
	return id, nil
}

// readDate reads the six packed BCD date/time bytes used by JT808.
func readDate(buf *helper.Buffer, location *time.Location) time.Time {
	year := helper.BcdReadInteger(buf, 2)
	month := helper.BcdReadInteger(buf, 2)
	day := helper.BcdReadInteger(buf, 2)
	hour := helper.BcdReadInteger(buf, 2)
	minute := helper.BcdReadInteger(buf, 2)
	second := helper.BcdReadInteger(buf, 2)
	return time.Date(2000+year, time.Month(month), day, hour, minute, second, 0, location)
}

func isDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func bitSet(value uint32, index uint) bool {
	return value&(1<<index) != 0
}

// buildMessage frames and escapes a message. The escape rules mirror
// Jt808FrameEncoder, so the returned bytes are ready to be written to the
// device as they are.
func buildMessage(h *header, messageType int, payload []byte) []byte {
	attribute := len(payload)
	if h.version != nil {
		attribute |= 0x4000
	}

	body := make([]byte, 0, len(payload)+16)
	body = append(body, byte(messageType>>8), byte(messageType),
		byte(attribute>>8), byte(attribute))
	if h.version != nil {
		body = append(body, *h.version)
	}
	body = append(body, h.id...)
	if h.shortIndex {
		body = append(body, 1)
	} else {
		body = append(body, 0, 0)
	}
	body = append(body, payload...)

	body = append(body, helper.XOR(body))

	alternative := h.delimiter == 0xe7
	frame := make([]byte, 0, len(body)+2)
	frame = append(frame, h.delimiter)
	for _, b := range body {
		if alternative {
			switch b {
			case 0xe6:
				frame = append(frame, 0xe6, 0x01)
			case 0xe7:
				frame = append(frame, 0xe6, 0x02)
			case 0x3d:
				frame = append(frame, 0x3e, 0x02)
			case 0x3e:
				frame = append(frame, 0x3e, 0x01)
			default:
				frame = append(frame, b)
			}
			continue
		}
		switch b {
		case 0x7d:
			frame = append(frame, 0x7d, 0x01)
		case 0x7e:
			frame = append(frame, 0x7d, 0x02)
		default:
			frame = append(frame, b)
		}
	}
	frame = append(frame, h.delimiter)
	return frame
}
