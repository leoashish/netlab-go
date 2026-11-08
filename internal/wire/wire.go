// Package wire converts between numbers and the byte sequences that actually travel on a network.
//
// A program sends bytes; an integer only becomes bytes when you choose their order. This package makes that
// choice explicit, and its functions are the primitives every later chapter builds on (Ch02 frames messages,
// Ch03 parses IP headers, and both read and write through these functions):
//
//   - Fixed-width integers read and write exactly N bytes.
//     Uint16BE/Uint32BE/Uint64BE interpret the FIRST N bytes of a slice as a big-endian number: the most
//     significant byte comes first. That is "network byte order" -- IP, TCP, UDP and DNS all use it.
//     Uint16LE and friends are its mirror image (least significant byte first), the order x86 and ARM CPUs
//     use natively. The Put* functions overwrite the first N bytes of dst and leave the rest alone.
//
//   - Varints encode an unsigned 64-bit number in LEB128 form (the same scheme protobuf uses): 7 value bits
//     per byte, most significant bit set = "another byte follows", least significant group first. Small
//     values stay short (127 is one byte) and a uint64 never needs more than 10 bytes.
//     ParseVarint decodes one value from the front of a slice and reports how many bytes it consumed, which
//     is how you walk a stream of concatenated values. Short input is io.ErrUnexpectedEOF; an encoding
//     that cannot fit in 64 bits is ErrVarintOverflow.
//
//   - Zig-zag turns signed numbers into unsigned ones before varint-encoding them ((v<<1)^(v>>63)):
//     0 -> 0, -1 -> 1, 1 -> 2, -2 -> 3, ... so small negatives stay small instead of becoming 10-byte
//     varints. ParseZigZag reverses both steps.
//
//   - HexDump renders bytes exactly like `hexdump -C`: the offset, 16 bytes per row as hex (8 + 8) and a
//     printable-ASCII gutter. It is the format you will stare at every time a packet misbehaves.
//
// Getters only read; Put* only overwrite bytes that are already in the slice. Nothing here allocates beyond
// what append must, and nothing here does I/O.
package wire

import (
	"errors"

	"netlab/internal/todo"
)

// ErrVarintOverflow reports varint input that cannot be a uint64: an encoding needing more than 10 bytes, or
// whose 10th byte carries value bits beyond bit 63.
var ErrVarintOverflow = errors.New("varint exceeds 64 bits")

// Uint16BE returns the first 2 bytes of b as a big-endian uint16 (b[0] is most significant).
// b must be at least 2 bytes long; a shorter slice is a programming error and panics like encoding/binary.
func Uint16BE(b []byte) uint16 {
	return 0 // TODO Ch01: b[0]<<8 | b[1]
}

// Uint16LE returns the first 2 bytes of b as a little-endian uint16 (b[0] is least significant).
// b must be at least 2 bytes long; a shorter slice is a programming error and panics like encoding/binary.
func Uint16LE(b []byte) uint16 {
	return 0 // TODO Ch01: b[0] | b[1]<<8
}

// Uint32BE returns the first 4 bytes of b as a big-endian uint32 (b[0] is most significant).
// b must be at least 4 bytes long; a shorter slice is a programming error and panics like encoding/binary.
func Uint32BE(b []byte) uint32 {
	return 0 // TODO Ch01
}

// Uint32LE returns the first 4 bytes of b as a little-endian uint32 (b[0] is least significant).
// b must be at least 4 bytes long; a shorter slice is a programming error and panics like encoding/binary.
func Uint32LE(b []byte) uint32 {
	return 0 // TODO Ch01
}

// Uint64BE returns the first 8 bytes of b as a big-endian uint64 (b[0] is most significant).
// b must be at least 8 bytes long; a shorter slice is a programming error and panics like encoding/binary.
func Uint64BE(b []byte) uint64 {
	return 0 // TODO Ch01
}

// Uint64LE returns the first 8 bytes of b as a little-endian uint64 (b[0] is least significant).
// b must be at least 8 bytes long; a shorter slice is a programming error and panics like encoding/binary.
func Uint64LE(b []byte) uint64 {
	return 0 // TODO Ch01
}

// PutUint16BE overwrites the first 2 bytes of dst with the big-endian encoding of v and leaves the rest of
// dst untouched. dst must be at least 2 bytes long.
func PutUint16BE(dst []byte, v uint16) {
	// TODO Ch01
}

// PutUint16LE overwrites the first 2 bytes of dst with the little-endian encoding of v and leaves the rest of
// dst untouched. dst must be at least 2 bytes long.
func PutUint16LE(dst []byte, v uint16) {
	// TODO Ch01
}

// PutUint32BE overwrites the first 4 bytes of dst with the big-endian encoding of v and leaves the rest of
// dst untouched. dst must be at least 4 bytes long.
func PutUint32BE(dst []byte, v uint32) {
	// TODO Ch01
}

// PutUint32LE overwrites the first 4 bytes of dst with the little-endian encoding of v and leaves the rest of
// dst untouched. dst must be at least 4 bytes long.
func PutUint32LE(dst []byte, v uint32) {
	// TODO Ch01
}

// PutUint64BE overwrites the first 8 bytes of dst with the big-endian encoding of v and leaves the rest of
// dst untouched. dst must be at least 8 bytes long.
func PutUint64BE(dst []byte, v uint64) {
	// TODO Ch01
}

// PutUint64LE overwrites the first 8 bytes of dst with the little-endian encoding of v and leaves the rest of
// dst untouched. dst must be at least 8 bytes long.
func PutUint64LE(dst []byte, v uint64) {
	// TODO Ch01
}

// AppendVarint appends the LEB128 varint encoding of v to dst and returns the extended slice.
// Seven value bits per byte, least significant group first; the byte's MSB is 1 when more bytes follow.
// The encoding is 1..10 bytes long; VarintLen(v) returns the exact length without building it.
func AppendVarint(dst []byte, v uint64) []byte {
	return dst // TODO Ch01
}

// VarintLen returns the exact number of bytes AppendVarint(dst, v) appends:
// 1 for v < 1<<7, 2 for v < 1<<14, ... up to 10 for v >= 1<<63.
func VarintLen(v uint64) int {
	return 0 // TODO Ch01
}

// ParseVarint decodes one varint from the front of b and returns the value and the number of bytes it
// consumed, n. Bytes after b[:n] belong to whatever follows the varint (another varint, a payload, ...).
//
// Errors:
//   - io.ErrUnexpectedEOF: b ends in the middle of a varint (the last byte read still had the continuation
//     bit set). More bytes could still complete the value, so a stream reader should read more and retry.
//   - ErrVarintOverflow: the encoding needs more than 10 bytes, or its 10th byte carries value bits beyond
//     bit 63. The input can never be a valid uint64.
//
// On any error v and n are zero.
func ParseVarint(b []byte) (v uint64, n int, err error) {
	return 0, 0, todo.NotImplemented("Ch01: ParseVarint")
}

// AppendZigZag maps the signed value v onto an unsigned one -- 0->0, -1->1, 1->2, -2->3, 2->4, ... -- and
// appends its varint encoding. The mapping is (v<<1)^(v>>63): it folds the sign into the low bit so that
// small-magnitude negatives stay short (without it, -1 would encode as ten bytes of 0xff...0x01).
func AppendZigZag(dst []byte, v int64) []byte {
	return dst // TODO Ch01
}

// ParseZigZag decodes one zig-zag varint from the front of b and returns the signed value and the number of
// bytes consumed, n. The errors are exactly ParseVarint's (io.ErrUnexpectedEOF, ErrVarintOverflow); on any
// error v and n are zero.
func ParseZigZag(b []byte) (v int64, n int, err error) {
	return 0, 0, todo.NotImplemented("Ch01: ParseZigZag")
}

// HexDump renders b the way `hexdump -C` does and returns it as a string. Empty input returns "".
//
// One line per 16 bytes:
//
//		00000000  47 45 54 20 2f 20 48 54  54 50 2f 31 2e 31 0d 0a  |GET / HTTP/1.1..|
//
//	  - offset: 8 lowercase hex digits, the byte offset of the row's first byte
//	  - hex area: each byte as two lowercase hex digits separated by single spaces, with one extra space
//	    after the 8th byte so the two halves are easy to tell apart; a short final row is padded with spaces
//	    so that every gutter starts in the same column
//	  - gutter: |...| with printable bytes (0x20..0x7e) shown as themselves and everything else -- including
//	    DEL, 0x7f -- shown as '.'
//
// Every line ends in '\n', the last one included.
func HexDump(b []byte) string {
	return "" // TODO Ch01
}
