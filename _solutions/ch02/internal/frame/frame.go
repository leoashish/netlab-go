// Package frame is the wire's two utilities: checksums (detect accidental corruption) and length-prefixed framing
// (recover message boundaries from a byte stream that doesn't have them).
//
// Checksums detect ACCIDENTS: a flipped bit on the wire, a torn write, bit rot. They are not MACs — an attacker can
// recompute them. If you need to know WHO sent the bytes, that is Chapter 12 (TLS), not this package.
package frame

import (
	"encoding/binary"
	"errors"
	"io"
)

// ErrFrameTooLarge is returned when a frame's declared (or actual) payload exceeds the allowed maximum. GIVEN.
var ErrFrameTooLarge = errors.New("frame exceeds the maximum size")

// DefaultMaxFrame is the default cap on a frame payload: 16 MiB. A corrupt or hostile length field must never make
// a reader allocate gigabytes before noticing the length is nonsense. GIVEN.
const DefaultMaxFrame uint32 = 16 << 20 // 16 MiB

// InternetChecksum computes the RFC 1071 internet checksum of b:
//
//   - view b as a sequence of big-endian 16-bit words (b[0],b[1] is a word; then b[2],b[3]; ...);
//   - if len(b) is odd, the LAST byte is the HIGH byte of a final word whose low byte is zero,
//     i.e. the word [b[len(b)-1], 0x00];
//   - add the words into a 32+ bit accumulator with END-AROUND CARRY: whenever a carry leaves bit 15,
//     it is added back into bit 0 (never discarded);
//   - finally return the ONE'S COMPLEMENT (~sum) of the folded 16-bit sum.
//
// Examples from the tests: {00 01 f2 03 f4 f5 f6 f7} gives 0x220d (the RFC 1071 worked example);
// {} gives 0xffff; {ff} gives 0x00ff.
//
// Used by IPv4, ICMP, TCP and UDP headers. It is a corruption detector for ACCIDENTS only. It is not a MAC: an
// attacker can fix it up trivially, and it cannot see some multi-bit errors (the chapter shows which).
func InternetChecksum(b []byte) uint16 {
	// Accumulate in uint64 so ordinary inputs cannot overflow before the fold; the fold itself
	// (sum = low16 + high bits) is repeated until only 16 bits remain. For a 16 MiB frame that
	// is at most 8 Mi words * 0xffff ≈ 2^35 — comfortably inside uint64.
	var sum uint64
	for len(b) >= 2 {
		sum += uint64(b[0])<<8 | uint64(b[1])
		b = b[2:]
	}
	if len(b) == 1 {
		// Odd length: the last byte is the HIGH byte; the low byte is the implicit zero pad.
		sum += uint64(b[0]) << 8
	}
	// End-around carry: every carry out of bit 15 re-enters at bit 0. Folding as
	// (low 16 bits) + (everything above) implements exactly that, however many times it takes.
	for sum > 0xffff {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}

// InternetChecksumVerify reports whether b — a packet WITH its checksum already filled in — has a valid checksum.
//
// The trick: the sender stores ~(sum of the words) in the checksum field. If the receiver sums ALL words including
// the checksum, the total is 0xffff, whose one's complement is 0x0000. So verifying needs no separate algorithm:
// b has a valid checksum iff InternetChecksum(b) == 0x0000.
func InternetChecksumVerify(b []byte) bool {
	return InternetChecksum(b) == 0x0000
}

// CRC32IEEE returns the CRC-32 (IEEE 802.3) of b: a big binary number - the message's bit pattern - divided by the
// generator polynomial x^32+x^26+x^23+x^22+x^16+x^12+x^11+x^10+x^8+x^7+x^5+x^4+x^2+x+1; the remainder is the CRC.
//
// Specs to match, exactly:
//
//   - reflected polynomial 0xEDB88320 (the IEEE polynomial with all its bits reversed, so a right-shifting register
//     implements it);
//   - register initialised to 0xFFFFFFFF;
//   - read each byte into the low byte of the register and run 8 rounds; a 1 bit shifts in 0xEDB88320 instead of 0;
//   - final result XORed with 0xFFFFFFFF.
//
// Implement it BITWISE from first principles - no lookup tables (Chapter 2's stretch adds the table). It must agree
// with hash/crc32.ChecksumIEEE on every input. Bitwise is O(8·len) and fast enough for frames.
func CRC32IEEE(b []byte) uint32 {
	crc := ^uint32(0) // init: all ones
	for _, v := range b {
		crc ^= uint32(v) // bring the byte into the register, low 8 bits
		for i := 0; i < 8; i++ {
			if crc&1 != 0 { // the bit shifted out is 1: subtract (XOR) the polynomial
				crc = crc>>1 ^ 0xEDB88320
			} else {
				crc >>= 1
			}
		}
	}
	return ^crc // final XOR: all ones
}

// AppendFrame appends one frame — a 4-byte big-endian length followed by payload — to dst and returns the extended
// slice, so `buf = frame.AppendFrame(buf, p)` reuses spare capacity instead of copying.
//
// AppendFrame has no error to report and does NOT enforce DefaultMaxFrame: it is the low-level encoder, the length
// field cannot describe a payload bigger than MaxUint32 bytes anyway, and the cap is enforced where it actually
// protects you — on the STREAMING side: FrameWriter refuses to send an oversized frame, and ParseFrame/FrameReader
// refuse to trust an oversized length field from the network. Callers buffering frames in memory before sending can
// check len(payload) against DefaultMaxFrame themselves if they need to.
func AppendFrame(dst []byte, payload []byte) []byte {
	size := uint32(len(payload))
	dst = append(dst, byte(size>>24), byte(size>>16), byte(size>>8), byte(size))
	return append(dst, payload...)
}

// ParseFrame parses ONE frame from the START of b — nothing else. n is the total number of bytes consumed
// (4 + declared length); call it repeatedly on the leftover b[n:] to walk a buffer of concatenated frames.
//
// The returned payload aliases b (it is b[4:n], no copy) — copy it if b may be reused afterwards.
//
// Errors, checked in this order:
//
//  1. len(b) < 4                       → io.ErrUnexpectedEOF   (a short buffer is not a stream boundary: the caller
//     fed us an incomplete frame, unlike a network stream which distinguishes EOF from truncation)
//  2. declared length > DefaultMaxFrame → ErrFrameTooLarge      (checked BEFORE trusting the length: a hostile 4 GiB
//     field must not make us slice, allocate or trust anything)
//  3. len(b) < 4 + declared            → io.ErrUnexpectedEOF   (the buffer holds only part of the frame)
//
// An empty payload is a legal frame (length 0).
func ParseFrame(b []byte) (payload []byte, n int, err error) {
	if len(b) < 4 {
		return nil, 0, io.ErrUnexpectedEOF
	}
	size := binary.BigEndian.Uint32(b[:4])
	if size > DefaultMaxFrame {
		return nil, 0, ErrFrameTooLarge
	}
	n = 4 + int(size) // size <= DefaultMaxFrame, so the conversion cannot overflow
	if len(b) < n {
		return nil, 0, io.ErrUnexpectedEOF
	}
	return b[4:n], n, nil
}

// FrameReader reads a stream of length-prefixed frames from an io.Reader — a TCP connection, a file, a
// bytes.Reader. Frames arrive one ReadFrame at a time no matter how the underlying reader chops its bytes: the
// reader loops until it has the whole header and then the whole payload. A source that yields one byte per Read
// call produces exactly the same frames (the tests feed it such an adversarial reader).
//
// max caps the declared payload size; 0 means DefaultMaxFrame. A frame whose declared size exceeds the cap is
// rejected with ErrFrameTooLarge BEFORE allocating a single byte of payload — the check against hostile length
// fields. After that error the stream is out of sync (we consumed only the 4 header bytes) and the reader is
// unusable; close the connection.
type FrameReader struct {
	r   io.Reader
	max uint32
}

// NewFrameReader returns a FrameReader over r. max is the largest payload it will accept; 0 means DefaultMaxFrame.
// GIVEN.
func NewFrameReader(r io.Reader, max uint32) *FrameReader {
	return &FrameReader{r: r, max: max}
}

// ReadFrame reads exactly one frame and returns its payload, freshly allocated, exactly len(payload) bytes.
//
// Error contract:
//
//   - io.EOF            — the stream ended cleanly BETWEEN frames; not one byte of a frame was read. End of input.
//   - io.ErrUnexpectedEOF — the stream ended in the MIDDLE of a frame (partial header, or the header was read but
//     the payload is short): the peer died or was cut off. Do NOT re-use the partial bytes.
//   - ErrFrameTooLarge  — the declared size exceeds max (only the header has been consumed; abandon the reader).
//   - any other error from the underlying reader, unchanged.
func (f *FrameReader) ReadFrame() ([]byte, error) {
	// io.ReadFull keeps calling Read until the buffer is full or an error arrives, so a stream that
	// delivers one byte per call is handled for free.
	var header [4]byte
	if _, err := io.ReadFull(f.r, header[:]); err != nil {
		// err is io.EOF only if ZERO header bytes were read — a clean frame boundary.
		// Any partial header comes back as io.ErrUnexpectedEOF already.
		return nil, err
	}

	size := binary.BigEndian.Uint32(header[:])
	max := f.max
	if max == 0 {
		max = DefaultMaxFrame
	}
	if size > max {
		// Refuse BEFORE make([]byte, size): the whole point of the cap is that a hostile length
		// field must not turn into a huge allocation (or a long read of bytes that never come).
		return nil, ErrFrameTooLarge
	}

	payload := make([]byte, size) // allocates exactly the frame payload
	if _, err := io.ReadFull(f.r, payload); err != nil {
		if err == io.EOF {
			// The header declared payload bytes, so zero bytes read is a frame cut short,
			// not a clean boundary. io.ReadFull only reports io.EOF when it read nothing.
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return payload, nil
}

// FrameWriter writes length-prefixed frames to an io.Writer. It does no buffering of its own — wrap a
// bufio.Writer around it if you want to batch the two Writes of each frame.
//
// A frame is written as TWO sequential Writes (the 4-byte header, then the payload), not one Write: the reader may
// see a frame whose header arrived before its payload (e.g. if the machine dies in between), which is exactly the
// partial-frame case the reader's io.ErrUnexpectedEOF contract exists for.
type FrameWriter struct {
	w io.Writer
}

// NewFrameWriter returns a FrameWriter over w. GIVEN.
func NewFrameWriter(w io.Writer) *FrameWriter {
	return &FrameWriter{w: w}
}

// WriteFrame writes one frame: [4-byte big-endian length][payload].
//
// If len(payload) exceeds DefaultMaxFrame it returns ErrFrameTooLarge WITHOUT writing anything to w (no partial
// frame leaks onto the wire). Otherwise it writes the header and then the payload; any error from the underlying
// writer is returned unchanged, and a failed payload write leaves a partial frame on the wire (see FrameWriter).
func (f *FrameWriter) WriteFrame(payload []byte) error {
	if len(payload) > int(DefaultMaxFrame) {
		return ErrFrameTooLarge
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if _, err := f.w.Write(header[:]); err != nil {
		return err
	}
	if _, err := f.w.Write(payload); err != nil {
		return err
	}
	return nil
}
