package helper

// CRC16X25 computes the CRC-16/X-25 checksum (poly 0x1021 reflected, init
// 0xFFFF, final XOR 0xFFFF) used by the GT06 family.
func CRC16X25(data []byte) int {
	crc := 0xFFFF
	for _, b := range data {
		crc ^= int(b)
		for i := 0; i < 8; i++ {
			if crc&1 != 0 {
				crc = crc>>1 ^ 0x8408
			} else {
				crc >>= 1
			}
		}
	}
	return crc ^ 0xFFFF
}

// CRC16Kermit computes the CRC-16/KERMIT checksum (poly 0x1021 reflected, init
// 0x0000, no final XOR) used by the Ruptela family.
func CRC16Kermit(data []byte) int {
	crc := 0x0000
	for _, b := range data {
		crc ^= int(b)
		for i := 0; i < 8; i++ {
			if crc&1 != 0 {
				crc = crc>>1 ^ 0x8408
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}

// CRC16IBM computes the CRC-16/IBM checksum (poly 0x8005 reflected, init
// 0x0000) used by the Teltonika codecs.
func CRC16IBM(data []byte) int {
	crc := 0x0000
	for _, b := range data {
		crc ^= int(b)
		for i := 0; i < 8; i++ {
			if crc&1 != 0 {
				crc = crc>>1 ^ 0xA001
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}

// XOR returns the JT808 style exclusive-or checksum.
func XOR(data []byte) byte {
	var sum byte
	for _, b := range data {
		sum ^= b
	}
	return sum
}

// Luhn returns the Luhn check digit of a numeric identifier.
func Luhn(number int64) int {
	checksum := int64(0)
	remain := number
	for i := 0; remain != 0; i++ {
		digit := remain % 10
		if i%2 == 0 {
			digit *= 2
			if digit >= 10 {
				digit = 1 + digit%10
			}
		}
		checksum += digit
		remain /= 10
	}
	return int((10 - checksum%10) % 10)
}
