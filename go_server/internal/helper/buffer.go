// Package helper contains low level parsing utilities shared by the protocol
// decoders. They are ports of the corresponding org.traccar.helper classes.
package helper

import (
	"encoding/binary"
	"errors"
)

// ErrShortBuffer is returned when a read would exceed the available data.
var ErrShortBuffer = errors.New("buffer underflow")

// Buffer is a big-endian byte reader equivalent to a Netty ByteBuf as used by
// the Traccar protocol decoders.
type Buffer struct {
	data []byte
	pos  int
}

// NewBuffer wraps a byte slice.
func NewBuffer(data []byte) *Buffer {
	return &Buffer{data: data}
}

// Bytes returns the underlying data.
func (b *Buffer) Bytes() []byte {
	return b.data
}

// ReaderIndex returns the current read offset.
func (b *Buffer) ReaderIndex() int {
	return b.pos
}

// ReadableBytes returns how many bytes can still be read.
func (b *Buffer) ReadableBytes() int {
	return len(b.data) - b.pos
}

// IsReadable reports whether at least one byte remains.
func (b *Buffer) IsReadable() bool {
	return b.pos < len(b.data)
}

// SkipBytes advances the read offset.
func (b *Buffer) SkipBytes(n int) {
	b.pos += n
	if b.pos > len(b.data) {
		b.pos = len(b.data)
	}
}

// SetReaderIndex moves the read offset.
func (b *Buffer) SetReaderIndex(index int) {
	b.pos = index
}

// GetUnsignedByte returns the byte at index relative to the reader index
// without advancing it.
func (b *Buffer) GetUnsignedByte(index int) int {
	pos := b.pos + index
	if pos < 0 || pos >= len(b.data) {
		return 0
	}
	return int(b.data[pos])
}

// GetByte returns the signed byte at index relative to the reader index.
func (b *Buffer) GetByte(index int) int8 {
	return int8(b.GetUnsignedByte(index))
}

// GetUnsignedShort returns a big-endian uint16 at index relative to the reader
// index without advancing it.
func (b *Buffer) GetUnsignedShort(index int) int {
	pos := b.pos + index
	if pos < 0 || pos+2 > len(b.data) {
		return 0
	}
	return int(binary.BigEndian.Uint16(b.data[pos:]))
}

// GetInt returns a big-endian int32 at index relative to the reader index.
func (b *Buffer) GetInt(index int) int32 {
	pos := b.pos + index
	if pos < 0 || pos+4 > len(b.data) {
		return 0
	}
	return int32(binary.BigEndian.Uint32(b.data[pos:]))
}

// ReadSignedByte reads a single signed byte.
func (b *Buffer) ReadSignedByte() int8 {
	return int8(b.ReadUnsignedByte())
}

// ReadUnsignedByte reads a single unsigned byte.
func (b *Buffer) ReadUnsignedByte() int {
	if b.pos >= len(b.data) {
		return 0
	}
	v := b.data[b.pos]
	b.pos++
	return int(v)
}

// ReadUnsignedShort reads a big-endian uint16.
func (b *Buffer) ReadUnsignedShort() int {
	if b.pos+2 > len(b.data) {
		b.pos = len(b.data)
		return 0
	}
	v := binary.BigEndian.Uint16(b.data[b.pos:])
	b.pos += 2
	return int(v)
}

// ReadShort reads a big-endian int16.
func (b *Buffer) ReadShort() int16 {
	return int16(b.ReadUnsignedShort())
}

// ReadInt reads a big-endian int32.
func (b *Buffer) ReadInt() int32 {
	if b.pos+4 > len(b.data) {
		b.pos = len(b.data)
		return 0
	}
	v := int32(binary.BigEndian.Uint32(b.data[b.pos:]))
	b.pos += 4
	return v
}

// ReadUnsignedInt reads a big-endian uint32.
func (b *Buffer) ReadUnsignedInt() uint32 {
	return uint32(b.ReadInt())
}

// ReadUnsignedMedium reads a big-endian 24 bit unsigned integer.
func (b *Buffer) ReadUnsignedMedium() int {
	if b.pos+3 > len(b.data) {
		b.pos = len(b.data)
		return 0
	}
	v := int(b.data[b.pos])<<16 | int(b.data[b.pos+1])<<8 | int(b.data[b.pos+2])
	b.pos += 3
	return v
}

// ReadLong reads a big-endian int64.
func (b *Buffer) ReadLong() int64 {
	if b.pos+8 > len(b.data) {
		b.pos = len(b.data)
		return 0
	}
	v := int64(binary.BigEndian.Uint64(b.data[b.pos:]))
	b.pos += 8
	return v
}

// ReadSlice returns the next n bytes and advances the offset.
func (b *Buffer) ReadSlice(n int) []byte {
	if n <= 0 {
		return nil
	}
	if b.pos+n > len(b.data) {
		n = len(b.data) - b.pos
	}
	out := b.data[b.pos : b.pos+n]
	b.pos += n
	return out
}

// IndexOfBytes finds the first occurrence of needle at or after the reader
// index. It returns the absolute offset or -1.
func (b *Buffer) IndexOfBytes(needle []byte) int {
	if len(needle) == 0 {
		return -1
	}
	for i := b.pos; i+len(needle) <= len(b.data); i++ {
		match := true
		for j := range needle {
			if b.data[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// IndexOfByte finds the first occurrence of value at or after from. It returns
// the absolute offset or -1.
func (b *Buffer) IndexOfByte(from int, value byte) int {
	if from < 0 {
		from = 0
	}
	for i := from; i < len(b.data); i++ {
		if b.data[i] == value {
			return i
		}
	}
	return -1
}
