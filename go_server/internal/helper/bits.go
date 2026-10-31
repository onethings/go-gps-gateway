package helper

// CheckBit reports whether bit index of number is set.
func CheckBit(number int64, index uint) bool {
	return number&(1<<index) != 0
}

// BitsBetween extracts the bit range [from, to) of number.
func BitsBetween(number int32, from, to uint) int32 {
	return (number >> from) & ((1 << (to - from)) - 1)
}

// BitsFrom shifts number right by from bits, discarding the lower bits.
func BitsFrom(number int32, from uint) int32 {
	return number >> from
}

// BitsTo keeps only the lowest to bits of number.
func BitsTo(number int32, to uint) int32 {
	return BitsBetween(number, 0, to)
}

// BitsBetween64 extracts the bit range [from, to) of a 64 bit number.
func BitsBetween64(number int64, from, to uint) int64 {
	return (number >> from) & ((1 << (to - from)) - 1)
}

// BitsFrom64 shifts a 64 bit number right by from bits.
func BitsFrom64(number int64, from uint) int64 {
	return number >> from
}

// BitsTo64 keeps only the lowest to bits of a 64 bit number.
func BitsTo64(number int64, to uint) int64 {
	return BitsBetween64(number, 0, to)
}
