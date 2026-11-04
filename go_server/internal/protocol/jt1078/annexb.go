package jt1078

// Annex-B parsing shared by the decoder tests and the device simulator. A
// terminal sends H.264 or H.265 as start code delimited NAL units, and one
// JT1078 body is one access unit, which may hold several of them.

// SplitAccessUnits groups the NAL units of an elementary stream into access
// units. A delimiter, a parameter set or a picture starting slice begins the
// next unit, so SPS and PPS stay with the IDR slice that needs them and the
// slices of one picture stay together.
func SplitAccessUnits(data []byte, h265 bool) [][]byte {
	if len(data) == 0 {
		return nil
	}

	var (
		units   [][]byte
		current []byte
		hasVCL  bool
	)
	for _, nal := range NALUnits(data) {
		vcl := IsVCL(nal, h265)
		boundary := len(current) > 0 && (IsAccessUnitDelimiter(nal, h265) ||
			hasVCL && !vcl ||
			vcl && hasVCL && startsNewPicture(nal, h265))
		if boundary {
			units = append(units, current)
			current = nil
			hasVCL = false
		}
		current = append(current, nal...)
		if vcl {
			hasVCL = true
		}
	}
	if len(current) > 0 {
		units = append(units, current)
	}
	return units
}

// startsNewPicture reports whether a slice starts a picture. The first value of
// a slice header is first_mb_in_slice for H.264 and
// first_slice_segment_in_pic_flag for H.265; both are zero for a new picture,
// which is a single set bit at the start of the RBSP.
func startsNewPicture(nal []byte, h265 bool) bool {
	headerLength := 1
	if h265 {
		headerLength = 2
	}
	start := startCodeLength(nal) + headerLength
	if start >= len(nal) {
		return false
	}
	data := removeEmulationPrevention(nal[start:])
	return len(data) > 0 && data[0]&0x80 != 0
}

func startCodeLength(nal []byte) int {
	if len(nal) >= 4 && nal[0] == 0x00 && nal[1] == 0x00 && nal[2] == 0x00 && nal[3] == 0x01 {
		return 4
	}
	return 3
}

// removeEmulationPrevention drops the 0x03 a start code emulation inserts, so
// the next bit is the real slice header bit.
func removeEmulationPrevention(data []byte) []byte {
	for index := 2; index < len(data); index++ {
		if data[index] == 0x03 && data[index-1] == 0x00 && data[index-2] == 0x00 {
			out := make([]byte, 0, len(data)-1)
			out = append(out, data[:index]...)
			return append(out, data[index+1:]...)
		}
	}
	return data
}

// NALUnits returns the NAL units of an Annex-B stream, each including its start
// code.
func NALUnits(data []byte) [][]byte {
	var (
		units [][]byte
		start = -1
	)
	for index := 0; index+3 <= len(data); index++ {
		if data[index] != 0x00 || data[index+1] != 0x00 {
			continue
		}
		length := 0
		switch {
		case data[index+2] == 0x01:
			length = 3
		case index+4 <= len(data) && data[index+2] == 0x00 && data[index+3] == 0x01:
			length = 4
		}
		if length == 0 {
			continue
		}
		if start >= 0 {
			units = append(units, data[start:index])
		}
		start = index
		index += length - 1
	}
	if start >= 0 {
		units = append(units, data[start:])
	}
	return units
}

// NALHeader returns the first byte of the NAL unit after its start code.
func NALHeader(nal []byte) byte {
	switch {
	case len(nal) > 4 && nal[2] == 0x00 && nal[3] == 0x01:
		return nal[4]
	case len(nal) > 3 && nal[2] == 0x01:
		return nal[3]
	}
	return 0
}

// IsVCL reports whether the NAL unit carries slice data.
func IsVCL(nal []byte, h265 bool) bool {
	header := NALHeader(nal)
	if h265 {
		return header>>1&0x3f <= 31
	}
	return header&0x1f >= 1 && header&0x1f <= 5
}

// IsAccessUnitDelimiter reports whether the NAL unit is an access unit
// delimiter.
func IsAccessUnitDelimiter(nal []byte, h265 bool) bool {
	header := NALHeader(nal)
	if h265 {
		return header>>1&0x3f == 35
	}
	return header&0x1f == 9
}

// IsKeyFrame reports whether the access unit holds an IDR slice.
func IsKeyFrame(unit []byte, h265 bool) bool {
	for _, nal := range NALUnits(unit) {
		header := NALHeader(nal)
		if h265 {
			kind := header >> 1 & 0x3f
			if kind == 19 || kind == 20 {
				return true
			}
			continue
		}
		if header&0x1f == 5 {
			return true
		}
	}
	return false
}
