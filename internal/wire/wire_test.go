package wire_test

import (
	"bytes"
	"errors"
	"io"
	"math"
	"strings"
	"testing"

	"netlab/internal/testutil"
	"netlab/internal/wire"
)

// Chapter 01 -- bytes on the wire: endianness, varints and hexdumps.
// Run: go test ./internal/wire -run Ch01 -race -v

// ---------------------------------------------------------------------------
// Fixed-width integers
// ---------------------------------------------------------------------------

func TestCh01_Uint16_ReadsFirstTwoBytesInBothOrders(t *testing.T) {
	b := []byte{0x01, 0x02, 0xAA, 0xBB} // the extra bytes must be ignored
	if got := wire.Uint16BE(b); got != 0x0102 {
		t.Fatalf("Uint16BE(% x) = 0x%04x, want 0x0102", b, got)
	}
	if got := wire.Uint16LE(b); got != 0x0201 {
		t.Fatalf("Uint16LE(% x) = 0x%04x, want 0x0201", b, got)
	}
	if got := wire.Uint16BE([]byte{0xff, 0xff}); got != 0xffff {
		t.Fatalf("Uint16BE(ff ff) = 0x%04x, want 0xffff", got)
	}
}

func TestCh01_Uint32_ReadsFirstFourBytesInBothOrders(t *testing.T) {
	b := []byte{0x01, 0x02, 0x03, 0x04, 0xAA} // 0x01020304 as it appears on the wire
	if got := wire.Uint32BE(b); got != 0x01020304 {
		t.Fatalf("Uint32BE(% x) = 0x%08x, want 0x01020304", b, got)
	}
	if got := wire.Uint32LE(b); got != 0x04030201 {
		t.Fatalf("Uint32LE(% x) = 0x%08x, want 0x04030201", b, got)
	}
	if got := wire.Uint32BE([]byte{0xff, 0xff, 0xff, 0xff}); got != math.MaxUint32 {
		t.Fatalf("Uint32BE(ff ff ff ff) = 0x%08x, want 0xffffffff", got)
	}
}

func TestCh01_Uint64_ReadsFirstEightBytesInBothOrders(t *testing.T) {
	b := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0xAA}
	if got := wire.Uint64BE(b); got != 0x0102030405060708 {
		t.Fatalf("Uint64BE(% x) = 0x%016x, want 0x0102030405060708", b, got)
	}
	if got := wire.Uint64LE(b); got != 0x0807060504030201 {
		t.Fatalf("Uint64LE(% x) = 0x%016x, want 0x0807060504030201", b, got)
	}
	if got := wire.Uint64BE([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}); got != math.MaxUint64 {
		t.Fatalf("Uint64BE(ff ...) = 0x%016x, want 0xffffffffffffffff", got)
	}
}

func TestCh01_PutUint16_WritesFirstTwoBytesOnly(t *testing.T) {
	dst := []byte{0xAA, 0xAA, 0xAA, 0xAA}
	wire.PutUint16BE(dst, 0x0102)
	if want := []byte{0x01, 0x02, 0xAA, 0xAA}; !bytes.Equal(dst, want) {
		t.Fatalf("PutUint16BE left % x, want % x", dst, want)
	}
	wire.PutUint16LE(dst, 0x0102)
	if want := []byte{0x02, 0x01, 0xAA, 0xAA}; !bytes.Equal(dst, want) {
		t.Fatalf("PutUint16LE left % x, want % x", dst, want)
	}
	wire.PutUint16BE(dst, 0)
	if want := []byte{0x00, 0x00, 0xAA, 0xAA}; !bytes.Equal(dst, want) {
		t.Fatalf("PutUint16BE(dst, 0) left % x, want % x", dst, want)
	}
}

func TestCh01_PutUint32_WritesFirstFourBytesOnly(t *testing.T) {
	dst := []byte{0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA}
	wire.PutUint32BE(dst, 0x01020304)
	if want := []byte{0x01, 0x02, 0x03, 0x04, 0xAA, 0xAA}; !bytes.Equal(dst, want) {
		t.Fatalf("PutUint32BE left % x, want % x", dst, want)
	}
	wire.PutUint32LE(dst, 0x01020304)
	if want := []byte{0x04, 0x03, 0x02, 0x01, 0xAA, 0xAA}; !bytes.Equal(dst, want) {
		t.Fatalf("PutUint32LE left % x, want % x", dst, want)
	}
}

func TestCh01_PutUint64_WritesFirstEightBytesOnly(t *testing.T) {
	dst := []byte{0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA}
	wire.PutUint64BE(dst, 0x0102030405060708)
	if want := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0xAA, 0xAA}; !bytes.Equal(dst, want) {
		t.Fatalf("PutUint64BE left % x, want % x", dst, want)
	}
	wire.PutUint64LE(dst, 0x0102030405060708)
	if want := []byte{0x08, 0x07, 0x06, 0x05, 0x04, 0x03, 0x02, 0x01, 0xAA, 0xAA}; !bytes.Equal(dst, want) {
		t.Fatalf("PutUint64LE left % x, want % x", dst, want)
	}
}

func TestCh01_FixedWidth_RoundTrips_AcrossWidthsAndValues(t *testing.T) {
	cases := []struct {
		name  string
		width int
		v     uint64
	}{
		{"u16/0x0001", 2, 0x0001},
		{"u16/0x0102", 2, 0x0102},
		{"u16/0xffff", 2, 0xffff},
		{"u32/0x01020304", 4, 0x01020304},
		{"u32/0xdeadbeef", 4, 0xdeadbeef},
		{"u32/0xffffffff", 4, 0xffffffff},
		{"u64/0x0102030405060708", 8, 0x0102030405060708},
		{"u64/0xdeadbeefcafebabe", 8, 0xdeadbeefcafebabe},
		{"u64/max", 8, math.MaxUint64},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			poison := func() []byte {
				dst := make([]byte, tc.width+2) // 2 sentinel bytes past the value
				for i := range dst {
					dst[i] = 0xAA
				}
				return dst
			}
			be, le := poison(), poison()

			switch tc.width {
			case 2:
				wire.PutUint16BE(be, uint16(tc.v))
				if got := wire.Uint16BE(be); got != uint16(tc.v) {
					t.Fatalf("BE round-trip of 0x%04x gave 0x%04x", tc.v, got)
				}
				wire.PutUint16LE(le, uint16(tc.v))
				if got := wire.Uint16LE(le); got != uint16(tc.v) {
					t.Fatalf("LE round-trip of 0x%04x gave 0x%04x", tc.v, got)
				}
			case 4:
				wire.PutUint32BE(be, uint32(tc.v))
				if got := wire.Uint32BE(be); got != uint32(tc.v) {
					t.Fatalf("BE round-trip of 0x%08x gave 0x%08x", tc.v, got)
				}
				wire.PutUint32LE(le, uint32(tc.v))
				if got := wire.Uint32LE(le); got != uint32(tc.v) {
					t.Fatalf("LE round-trip of 0x%08x gave 0x%08x", tc.v, got)
				}
			case 8:
				wire.PutUint64BE(be, tc.v)
				if got := wire.Uint64BE(be); got != tc.v {
					t.Fatalf("BE round-trip of 0x%016x gave 0x%016x", tc.v, got)
				}
				wire.PutUint64LE(le, tc.v)
				if got := wire.Uint64LE(le); got != tc.v {
					t.Fatalf("LE round-trip of 0x%016x gave 0x%016x", tc.v, got)
				}
			default:
				t.Fatalf("bad test case width %d", tc.width)
			}

			for i := tc.width; i < len(be); i++ {
				if be[i] != 0xAA || le[i] != 0xAA {
					t.Fatalf("a putter wrote past the first %d bytes: be=% x le=% x", tc.width, be, le)
				}
			}
			// Encoding the same value BE and LE produces byte-reversed buffers.
			for i := 0; i < tc.width; i++ {
				if be[i] != le[tc.width-1-i] {
					t.Fatalf("BE % x and LE % x are not byte-reverses", be, le)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Varints (LEB128)
// ---------------------------------------------------------------------------

func TestCh01_Varint_KnownVectors(t *testing.T) {
	cases := []struct {
		v    uint64
		want []byte
	}{
		{0, []byte{0x00}},
		{1, []byte{0x01}},
		{127, []byte{0x7f}},
		{128, []byte{0x80, 0x01}},
		{300, []byte{0xac, 0x02}}, // the prerequisite's worked example
		{16383, []byte{0xff, 0x7f}},
		{16384, []byte{0x80, 0x80, 0x01}},
		{1 << 63, []byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x01}},
		// MaxUint64: nine 0xff bytes then a final 0x01. (LEB128 stores the least
		// significant group first, so the 0x01 -- bit 63 -- comes LAST.)
		{math.MaxUint64, []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01}},
	}
	for _, tc := range cases {
		got := wire.AppendVarint(nil, tc.v)
		if !bytes.Equal(got, tc.want) {
			t.Fatalf("AppendVarint(nil, 0x%x) = % x, want % x", tc.v, got, tc.want)
		}
		v, n, err := wire.ParseVarint(got)
		if err != nil {
			t.Fatalf("ParseVarint(% x) = error %v, want nil", got, err)
		}
		if v != tc.v || n != len(tc.want) {
			t.Fatalf("ParseVarint(% x) = (0x%x, %d), want (0x%x, %d)", got, v, n, tc.v, len(tc.want))
		}
	}
	if got := len(wire.AppendVarint(nil, math.MaxUint64)); got != 10 {
		t.Fatalf("max uint64 encoded in %d bytes, want 10", got)
	}
	if got := wire.AppendVarint(nil, math.MaxUint64); got[0] != 0xff || got[9] != 0x01 {
		t.Fatalf("max uint64 = % x, want first byte 0xff and last byte 0x01", got)
	}
}

func TestCh01_VarintLen_MatchesTheEncoding(t *testing.T) {
	// Exact boundary lengths: 7 bits per byte.
	bounds := []struct {
		v    uint64
		want int
	}{
		{0, 1},
		{127, 1},
		{128, 2},
		{16383, 2},
		{16384, 3},
		{1<<63 - 1, 9},
		{1 << 63, 10},
		{math.MaxUint64, 10},
	}
	for _, tc := range bounds {
		if got := wire.VarintLen(tc.v); got != tc.want {
			t.Fatalf("VarintLen(0x%x) = %d, want %d", tc.v, got, tc.want)
		}
	}

	// And it must agree with actually encoding, over a deterministic spread.
	values := []uint64{0, 1, 127, 128, 300, 16384, 1<<32 - 1, 1 << 63, math.MaxUint64}
	r := testutil.Rand(1)
	for i := 0; i < 500; i++ {
		width := r.Intn(65) // 0..64 significant bits
		v := r.Uint64()
		if width < 64 {
			v &= 1<<uint(width) - 1
		}
		values = append(values, v)
	}
	for _, v := range values {
		if got, want := wire.VarintLen(v), len(wire.AppendVarint(nil, v)); got != want {
			t.Fatalf("VarintLen(0x%x) = %d but AppendVarint produced %d bytes", v, got, want)
		}
	}
}

func TestCh01_Varint_RoundTrips_OverASpreadOfValues(t *testing.T) {
	values := []uint64{0, 1, 127, 128, 300, 16384, 1<<32 - 1, 1 << 63, math.MaxUint64}
	r := testutil.Rand(7)
	for i := 0; i < 300; i++ {
		width := r.Intn(65)
		v := r.Uint64()
		if width < 64 {
			v &= 1<<uint(width) - 1
		}
		values = append(values, v)
	}

	for _, v := range values {
		enc := wire.AppendVarint(nil, v)
		got, n, err := wire.ParseVarint(enc)
		if err != nil {
			t.Fatalf("ParseVarint(AppendVarint(nil, 0x%x)) = error %v", v, err)
		}
		if got != v || n != len(enc) {
			t.Fatalf("round-trip of 0x%x = (0x%x, %d bytes); encoding was % x (%d bytes)", v, got, n, enc, len(enc))
		}
	}
}

func TestCh01_Varint_ConcatenatedValues_ParseBackInOrder(t *testing.T) {
	// 0, 300, 16383, 128 written back to back, the way several fields arrive in one packet.
	stream := []byte{0x00, 0xac, 0x02, 0xff, 0x7f, 0x80, 0x01}
	want := []uint64{0, 300, 16383, 128}

	off := 0
	for i, w := range want {
		v, n, err := wire.ParseVarint(stream[off:])
		if err != nil {
			t.Fatalf("value %d: ParseVarint(% x) = error %v", i, stream[off:], err)
		}
		if v != w {
			t.Fatalf("value %d = 0x%x, want 0x%x", i, v, w)
		}
		if n <= 0 {
			t.Fatalf("value %d consumed %d bytes", i, n)
		}
		off += n
	}
	if off != len(stream) {
		t.Fatalf("parsing stopped at byte %d of %d", off, len(stream))
	}
}

func TestCh01_ParseVarint_ConsumesExactlyNAndLeavesTheRest(t *testing.T) {
	stream := []byte{0xac, 0x02, 0xde, 0xad}
	v, n, err := wire.ParseVarint(stream)
	if err != nil {
		t.Fatalf("ParseVarint(% x) = error %v, want nil", stream, err)
	}
	if v != 300 || n != 2 {
		t.Fatalf("ParseVarint(% x) = (0x%x, %d), want (300, 2)", stream, v, n)
	}
	if rest := stream[n:]; !bytes.Equal(rest, []byte{0xde, 0xad}) {
		t.Fatalf("after n=%d bytes the rest is % x, want de ad", n, rest)
	}
}

func TestCh01_ParseVarint_ShortInput_ReturnsUnexpectedEOF(t *testing.T) {
	inputs := [][]byte{
		{},     // empty
		{0x80}, // says "one more byte", none follows
		{0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80}, // 9 continuation bytes: a 10-byte varint needs a 10th
	}
	for _, in := range inputs {
		v, n, err := wire.ParseVarint(in)
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("ParseVarint(% x) = error %v, want io.ErrUnexpectedEOF", in, err)
		}
		if v != 0 || n != 0 {
			t.Fatalf("ParseVarint(% x) returned (%#x, %d) with an error, want (0, 0)", in, v, n)
		}
	}
}

func TestCh01_ParseVarint_Overflow(t *testing.T) {
	inputs := [][]byte{
		bytes.Repeat([]byte{0x80}, 11),              // 11 continuation bytes: needs more than 64 bits
		append(bytes.Repeat([]byte{0x80}, 9), 0x02), // 10th byte has value bit 64
		append(bytes.Repeat([]byte{0xff}, 9), 0x7f), // 10th byte has seven value bits, not one
		bytes.Repeat([]byte{0xff}, 10),              // 10th byte has continuation AND extra value bits
	}
	for _, in := range inputs {
		v, n, err := wire.ParseVarint(in)
		if !errors.Is(err, wire.ErrVarintOverflow) {
			t.Fatalf("ParseVarint(% x) = error %v, want ErrVarintOverflow", in, err)
		}
		if v != 0 || n != 0 {
			t.Fatalf("ParseVarint(% x) returned (%#x, %d) with an error, want (0, 0)", in, v, n)
		}
	}
	// The largest legal encoding, 9*0xff + 0x01 (= MaxUint64), must NOT overflow.
	if _, _, err := wire.ParseVarint([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01}); err != nil {
		t.Fatalf("ParseVarint(max uint64) = error %v, want nil", err)
	}
}

// ---------------------------------------------------------------------------
// Zig-zag
// ---------------------------------------------------------------------------

func TestCh01_ZigZag_KnownVectors(t *testing.T) {
	cases := []struct {
		v    int64
		want []byte
	}{
		{0, []byte{0x00}},
		{-1, []byte{0x01}},
		{1, []byte{0x02}},
		{-2, []byte{0x03}},
		{2, []byte{0x04}},
		{-64, []byte{0x7f}},
		{63, []byte{0x7e}},
		{64, []byte{0x80, 0x01}},
		{-65, []byte{0x81, 0x01}},
		{math.MaxInt64, []byte{0xfe, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01}},
		{math.MinInt64, []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01}},
	}
	for _, tc := range cases {
		got := wire.AppendZigZag(nil, tc.v)
		if !bytes.Equal(got, tc.want) {
			t.Fatalf("AppendZigZag(nil, %d) = % x, want % x", tc.v, got, tc.want)
		}
		v, n, err := wire.ParseZigZag(got)
		if err != nil {
			t.Fatalf("ParseZigZag(% x) = error %v, want nil", got, err)
		}
		if v != tc.v || n != len(got) {
			t.Fatalf("ParseZigZag(% x) = (%d, %d), want (%d, %d)", got, v, n, tc.v, len(got))
		}
	}
}

func TestCh01_ZigZag_RoundTrips_MinMaxAndSmallMagnitudes(t *testing.T) {
	values := []int64{
		0, -1, 1, -2, 2, -63, 63, -64, 64, -65, 65,
		-300, 300, -16384, 16384,
		math.MinInt32, math.MinInt32 + 1, math.MaxInt32, math.MinInt32 - 1, math.MaxInt32 + 1,
		math.MinInt64 + 1, math.MaxInt64, math.MinInt64,
	}
	for _, v := range values {
		enc := wire.AppendZigZag(nil, v)
		got, n, err := wire.ParseZigZag(enc)
		if err != nil {
			t.Fatalf("ParseZigZag(AppendZigZag(nil, %d)) = error %v", v, err)
		}
		if got != v || n != len(enc) {
			t.Fatalf("round-trip of %d = (%d, %d bytes); encoding was % x (%d bytes)", v, got, n, enc, len(enc))
		}
	}
}

func TestCh01_ZigZag_KeepsSmallNegativesShort(t *testing.T) {
	cases := []struct {
		v    int64
		want int
	}{
		{-1, 1},
		{1, 1},
		{-64, 1},
		{63, 1},
		{-65, 2},
		{64, 2},
		{math.MinInt64, 10},
	}
	for _, tc := range cases {
		if got := len(wire.AppendZigZag(nil, tc.v)); got != tc.want {
			t.Fatalf("len(AppendZigZag(nil, %d)) = %d, want %d", tc.v, got, tc.want)
		}
	}
	// Contrast: a plain varint of -1 (as a huge uint64) is ten bytes. That is exactly why zig-zag exists.
	if got := len(wire.AppendVarint(nil, math.MaxUint64)); got != 10 {
		t.Fatalf("len(AppendVarint(nil, math.MaxUint64)) = %d, want 10", got)
	}
}

func TestCh01_ZigZag_PropagatesVarintErrors(t *testing.T) {
	if _, _, err := wire.ParseZigZag([]byte{0x80}); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("ParseZigZag(80) = error %v, want io.ErrUnexpectedEOF", err)
	}
	if _, _, err := wire.ParseZigZag(bytes.Repeat([]byte{0xff}, 10)); !errors.Is(err, wire.ErrVarintOverflow) {
		t.Fatalf("ParseZigZag(ff x10) = error %v, want ErrVarintOverflow", err)
	}
}

// ---------------------------------------------------------------------------
// HexDump
// ---------------------------------------------------------------------------

func TestCh01_HexDump_EmptyAndShortInput(t *testing.T) {
	if got := wire.HexDump(nil); got != "" {
		t.Fatalf("HexDump(nil) = %q, want empty string", got)
	}
	if got := wire.HexDump([]byte{}); got != "" {
		t.Fatalf("HexDump(empty) = %q, want empty string", got)
	}
	want := "00000000  61 62 63                                          |abc|\n"
	if got := wire.HexDump([]byte("abc")); got != want {
		t.Fatalf("HexDump(\"abc\") =\n%q\nwant\n%q", got, want)
	}
}

func TestCh01_HexDump_ExactlyOneRow(t *testing.T) {
	got := wire.HexDump([]byte("GET / HTTP/1.1\r\n")) // exactly 16 bytes
	want := "00000000  47 45 54 20 2f 20 48 54  54 50 2f 31 2e 31 0d 0a  |GET / HTTP/1.1..|\n"
	if got != want {
		t.Fatalf("HexDump = %q, want %q", got, want)
	}
	if strings.Count(got, "\n") != 1 || !strings.HasSuffix(got, "\n") {
		t.Fatalf("HexDump must end every line with \\n; got %q", got)
	}
}

func TestCh01_HexDump_TwoRows_SecondOffsetIsTenHex(t *testing.T) {
	got := wire.HexDump([]byte("GET / HTTP/1.1\r\nHost: example.co")) // exactly 32 bytes
	want := "00000000  47 45 54 20 2f 20 48 54  54 50 2f 31 2e 31 0d 0a  |GET / HTTP/1.1..|\n" +
		"00000010  48 6f 73 74 3a 20 65 78  61 6d 70 6c 65 2e 63 6f  |Host: example.co|\n"
	if got != want {
		t.Fatalf("HexDump =\n%q\nwant\n%q", got, want)
	}
	if strings.Count(got, "\n") != 2 {
		t.Fatalf("HexDump of 32 bytes printed %d lines, want 2", strings.Count(got, "\n"))
	}
}

func TestCh01_HexDump_NonPrintableBytesBecomeDots(t *testing.T) {
	got := wire.HexDump([]byte{0x00, 0x0a, 0x20, 0x7e, 0x7f, 0xff})
	want := "00000000  00 0a 20 7e 7f ff                                 |.. ~..|\n"
	if got != want {
		t.Fatalf("HexDump =\n%q\nwant\n%q", got, want)
	}
}

func TestCh01_HexDump_RowsAreAligned_OffsetsAreLowercase_TrailingNewline(t *testing.T) {
	got := wire.HexDump([]byte("0123456789abcdef0123456789abcdef!")) // 33 bytes -> 3 rows
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("HexDump of 33 bytes printed %d lines, want 3:\n%s", len(lines), got)
	}
	prefixes := []string{"00000000  ", "00000010  ", "00000020  "}
	for i, line := range lines {
		if !strings.HasPrefix(line, prefixes[i]) {
			t.Fatalf("line %d starts with %q, want prefix %q", i, line, prefixes[i])
		}
		if len(line) < 61 {
			t.Fatalf("line %d is only %d chars, too short to hold a gutter: %q", i, len(line), line)
		}
		if line[60] != '|' {
			t.Fatalf("line %d has its gutter at column %d, want 60: %q", i, strings.IndexByte(line, '|'), line)
		}
	}
	// The short last row pads the hex area so its gutter still starts at column 60.
	if !strings.HasSuffix(lines[2], "|!|") {
		t.Fatalf("last line = %q, want it to end with |!|", lines[2])
	}
	if !strings.HasSuffix(got, "\n") {
		t.Fatalf("HexDump must end with \\n, got %q", got)
	}
	if strings.Count(got, "\n") != len(lines) {
		t.Fatalf("got %d newlines for %d lines", strings.Count(got, "\n"), len(lines))
	}

	// Offsets beyond 0x9f must use lowercase hex digits, e.g. row 11 -> 000000a0.
	low := wire.HexDump(make([]byte, 0xAB))
	if !strings.Contains(low, "000000a0  ") {
		t.Fatalf("offset 0xa0 is not printed as lowercase %08x:\n%s", 0xa0, low)
	}
}
