package gator

import (
	"bytes"
	"testing"

	"traccar-server_go/go_server/internal/codec"
	"traccar-server_go/go_server/internal/model"
	. "traccar-server_go/go_server/internal/protocol/protocaltest"
)

// anyResolver accepts every identifier, like the mocked connection manager of
// the Java tests.
type anyResolver struct{}

func (anyResolver) Resolve(uniqueID string) (*codec.DeviceSession, error) {
	return &codec.DeviceSession{DeviceID: 1, UniqueID: uniqueID}, nil
}

// candidateResolver only knows the ids in known, so the decoder has to walk
// the candidate list before it finds a session.
type candidateResolver struct {
	known map[string]bool
	asked []string
}

func (r *candidateResolver) Resolve(uniqueID string) (*codec.DeviceSession, error) {
	r.asked = append(r.asked, uniqueID)
	if !r.known[uniqueID] {
		return nil, nil
	}
	return &codec.DeviceSession{DeviceID: 1, UniqueID: uniqueID}, nil
}

// The vectors are the exact frames of GatorProtocolDecoderTest.
func TestDecode(t *testing.T) {
	cases := []struct {
		name     string
		frame    string
		expected []*Expectation
	}{
		{
			"m588 position",
			"24248000278632210440910210250107210008006368860032001400000057c047000000185c050da7f9960d",
			[]*Expectation{Position()},
		},
		{
			"m588 acceleration alarm",
			"24248000278632210440910210250107210008006368860032001400000057c047000000185c050da7f90000000001960d",
			[]*Expectation{Position().Attribute(model.KeyAlarm, model.AlarmAcceleration)},
		},
		{
			"position with attributes",
			"242480002600341cad190917022021812497260280594200000000c047010000135400009bb600ff00b90d",
			[]*Expectation{PositionWith(ChecksAttributes)},
		},
		{
			"position with attributes 2",
			"2424800026364101b31608041108380273453415301532000000008000010000122800000124000000c40d",
			[]*Expectation{PositionWith(ChecksAttributes)},
		},
		{
			"heartbeat",
			"242421000658e3d851150d",
			nil,
		},
		{
			"position with attributes 3",
			"242480002658e3d851a60101c662bc00000000000000000000000000470007a30b0c00b10fc900ff00460d",
			[]*Expectation{PositionWith(ChecksAttributes)},
		},
		{
			"heartbeat 2",
			"242421000643e30282070d",
			nil,
		},
		{
			"position in norway",
			"24248000260009632d141121072702059226180104367500000000c04700079c0c34000ad80b00ff000a0d",
			[]*Expectation{Position().Location("2014-11-21T07:27:02.000Z", true, 59.37697, 10.72792)},
		},
		{
			"rollcall response",
			"24248100230CA23285100306145907022346901135294700000000C04001012C0E1100000021CB0D",
			[]*Expectation{Position()},
		},
		{
			"position in chile",
			"2424800023c2631e00111220104909833268648703804100000000c0470000000b4e00000000550d",
			[]*Expectation{Position().Location("2011-12-20T10:49:09.000Z", true, -33.44773, -70.63402)},
		},
		{
			"position in norway repeated",
			"24248000260009632d141121072702059226180104367500000000c04700079c0c34000ad80b00ff000a0d",
			[]*Expectation{Position()},
		},
	}

	decoder := NewDecoder(codec.Base{Resolver: anyResolver{}})
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := VerifyDecode(t, decoder, Hex(t, testCase.frame), testCase.expected...)
			for _, position := range result.Positions {
				if position.DeviceID != 1 {
					t.Fatalf("deviceId = %d, want 1", position.DeviceID)
				}
				if position.Protocol != ProtocolName {
					t.Fatalf("protocol = %q, want %q", position.Protocol, ProtocolName)
				}
			}
		})
	}
}

func TestDecodeID(t *testing.T) {
	if got := decodeID(12, 162, 50, 134); got != "3512345006" {
		t.Fatalf("decodeID = %q, want %q", got, "3512345006")
	}
}

const positionNorway = "24248000260009632d141121072702059226180104367500000000c04700079c0c34000ad80b00ff000a0d"

func TestDecodeTriesBothIdentifiers(t *testing.T) {
	resolver := &candidateResolver{known: map[string]bool{"3000099945": true}}
	decoder := NewDecoder(codec.Base{Resolver: resolver})

	VerifyDecode(t, decoder, Hex(t, positionNorway), Position())

	want := []string{"13000099945", "3000099945"}
	if len(resolver.asked) != len(want) {
		t.Fatalf("resolver asked %v, want %v", resolver.asked, want)
	}
	for i := range want {
		if resolver.asked[i] != want[i] {
			t.Fatalf("resolver asked %v, want %v", resolver.asked, want)
		}
	}
}

// When the first candidate resolves, the bare identifier is never tried.
func TestDecodeStopsAtFirstIdentifier(t *testing.T) {
	resolver := &candidateResolver{known: map[string]bool{"13000099945": true}}
	decoder := NewDecoder(codec.Base{Resolver: resolver})

	VerifyDecode(t, decoder, Hex(t, positionNorway), Position())

	if len(resolver.asked) != 1 || resolver.asked[0] != "13000099945" {
		t.Fatalf("resolver asked %v, want [13000099945]", resolver.asked)
	}
}

// The M588 variant carries the IMEI as the first eight bytes, so the resolver
// only sees that one identifier.
func TestDecodeM588Identifier(t *testing.T) {
	resolver := &candidateResolver{known: map[string]bool{"863221044091021": true}}
	decoder := NewDecoder(codec.Base{Resolver: resolver})

	result := VerifyDecode(t, decoder, Hex(t,
		"24248000278632210440910210250107210008006368860032001400000057c047000000185c050da7f9960d"),
		Position())

	if len(resolver.asked) != 1 || resolver.asked[0] != "863221044091021" {
		t.Fatalf("resolver asked %v, want [863221044091021]", resolver.asked)
	}
	VerifyResponse(t, result, string(Hex(t, "2424210005968000320d")))
}

func TestDecodeAcknowledgesEveryFrame(t *testing.T) {
	decoder := NewDecoder(codec.Base{Resolver: anyResolver{}})

	result := VerifyDecode(t, decoder, Hex(t, "242421000658e3d851150d"))
	VerifyResponse(t, result, string(Hex(t, "2424210005152100100d")))
}

func TestDecodeUnknownDevice(t *testing.T) {
	decoder := NewDecoder(codec.Base{Resolver: &StubResolver{Reject: true}})

	VerifyObject(t, decoder, Hex(t,
		"24248000260009632d141121072702059226180104367500000000c04700079c0c34000ad80b00ff000a0d"))
}

func TestDecodeRejectsShortFrame(t *testing.T) {
	decoder := NewDecoder(codec.Base{Resolver: anyResolver{}})

	VerifyObject(t, decoder, []byte{0x24, 0x24, 0x80})
}

func TestFrameDecoder(t *testing.T) {
	first := Hex(t, "24248000260009632d141121072702059226180104367500000000c04700079c0c34000ad80b00ff000a0d")
	second := Hex(t, "242421000658e3d851150d")
	garbage := Hex(t, "00ff247f")

	decoder := NewFrameDecoder()

	stream := append(append(append([]byte{}, garbage...), first...), second...)

	frame, consumed := decoder.DecodeFrame(stream)
	if !bytes.Equal(frame, first) {
		t.Fatalf("frame = %x, want %x", frame, first)
	}
	if consumed != len(garbage)+len(first) {
		t.Fatalf("consumed = %d, want %d", consumed, len(garbage)+len(first))
	}

	frame, consumed = decoder.DecodeFrame(stream[consumed:])
	if !bytes.Equal(frame, second) {
		t.Fatalf("second frame = %x, want %x", frame, second)
	}
	if consumed != len(second) {
		t.Fatalf("second consumed = %d, want %d", consumed, len(second))
	}

	// A truncated frame buffers without consuming the header.
	if frame, consumed = decoder.DecodeFrame(first[:len(first)-1]); frame != nil || consumed != 0 {
		t.Fatalf("truncated frame = %x, consumed = %d", frame, consumed)
	}

	// A trailing lone 0x24 is kept as a possible header start.
	if frame, consumed = decoder.DecodeFrame([]byte{0x00, 0xff, 0x24}); frame != nil || consumed != 2 {
		t.Fatalf("partial header = %x, consumed = %d", frame, consumed)
	}

	// An impossible length drops the header and resynchronises.
	if frame, consumed = decoder.DecodeFrame([]byte{0x24, 0x24, 0x80, 0xff, 0xff, 0x00}); frame != nil || consumed != 2 {
		t.Fatalf("oversized frame = %x, consumed = %d", frame, consumed)
	}
}
