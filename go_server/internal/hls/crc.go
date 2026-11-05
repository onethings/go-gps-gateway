package hls

// crc32MPEG2 is the CRC-32/MPEG-2 the transport stream tables carry. The Go
// standard library only ships the reflected CRC-32, so the table is built here
// with the non reflected polynomial.
const crcPoly = 0x04c11db7

var crcTable = func() [256]uint32 {
	var table [256]uint32
	for index := 0; index < 256; index++ {
		crc := uint32(index) << 24
		for bit := 0; bit < 8; bit++ {
			if crc&0x80000000 != 0 {
				crc = crc<<1 ^ crcPoly
			} else {
				crc <<= 1
			}
		}
		table[index] = crc
	}
	return table
}()

func crc32MPEG2(data []byte) uint32 {
	crc := uint32(0xffffffff)
	for _, value := range data {
		crc = crc<<8 ^ crcTable[byte(crc>>24)^value]
	}
	return crc
}
