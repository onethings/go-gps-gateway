package helper

// BcdReadInteger reads a packed BCD number of the given digit count, matching
// BcdUtil.readInteger in Traccar. Odd digit counts consume the high nibble of
// the next byte without advancing past it.
func BcdReadInteger(buf *Buffer, digits int) int {
	result := 0

	for i := 0; i < digits/2; i++ {
		b := buf.ReadUnsignedByte()
		result *= 10
		result += b >> 4
		result *= 10
		result += b & 0x0f
	}

	if digits%2 != 0 {
		b := buf.GetUnsignedByte(0)
		result *= 10
		result += b >> 4
	}

	return result
}

// BcdReadCoordinate reads a packed BCD coordinate in the format used by many
// Chinese trackers: DDMM.MMMM encoded across four bytes with the hemisphere in
// the top bit of the first byte.
func BcdReadCoordinate(buf *Buffer) float64 {
	b1 := buf.ReadUnsignedByte()
	b2 := buf.ReadUnsignedByte()
	b3 := buf.ReadUnsignedByte()
	b4 := buf.ReadUnsignedByte()

	value := float64(b2&0xf)*10 + float64(b3>>4)
	value += (float64((b3&0xf)*10+(b4>>4))*10 + float64(b4&0xf)) / 1000.0
	value /= 60
	value += float64((b1>>4&0x7)*10+(b1&0xf))*10 + float64(b2>>4)

	if b1&0x80 != 0 {
		value = -value
	}

	return value
}
