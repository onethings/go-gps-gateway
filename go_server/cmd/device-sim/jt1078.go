package main

import (
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"traccar-server_go/go_server/internal/protocol/jt1078"
)

// jt1078Chunk is the body size of one sub-package. A larger access unit is
// split the way a terminal splits it, which exercises the reassembly path.
const jt1078Chunk = 1400

// runJT1078 pushes an Annex-B elementary stream to a JT1078 listener.
func runJT1078(
	address, path, uniqueID string, channel int, codecName string, fps int,
	stdout, stderr io.Writer,
) int {
	stream, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "device-sim: %v\n", err)
		return 1
	}
	identifier, err := parseJT1078ID(uniqueID)
	if err != nil {
		fmt.Fprintf(stderr, "device-sim: %v\n", err)
		return 2
	}

	if codecName != "h264" && codecName != "h265" {
		fmt.Fprintf(stderr, "device-sim: -jt1078-codec must be h264 or h265, not %q\n", codecName)
		return 2
	}
	h265 := codecName == "h265"
	payloadType := byte(98)
	if h265 {
		payloadType = 99
	}
	units := jt1078.SplitAccessUnits(stream, h265)
	if len(units) == 0 {
		fmt.Fprintln(stderr, "device-sim: no access units in the file")
		return 1
	}

	conn, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		fmt.Fprintf(stderr, "device-sim: %v\n", err)
		return 1
	}
	defer conn.Close()

	var interval time.Duration
	if fps > 0 {
		interval = time.Second / time.Duration(fps)
	}
	start := time.Now()
	keyFrames := 0

	for index, unit := range units {
		dataType := byte(1)
		if jt1078.IsKeyFrame(unit, h265) {
			dataType = 0
			keyFrames++
		}
		at := start.Add(time.Duration(index) * interval)
		for _, packet := range accessUnitPackets(identifier, channel, dataType, payloadType, at, unit) {
			if _, err := conn.Write(packet); err != nil {
				fmt.Fprintf(stderr, "device-sim: write: %v\n", err)
				return 1
			}
		}
		if interval > 0 {
			time.Sleep(interval)
		}
	}

	fmt.Fprintf(stdout, "sent %d access units (%d key frames) to %s\n", len(units), keyFrames, address)
	return 0
}

// parseJT1078ID turns the twelve or twenty digit terminal identifier into the
// BCD style bytes the protocol carries.
func parseJT1078ID(text string) ([]byte, error) {
	if len(text) != 12 && len(text) != 20 {
		return nil, fmt.Errorf("identifier %q must be 12 or 20 digits", text)
	}
	return hex.DecodeString(text)
}

// accessUnitPackets wraps one access unit in JT1078 packets, splitting it when
// it does not fit one body.
func accessUnitPackets(
	identifier []byte, channel int, dataType, payloadType byte, at time.Time, unit []byte,
) [][]byte {
	if len(unit) <= jt1078Chunk {
		return [][]byte{jt1078Packet(identifier, channel, dataType, 0, payloadType, at, unit)}
	}

	var packets [][]byte
	for offset := 0; offset < len(unit); offset += jt1078Chunk {
		end := offset + jt1078Chunk
		if end > len(unit) {
			end = len(unit)
		}
		subpackage := byte(3)
		switch {
		case offset == 0:
			subpackage = 1
		case end == len(unit):
			subpackage = 2
		}
		packets = append(packets, jt1078Packet(
			identifier, channel, dataType, subpackage, payloadType, at, unit[offset:end]))
	}
	return packets
}

func jt1078Packet(
	identifier []byte, channel int, dataType, subpackage, payloadType byte, at time.Time, body []byte,
) []byte {
	out := append([]byte(nil), jt1078.Magic...)
	out = append(out, 0x80)             // V/P/X/CC
	out = append(out, 0x80|payloadType) // marker bit and payload type
	out = append(out, 0x00, 0x01)       // packet index
	out = append(out, identifier...)
	out = append(out, byte(channel))
	out = append(out, dataType<<4|subpackage)

	stamp := uint64(at.UnixMilli())
	for shift := 56; shift >= 0; shift -= 8 {
		out = append(out, byte(stamp>>shift))
	}
	if dataType <= 2 {
		out = append(out, 0x00, 0x00, 0x00, 0x00)
	}
	out = append(out, byte(len(body)>>8), byte(len(body)))
	return append(out, body...)
}
