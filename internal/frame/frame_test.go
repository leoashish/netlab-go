package frame_test

// Chapter 02 -- the internet checksum, CRC-32, and length-prefixed frames.
// Run: go test ./internal/frame -run Ch02 -race -v

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"testing"

	"netlab/internal/frame"
	"netlab/internal/testutil"
)

// ---------------------------------------------------------------- RFC 1071 internet checksum

func TestCh02_InternetChecksum_MatchesRFC1071Example(t *testing.T) {
	testutil.NoLeaks(t)
	data := []byte{0x00, 0x01, 0xf2, 0x03, 0xf4, 0xf5, 0xf6, 0xf7}
	// The worked example from RFC 1071: the words are 0001, f203, f4f5, f6f7.
	if got := frame.InternetChecksum(data); got != 0x220d {
		t.Fatalf("InternetChecksum(% x) = %#04x, want 0x220d (RFC 1071's example)", data, got)
	}
}

func TestCh02_InternetChecksum_OddLengthPadsWithZeroLowByte(t *testing.T) {
	testutil.NoLeaks(t)
	cases := []struct {
		name string
		in   []byte
		want uint16
	}{
		{"empty input sums to all ones, complemented to 0xffff", nil, 0xffff},
		{"single byte is the HIGH byte of the final word", []byte{0xff}, 0x00ff}, // word ff 00
		{"odd nibble-ish byte", []byte{0x12}, 0xedff},                            // word 12 00, ^0x1200
		{"the RFC example minus its last byte", []byte{0x00, 0x01, 0xf2, 0x03, 0xf4, 0xf5, 0xf6}, 0x2304},
		{"one complete word, nothing to fold", []byte{0x12, 0x34}, 0xedcb}, // ^0x1234
	}
	for _, tc := range cases {
		if got := frame.InternetChecksum(tc.in); got != tc.want {
			t.Errorf("%s: InternetChecksum(% x) = %#04x, want %#04x", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestCh02_InternetChecksum_EndAroundCarryWrapsIntoTheLowBit(t *testing.T) {
	testutil.NoLeaks(t)

	// 0xffff + 0x0001 overflows 16 bits; the carry must come back as +1, NOT be dropped.
	// sum (folded) = 0x0001, complement = 0xfffe. Forgetting the end-around carry gives 0xffff.
	if got := frame.InternetChecksum([]byte{0xff, 0xff, 0x00, 0x01}); got != 0xfffe {
		t.Fatalf("InternetChecksum(ff ff 00 01) = %#04x, want 0xfffe (the carry must re-enter at bit 0)", got)
	}

	// Eight 0xffff words: sum = 0x7fff8; every 16-bit fold adds the dropped 16-bit pattern back:
	// 0x7fff8 -> 0xffff, whose complement is 0x0000.
	if got := frame.InternetChecksum(bytes.Repeat([]byte{0xff}, 16)); got != 0x0000 {
		t.Fatalf("InternetChecksum(16 x 0xff) = %#04x, want 0x0000", got)
	}
}

func TestCh02_InternetChecksumVerify_RoundTripsAndDetectsACorruption(t *testing.T) {
	testutil.NoLeaks(t)

	// The packet from RFC 1071 with its checksum field filled in: the sum of every word is 0xffff,
	// whose complement is 0x0000.
	valid := []byte{0x00, 0x01, 0xf2, 0x03, 0xf4, 0xf5, 0xf6, 0xf7, 0x22, 0x0d}
	if !frame.InternetChecksumVerify(valid) {
		t.Fatal("InternetChecksumVerify(the RFC 1071 example packet) = false, want true")
	}

	// And a packet built from data X: append the checksum big-endian and the whole thing verifies.
	//
	// Padding subtlety (a classic gotcha): if len(X) is odd, the zero pad byte must go BETWEEN X and the
	// appended checksum, not after it. A checksum appended straight onto an odd byte count starts on an odd
	// offset, so the receiver would pair X's last byte with the checksum's high byte — two words the sender
	// never summed — and verification would fail. (When the checksum has a FIXED position in a packet, like
	// IPv4's header field, the layout keeps it word-aligned and the problem does not arise.)
	var packets [][]byte
	for _, data := range [][]byte{
		[]byte("netlab speaks in octets"),  // 23 bytes: odd
		[]byte("netlab speaks in octets!"), // 24 bytes: even
	} {
		cs := frame.InternetChecksum(data)
		packet := append([]byte{}, data...)
		if len(data)%2 == 1 {
			packet = append(packet, 0x00) // pad to the next 16-bit word boundary FIRST
		}
		packet = append(packet, byte(cs>>8), byte(cs))
		if !frame.InternetChecksumVerify(packet) {
			t.Fatalf("InternetChecksumVerify(%q + checksum %#04x) = false, want true", data, cs)
		}
		packets = append(packets, packet)
	}

	// Every single-bit corruption must be detected: a bit flip changes one 16-bit word by ±2^k,
	// which can never cancel itself out in a one's-complement sum.
	for _, packet := range packets {
		for i := range packet {
			corrupt := append([]byte{}, packet...)
			corrupt[i] ^= 0x01
			if frame.InternetChecksumVerify(corrupt) {
				t.Fatalf("a bit flip at byte %d was NOT detected", i)
			}
		}
	}
}

func TestCh02_InternetChecksum_BlindSpotsAreReal(t *testing.T) {
	testutil.NoLeaks(t)

	// Blind spot 1 — reordering: the one's-complement sum is commutative, so it CANNOT see 16-bit words
	// arrive in a different order (nor zero words inserted anywhere). Reordering on the internet is
	// caught by TCP sequence numbers, not by this checksum.
	data := []byte{0x45, 0x00, 0x00, 0x54, 0xab, 0xcd, 0xef, 0x01} // two words: 4500 0054 abcd ef01
	swapped := append([]byte{}, data...)
	swapped[0], swapped[2] = swapped[2], swapped[0]
	swapped[1], swapped[3] = swapped[3], swapped[1]

	if frame.InternetChecksum(data) != frame.InternetChecksum(swapped) {
		t.Fatal("reordered words produced a different checksum; the one's-complement sum must not notice what word arrived first")
	}
	// And therefore the reordered packet still verifies — the weakness is real, not theoretical.
	sum := frame.InternetChecksum(swapped)
	packet := append(append([]byte{}, swapped...), byte(sum>>8), byte(sum))
	if !frame.InternetChecksumVerify(packet) {
		t.Fatal("the payload with reordered words and its recomputed checksum must verify — that is exactly the weakness")
	}

	// Blind spot 2 — cancelling bit flips: +1 in one word and −1 in another cancel out. Both changes here
	// are SINGLE-bit flips (set the low bit of word 2, clear it in word 1) and the checksum sees nothing.
	intact := []byte{0x00, 0x01, 0x00, 0x00}   // words 0001, 0000
	tampered := []byte{0x00, 0x00, 0x00, 0x01} // words 0000, 0001: two bits flipped, same sum
	if frame.InternetChecksum(tampered) != frame.InternetChecksum(intact) {
		t.Fatal("cancelling bit flips must be invisible to a one's-complement sum — that is the known weakness")
	}
	sum = frame.InternetChecksum(tampered)
	packet = append(append([]byte{}, tampered...), byte(sum>>8), byte(sum))
	if !frame.InternetChecksumVerify(packet) {
		t.Fatal("the tampered payload with its checksum must still verify — two flipped bits slipped through")
	}
}

// ---------------------------------------------------------------- CRC-32 (IEEE)

func TestCh02_CRC32IEEE_MatchesKnownVectors(t *testing.T) {
	testutil.NoLeaks(t)
	cases := []struct {
		name string
		in   string
		want uint32
	}{
		{"empty string", "", 0x00000000},
		{"the standard CHECK vector", "123456789", 0xCBF43926},
		{"the quick brown fox", "The quick brown fox jumps over the lazy dog", 0x414FA339},
	}
	for _, tc := range cases {
		if got := frame.CRC32IEEE([]byte(tc.in)); got != tc.want {
			t.Errorf("%s: CRC32IEEE(%q) = %#08x, want %#08x", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestCh02_CRC32IEEE_MatchesStdlibOnRandomInputs(t *testing.T) {
	testutil.NoLeaks(t)

	// One fixed, non-empty case first so an accidentally-zero stub cannot fluke its way through.
	if got, want := frame.CRC32IEEE([]byte{0x01}), crc32.ChecksumIEEE([]byte{0x01}); got != want {
		t.Fatalf("CRC32IEEE([01]) = %#08x, hash/crc32 says %#08x", got, want)
	}

	// Deterministic random corpus, seeded: never flakes, and covers 0..300-byte inputs.
	rng := testutil.Rand(20260203)
	for i := 0; i < 200; i++ {
		buf := make([]byte, rng.Intn(300))
		rng.Read(buf)
		if got, want := frame.CRC32IEEE(buf), crc32.ChecksumIEEE(buf); got != want {
			t.Fatalf("CRC32IEEE(% x) = %#08x, hash/crc32.ChecksumIEEE says %#08x", buf, got, want)
		}
	}
}

// ---------------------------------------------------------------- framing

func TestCh02_AppendFrame_WritesBigEndianLengthPrefix(t *testing.T) {
	testutil.NoLeaks(t)

	// Empty payload: a zero-length frame is exactly the four zero length bytes.
	if got := frame.AppendFrame(nil, nil); !bytes.Equal(got, []byte{0x00, 0x00, 0x00, 0x00}) {
		t.Fatalf("AppendFrame(nil, nil) = % x, want [00 00 00 00]", got)
	}

	// "abc": the length must be big-endian on the wire.
	if got := frame.AppendFrame(nil, []byte("abc")); !bytes.Equal(got, []byte{0x00, 0x00, 0x00, 0x03, 'a', 'b', 'c'}) {
		t.Fatalf("AppendFrame(nil, abc) = % x, want [00 00 00 03 61 62 63]", got)
	}

	// A length crossing into the second byte: 300 payload bytes -> 00 00 01 2c + payload.
	big := bytes.Repeat([]byte{0x5a}, 300)
	got := frame.AppendFrame(nil, big)
	want := append([]byte{0x00, 0x00, 0x01, 0x2c}, big...)
	if !bytes.Equal(got, want) {
		prefix := got
		if len(prefix) > 4 {
			prefix = prefix[:4]
		}
		t.Fatalf("AppendFrame(300 bytes): got prefix % x and len %d, want prefix 00 00 01 2c and len %d", prefix, len(got), len(want))
	}
}

func TestCh02_AppendFrame_AppendsToExistingSlice(t *testing.T) {
	testutil.NoLeaks(t)

	// Reuse of a caller-provided dst must work: nothing before the prefix may be touched.
	dst := []byte("PRE")
	got := frame.AppendFrame(dst, []byte{0xde, 0xad})
	want := append([]byte("PRE"), 0x00, 0x00, 0x00, 0x02, 0xde, 0xad)
	if !bytes.Equal(got, want) {
		t.Fatalf("AppendFrame(dst, 02 dead) = % x, want % x", got, want)
	}
}

func TestCh02_ParseFrame_RoundTripsEverySize(t *testing.T) {
	testutil.NoLeaks(t)

	for _, size := range []int{0, 1, 2, 3, 255, 256, 4095, 65535, 65536} {
		payload := make([]byte, size)
		for i := range payload {
			payload[i] = byte(i*7 + size)
		}

		f := frame.AppendFrame(nil, payload)
		got, n, err := frame.ParseFrame(f)

		if err != nil {
			t.Fatalf("size %d: ParseFrame: %v", size, err)
		}
		if n != len(f) {
			t.Fatalf("size %d: n = %d, want %d (4 + %d)", size, n, len(f), size)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("size %d: payload mismatch (got %d bytes)", size, len(got))
		}
	}
}

func TestCh02_ParseFrame_ConsumesExactlyOneFrame(t *testing.T) {
	testutil.NoLeaks(t)

	// A concatenation of frames: ParseFrame must stop cleanly at the end of the first one.
	payloads := [][]byte{[]byte("first"), {}, bytes.Repeat([]byte{0x0f}, 1000), []byte("last")}
	var buf []byte
	for _, p := range payloads {
		buf = frame.AppendFrame(buf, p)
	}
	for i, want := range payloads {
		got, n, err := frame.ParseFrame(buf)
		if err != nil {
			t.Fatalf("frame %d: ParseFrame: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("frame %d: payload = % x, want % x", i, got, want)
		}
		if n != 4+len(want) {
			t.Fatalf("frame %d: n = %d, want %d", i, n, 4+len(want))
		}
		buf = buf[n:]
	}
	if len(buf) != 0 {
		t.Fatalf("%d bytes left after all frames were parsed", len(buf))
	}
}

func TestCh02_ParseFrame_RejectsDeclaredLengthOverTheCap(t *testing.T) {
	testutil.NoLeaks(t)

	// The check must happen on the length field ALONE, before a single payload byte is needed:
	// hand it four bytes declaring 0xffffffff and nothing else.
	buf := []byte{0xff, 0xff, 0xff, 0xff}
	_, _, err := frame.ParseFrame(buf)
	testutil.ExpectError(t, err)
	if !errors.Is(err, frame.ErrFrameTooLarge) {
		t.Fatalf("ParseFrame(declaring 4 GiB) = %v, want ErrFrameTooLarge", err)
	}

	// One byte over the cap, no payload present at all — again rejected on the declaration alone.
	just := make([]byte, 4)
	binary.BigEndian.PutUint32(just, frame.DefaultMaxFrame+1)
	_, _, err = frame.ParseFrame(just)
	testutil.ExpectError(t, err)
	if !errors.Is(err, frame.ErrFrameTooLarge) {
		t.Fatalf("ParseFrame(declaring %d) = %v, want ErrFrameTooLarge", frame.DefaultMaxFrame+1, err)
	}

	// EXACTLY at the cap is allowed: it fails on truncation (all we gave it is 14 bytes),
	// never on the size check itself.
	atCap := make([]byte, 4+10)
	binary.BigEndian.PutUint32(atCap, frame.DefaultMaxFrame)
	_, _, err = frame.ParseFrame(atCap)
	testutil.ExpectError(t, err)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("ParseFrame(declaring exactly the cap, truncated) = %v, want io.ErrUnexpectedEOF", err)
	}
}

func TestCh02_ParseFrame_TruncatedInputIsUnexpectedEOF(t *testing.T) {
	testutil.NoLeaks(t)

	cases := []struct {
		name string
		in   []byte
	}{
		{"empty buffer", nil},
		{"partial header (1 byte)", []byte{0x00}},
		{"partial header (2 bytes)", []byte{0x00, 0x00}},
		{"partial header (3 bytes)", []byte{0x00, 0x00, 0x00}},
		{"full header, zero payload", []byte{0x00, 0x00, 0x00, 0x05}},
		{"full header, payload cut short", []byte{0x00, 0x00, 0x00, 0x05, 'a', 'b'}},
	}
	for _, tc := range cases {
		_, _, err := frame.ParseFrame(tc.in)
		testutil.ExpectError(t, err)
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("%s: ParseFrame = %v, want io.ErrUnexpectedEOF", tc.name, err)
		}
	}

	// Control: a complete buffer parses with no error (so the failures above are about truncation, not parsing).
	if _, n, err := frame.ParseFrame(frame.AppendFrame(nil, []byte("ok"))); err != nil || n != 6 {
		t.Fatalf("ParseFrame(complete frame) = (n=%d, err=%v), want (6, nil)", n, err)
	}
}

// oneByteReader is the worst possible underlying reader: it delivers exactly one byte per Read call.
type oneByteReader struct{ b []byte }

func (r *oneByteReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	p[0] = r.b[0]
	r.b = r.b[1:]
	return 1, nil
}

// failingReader always fails its Read, like a dead connection.
type failingReader struct{ err error }

func (r failingReader) Read(p []byte) (int, error) { return 0, r.err }

func TestCh02_FrameReader_ReadsConsecutiveFrames_ThenCleanEOF(t *testing.T) {
	testutil.NoLeaks(t)

	payloads := [][]byte{
		{}, // empty frame first: the zero-length case
		[]byte("one"),
		[]byte("two-two"),
		bytes.Repeat([]byte{0xab}, 4096),
	}
	var stream []byte
	for _, p := range payloads {
		stream = frame.AppendFrame(stream, p)
	}

	r := frame.NewFrameReader(bytes.NewReader(stream), 0)
	for i, want := range payloads {
		got, err := r.ReadFrame()
		if err != nil {
			t.Fatalf("frame %d: ReadFrame: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("frame %d: got % x..., want % x...", i, got[:min(8, len(got))], want[:min(8, len(want))])
		}
	}
	if _, err := r.ReadFrame(); !errors.Is(err, io.EOF) {
		t.Fatalf("after the last frame: ReadFrame = %v, want io.EOF (a clean frame boundary)", err)
	}
}

func TestCh02_FrameReader_SurvivesOneByteAtATimeReads(t *testing.T) {
	testutil.NoLeaks(t)

	want := bytes.Repeat([]byte("fragment me"), 40) // 440 bytes, not a multiple of anything helpful
	stream := frame.AppendFrame(nil, want)
	r := frame.NewFrameReader(&oneByteReader{b: stream}, 0)

	got, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("frame arrived fragmented and did not reassemble: got %d bytes, want %d", len(got), len(want))
	}
	if _, err := r.ReadFrame(); !errors.Is(err, io.EOF) {
		t.Fatalf("after the frame: ReadFrame = %v, want io.EOF", err)
	}
}

func TestCh02_FrameReader_TruncatedStreamIsUnexpectedEOF_NotCleanEOF(t *testing.T) {
	testutil.NoLeaks(t)

	// Built by hand, not with AppendFrame, so this test exercises the reader alone.
	full := append([]byte{0x00, 0x00, 0x00, 0x0b}, "hello world"...) // 15 bytes: 4-byte header + 11-byte payload
	cases := []struct {
		name string
		b    []byte
	}{
		{"cut inside the header", full[:2]},
		{"cut immediately after the header", full[:4]}, // THE tricky one: peer closed, zero payload bytes read
		{"cut inside the payload", full[:11]},
	}
	for _, tc := range cases {
		_, err := frame.NewFrameReader(bytes.NewReader(tc.b), 0).ReadFrame()
		testutil.ExpectError(t, err)
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("%s: ReadFrame = %v, want io.ErrUnexpectedEOF (mid-frame truncation must not look like a boundary)", tc.name, err)
		}
	}
}

func TestCh02_FrameReader_EmptyStreamIsCleanEOF_AndFailuresPassThrough(t *testing.T) {
	testutil.NoLeaks(t)

	// No bytes at all: io.EOF — a clean boundary.
	r := frame.NewFrameReader(bytes.NewReader(nil), 0)
	if _, err := r.ReadFrame(); !errors.Is(err, io.EOF) {
		t.Fatalf("ReadFrame(empty) = %v, want io.EOF", err)
	}

	// An I/O error that is not EOF must surface unchanged.
	sentinel := errors.New("connection reset by peer")
	r2 := frame.NewFrameReader(failingReader{sentinel}, 0)
	if _, err := r2.ReadFrame(); !errors.Is(err, sentinel) {
		t.Fatalf("ReadFrame(dead connection) = %v, want the original error %q", err, sentinel)
	}
}

func TestCh02_FrameReader_EnforcesItsMaxBeforeAllocating(t *testing.T) {
	testutil.NoLeaks(t)

	// A small custom cap: a frame of exactly 5 bytes fits, 6 does not. First the good frame.
	okStream := frame.AppendFrame(nil, []byte("12345"))
	got, err := frame.NewFrameReader(bytes.NewReader(okStream), 5).ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame(5 bytes, max 5): %v", err)
	}
	if string(got) != "12345" {
		t.Fatalf("ReadFrame(max 5) = %q, want %q", got, "12345")
	}

	// Then a header ALONE declaring 6 bytes: rejected on the declaration, with no payload needed.
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], 6)
	_, err = frame.NewFrameReader(bytes.NewReader(hdr[:]), 5).ReadFrame()
	testutil.ExpectError(t, err)
	if !errors.Is(err, frame.ErrFrameTooLarge) {
		t.Fatalf("ReadFrame(declaring 6, max 5) = %v, want ErrFrameTooLarge", err)
	}

	// max == 0 means DefaultMaxFrame: a lying header claiming more than 16 MiB is refused
	// out of hand, without a 16 MiB allocation or read.
	binary.BigEndian.PutUint32(hdr[:], frame.DefaultMaxFrame+1)
	_, err = frame.NewFrameReader(bytes.NewReader(hdr[:]), 0).ReadFrame()
	testutil.ExpectError(t, err)
	if !errors.Is(err, frame.ErrFrameTooLarge) {
		t.Fatalf("ReadFrame(declaring %d, max 0 = DefaultMaxFrame) = %v, want ErrFrameTooLarge", frame.DefaultMaxFrame+1, err)
	}
}

// ---------------------------------------------------------------- the writer

func TestCh02_FrameWriter_WritesLengthPrefixedFrames(t *testing.T) {
	testutil.NoLeaks(t)

	payloads := [][]byte{
		[]byte("alpha"),
		nil,                             // empty frame: exactly [00 00 00 00]
		bytes.Repeat([]byte{0x00}, 300), // length crosses into the second byte
	}

	var buf bytes.Buffer
	w := frame.NewFrameWriter(&buf)
	for _, p := range payloads {
		if err := w.WriteFrame(p); err != nil {
			t.Fatalf("WriteFrame(%d bytes): %v", len(p), err)
		}
	}

	// Build the expected bytes by hand — NOT via AppendFrame, so a broken encoder cannot agree with itself.
	want := []byte{0x00, 0x00, 0x00, 0x05}
	want = append(want, "alpha"...)
	want = append(want, 0x00, 0x00, 0x00, 0x00)
	want = append(want, 0x00, 0x00, 0x01, 0x2c)
	want = append(want, bytes.Repeat([]byte{0x00}, 300)...)
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("FrameWriter produced wrong bytes:\ngot  % x\nwant % x", buf.Bytes(), want)
	}
}

func TestCh02_FrameWriter_RejectsPayloadOverTheCap_AndWritesNothing(t *testing.T) {
	testutil.NoLeaks(t)

	var buf bytes.Buffer
	w := frame.NewFrameWriter(&buf)
	oversized := make([]byte, frame.DefaultMaxFrame+1)

	err := w.WriteFrame(oversized)
	testutil.ExpectError(t, err)
	if !errors.Is(err, frame.ErrFrameTooLarge) {
		t.Fatalf("WriteFrame(%d bytes) = %v, want ErrFrameTooLarge", len(oversized), err)
	}
	if buf.Len() != 0 {
		t.Fatalf("a rejected frame wrote %d bytes to w; it must write nothing at all", buf.Len())
	}

	// At the cap is allowed: we only check it does not error (io.Discard sinks the 16 MiB).
	if err := frame.NewFrameWriter(io.Discard).WriteFrame(make([]byte, frame.DefaultMaxFrame)); err != nil {
		t.Fatalf("WriteFrame(exactly DefaultMaxFrame): %v, want nil", err)
	}
}

// ---------------------------------------------------------------- end to end

func TestCh02_EndToEnd_HundredRandomFramesSurviveTheRoundTrip(t *testing.T) {
	testutil.NoLeaks(t)

	const count = 100
	rng := testutil.Rand(0xC0FFEE)
	payloads := make([][]byte, 0, count)

	var stream bytes.Buffer
	w := frame.NewFrameWriter(&stream)
	for i := 0; i < count; i++ {
		n := rng.Intn(3000)
		if i%25 == 24 {
			n = rng.Intn(1 << 17) // a couple of large frames to cross every width boundary
		}
		p := make([]byte, n)
		rng.Read(p)
		payloads = append(payloads, p)
		if err := w.WriteFrame(p); err != nil {
			t.Fatalf("frame %d: WriteFrame: %v", i, err)
		}
	}
	framedLen := 0
	for _, p := range payloads {
		framedLen += 4 + len(p)
	}
	if stream.Len() != framedLen {
		t.Fatalf("the stream holds %d bytes, want exactly %d (no trailing junk, no loss)", stream.Len(), framedLen)
	}

	r := frame.NewFrameReader(bytes.NewReader(stream.Bytes()), 0)
	for i, want := range payloads {
		got, err := r.ReadFrame()
		if err != nil {
			t.Fatalf("frame %d: ReadFrame: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("frame %d: payload changed in transit (%d → %d bytes)", i, len(want), len(got))
		}
	}
	if _, err := r.ReadFrame(); !errors.Is(err, io.EOF) {
		t.Fatalf("after 100 frames: ReadFrame = %v, want io.EOF", err)
	}
}
