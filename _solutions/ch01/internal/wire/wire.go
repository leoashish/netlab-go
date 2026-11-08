// Package wire converts between numbers and the byte sequences that actually travel on a network.
//
// This is the reference solution for Chapter 01. See internal/wire/wire.go for the full contract; the short
// version is: BE/LE getters read the first N bytes of a slice, Put* overwrite the first N bytes of dst,
// varints are protobuf-style LEB128 with a 10-byte maximum, zig-zag maps signed values onto unsigned ones,
// and HexDump prints `hexdump -C` output.
package wire

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrVarintOverflow reports varint input that cannot be a uint64: an encoding needing more than 10 bytes, or
// whose 10th byte carries value bits beyond bit 63.
var ErrVarintOverflow = errors.New("varint exceeds 64 bits")

// Uint16BE returns the first 2 bytes of b as a big-endian uint16 (b[0] is most significant).
func Uint16BE(b []byte) uint16 {
	return uint16(b[0])<<8 | uint16(b[1])
}

// Uint16LE returns the first 2 bytes of b as a little-endian uint16 (b[0] is least significant).
func Uint16LE(b []byte) uint16 {
	return uint16(b[0]) | uint16(b[1])<<8
}

// Uint32BE returns the first 4 bytes of b as a big-endian uint32 (b[0] is most significant).
func Uint32BE(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// Uint32LE returns the first 4 bytes of b as a little-endian uint32 (b[0] is least significant).
func Uint32LE(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

// Uint64BE returns the first 8 bytes of b as a big-endian uint64 (b[0] is most significant).
func Uint64BE(b []byte) uint64 {
	var v uint64
	for i := 0; i < 8; i++ {
		v = v<<8 | uint64(b[i])
	}
	return v
}

// Uint64LE returns the first 8 bytes of b as a little-endian uint64 (b[0] is least significant).
func Uint64LE(b []byte) uint64 {
	var v uint64
	for i := 7; i >= 0; i-- {
		v = v<<8 | uint64(b[i])
	}
	return v
}

// PutUint16BE overwrites the first 2 bytes of dst with the big-endian encoding of v.
func PutUint16BE(dst []byte, v uint16) {
	dst[0] = byte(v >> 8)
	dst[1] = byte(v)
}

// PutUint16LE overwrites the first 2 bytes of dst with the little-endian encoding of v.
func PutUint16LE(dst []byte, v uint16) {
	dst[0] = byte(v)
	dst[1] = byte(v >> 8)
}

// PutUint32BE overwrites the first 4 bytes of dst with the big-endian encoding of v.
func PutUint32BE(dst []byte, v uint32) {
	dst[0] = byte(v >> 24)
	dst[1] = byte(v >> 16)
	dst[2] = byte(v >> 8)
	dst[3] = byte(v)
}

// PutUint32LE overwrites the first 4 bytes of dst with the little-endian encoding of v.
func PutUint32LE(dst []byte, v uint32) {
	dst[0] = byte(v)
	dst[1] = byte(v >> 8)
	dst[2] = byte(v >> 16)
	dst[3] = byte(v >> 24)
}

// PutUint64BE overwrites the first 8 bytes of dst with the big-endian encoding of v.
func PutUint64BE(dst []byte, v uint64) {
	for i := 0; i < 8; i++ {
		dst[7-i] = byte(v >> (8 * uint(i)))
	}
}

// PutUint64LE overwrites the first 8 bytes of dst with the little-endian encoding of v.
func PutUint64LE(dst []byte, v uint64) {
	for i := 0; i < 8; i++ {
		dst[i] = byte(v >> (8 * uint(i)))
	}
}

// AppendVarint appends the LEB128 varint encoding of v to dst and returns the extended slice.
func AppendVarint(dst []byte, v uint64) []byte {
	for v >= 0x80 {
		dst = append(dst, byte(v)|0x80) // low 7 bits + continuation bit
		v >>= 7
	}
	return append(dst, byte(v)) // last byte: no continuation bit
}

// VarintLen returns the exact number of bytes AppendVarint(dst, v) appends.
func VarintLen(v uint64) int {
	n := 1
	for v >= 0x80 {
		v >>= 7
		n++
	}
	return n
}

// ParseVarint decodes one varint from the front of b and returns the value and the number of bytes consumed.
func ParseVarint(b []byte) (uint64, int, error) {
	var v uint64
	for i := 0; i < len(b); i++ {
		c := b[i]
		if i == 9 && c > 0x01 {
			// The 10th byte may only carry bit 63: anything else (including another
			// continuation bit) means the value does not fit in 64 bits.
			return 0, 0, ErrVarintOverflow
		}
		v |= uint64(c&0x7f) << (7 * uint(i))
		if c&0x80 == 0 {
			return v, i + 1, nil
		}
	}
	return 0, 0, io.ErrUnexpectedEOF
}

// AppendZigZag maps v onto an unsigned value ((v<<1)^(v>>63)) and appends its varint encoding.
func AppendZigZag(dst []byte, v int64) []byte {
	return AppendVarint(dst, uint64(v<<1)^uint64(v>>63))
}

// ParseZigZag decodes one zig-zag varint from the front of b.
func ParseZigZag(b []byte) (int64, int, error) {
	z, n, err := ParseVarint(b)
	if err != nil {
		return 0, 0, err
	}
	return int64(z>>1) ^ -int64(z&1), n, nil
}

// HexDump renders b the way `hexdump -C` does.
func HexDump(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	var sb strings.Builder
	for off := 0; off < len(b); off += 16 {
		end := off + 16
		if end > len(b) {
			end = len(b)
		}
		row := b[off:end]

		fmt.Fprintf(&sb, "%08x  ", off)
		for i := 0; i < 16; i++ {
			if i < len(row) {
				fmt.Fprintf(&sb, "%02x ", row[i])
			} else {
				sb.WriteString("   ") // pad: keep the gutter in the same column
			}
			if i == 7 {
				sb.WriteByte(' ') // extra space between the two 8-byte halves
			}
		}
		sb.WriteByte(' ') // one space before the gutter (hexdump -C style)
		sb.WriteByte('|')
		for _, c := range row {
			if c >= 0x20 && c <= 0x7e {
				sb.WriteByte(c)
			} else {
				sb.WriteByte('.')
			}
		}
		sb.WriteString("|\n")
	}
	return sb.String()
}
