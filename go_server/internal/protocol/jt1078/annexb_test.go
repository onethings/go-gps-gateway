package jt1078

import (
	"bytes"
	"testing"
)

func TestSplitAccessUnitsGroupsParameterSets(t *testing.T) {
	// SPS, PPS, an IDR slice, then a P slice.
	stream := []byte{
		0x00, 0x00, 0x00, 0x01, 0x67, 0x42,
		0x00, 0x00, 0x00, 0x01, 0x68, 0xce,
		0x00, 0x00, 0x00, 0x01, 0x65, 0x88,
		0x00, 0x00, 0x00, 0x01, 0x41, 0x9a,
	}

	units := SplitAccessUnits(stream, false)
	if len(units) != 2 {
		t.Fatalf("units = %d, want 2", len(units))
	}
	if !IsKeyFrame(units[0], false) {
		t.Fatal("the first access unit should hold the IDR slice")
	}
	if IsKeyFrame(units[1], false) {
		t.Fatal("the second access unit is a P frame")
	}
	if !bytes.Contains(units[0], []byte{0x67}) || !bytes.Contains(units[0], []byte{0x68}) {
		t.Fatal("the parameter sets did not stay with the IDR slice")
	}
}
