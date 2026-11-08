package packet_test

import (
	"bytes"
	"errors"
	"testing"

	"netlab/internal/packet"
	"netlab/internal/testutil"
)

// Chapter 03 — Reading packets: IPv4 headers, CIDR math and longest-prefix routing.
// Run: go test ./internal/packet -run Ch03 -race -v
//
// The tests never touch the network. Golden bytes are pinned as hex so a regression in the checksum or the
// field layout cannot hide behind "the test built it the same way the code does".

// A real-ish header: TTL 64, TCP, ID 0x1c46, total length 0x003c (60 = 20 header + 40 payload).
// src 192.168.1.100 (c0 a8 01 64), dst 93.184.216.34 (5d b8 d8 22), checksum 0x668f (verified by hand).
var goldenHeader = []byte{0x45, 0x00, 0x00, 0x3c, 0x1c, 0x46, 0x00, 0x00, 0x40, 0x06, 0x66, 0x8f, 0xc0, 0xa8, 0x01, 0x64, 0x5d, 0xb8, 0xd8, 0x22}

const (
	addrLANClient  = uint32(0xC0A80164) // 192.168.1.100
	addrExampleCom = uint32(0x5DB8D822) // 93.184.216.34
	addrGateway    = uint32(0xC0A80101) // 192.168.1.1
)

func TestCh03_MarshalParse_RoundTrip(t *testing.T) {
	h := packet.IPv4Header{TotalLength: 20, Identification: 0xBEEF, TTL: 64, Protocol: packet.ProtocolUDP, SourceAddr: addrLANClient, DestAddr: addrGateway}

	b := h.Marshal()
	if len(b) != 20 {
		t.Fatalf("Marshal produced %d bytes, want 20", len(b))
	}
	if b[0] != 0x45 {
		t.Fatalf("first byte = 0x%02x, want 0x45 (version 4, IHL 5)", b[0])
	}

	got, headerLen, err := packet.ParseIPv4Header(b)
	if err != nil {
		t.Fatalf("ParseIPv4Header: %v", err)
	}
	if headerLen != 20 {
		t.Fatalf("headerLen = %d, want 20", headerLen)
	}
	if got != h {
		t.Fatalf("round trip changed the header:\ngot  %+v\nwant %+v", got, h)
	}
}

func TestCh03_MarshalParse_WithPayload(t *testing.T) {
	payload := bytes.Repeat([]byte{0xAB}, 40)
	h := packet.IPv4Header{TotalLength: uint16(20 + len(payload)), Identification: 0x0001, TTL: 32, Protocol: packet.ProtocolTCP, SourceAddr: addrLANClient, DestAddr: addrExampleCom}

	b := append(h.Marshal(), payload...)
	if len(b) != 60 {
		t.Fatalf("packet is %d bytes, want 60", len(b))
	}
	got, headerLen, err := packet.ParseIPv4Header(b)
	if err != nil {
		t.Fatalf("ParseIPv4Header: %v", err)
	}
	if headerLen != 20 {
		t.Fatalf("headerLen = %d, want 20 (payload starts there)", headerLen)
	}
	if got.TotalLength != 60 {
		t.Fatalf("TotalLength = %d, want 60", got.TotalLength)
	}
	if !bytes.Equal(b[headerLen:], payload) {
		t.Fatal("payload bytes were not left untouched after the header")
	}
}

func TestCh03_Parse_GoldenHeaderFromHexDump(t *testing.T) {
	pkt := append(append([]byte(nil), goldenHeader...), bytes.Repeat([]byte{0xAB}, 40)...)

	h, headerLen, err := packet.ParseIPv4Header(pkt)
	if err != nil {
		t.Fatalf("ParseIPv4Header(golden): %v", err)
	}
	if headerLen != 20 {
		t.Fatalf("headerLen = %d, want 20", headerLen)
	}
	want := packet.IPv4Header{TotalLength: 60, Identification: 0x1c46, TTL: 64, Protocol: packet.ProtocolTCP, SourceAddr: addrLANClient, DestAddr: addrExampleCom}
	if h != want {
		t.Fatalf("golden header mismatch:\ngot  %+v\nwant %+v", h, want)
	}
	// Marshal must reproduce the golden bytes EXACTLY, checksum included — pins the checksum position and value.
	if out := h.Marshal(); !bytes.Equal(out, goldenHeader) {
		t.Fatalf("Marshal(golden) = % x,\nwant            % x", out, goldenHeader)
	}
}

func TestCh03_Parse_RejectsBadVersion(t *testing.T) {
	b := packet.IPv4Header{TotalLength: 20, TTL: 64, Protocol: packet.ProtocolTCP}.Marshal()
	if len(b) != 20 {
		t.Fatalf("Marshal produced %d bytes, want 20", len(b))
	}
	b[0] = (b[0] & 0x0f) | 0x50 // version 5, IHL untouched — not a valid IPv4 header
	_, _, err := packet.ParseIPv4Header(b)
	testutil.ExpectError(t, err)
	if !errors.Is(err, packet.ErrInvalidIPv4Header) {
		t.Fatalf("err = %v, want it to wrap ErrInvalidIPv4Header", err)
	}
}

func TestCh03_Parse_RejectsIHLBelow5(t *testing.T) {
	b := packet.IPv4Header{TotalLength: 20, TTL: 64, Protocol: packet.ProtocolTCP}.Marshal()
	if len(b) != 20 {
		t.Fatalf("Marshal produced %d bytes, want 20", len(b))
	}
	b[0] = 0x44 // version 4, IHL 4 — cannot even fit the fixed fields
	_, _, err := packet.ParseIPv4Header(b)
	testutil.ExpectError(t, err)
	if !errors.Is(err, packet.ErrInvalidIPv4Header) {
		t.Fatalf("err = %v, want it to wrap ErrInvalidIPv4Header", err)
	}
}

func TestCh03_Parse_RejectsTruncatedInput(t *testing.T) {
	full := packet.IPv4Header{TotalLength: 20, TTL: 64, Protocol: packet.ProtocolUDP}.Marshal()
	if len(full) != 20 {
		t.Fatalf("Marshal produced %d bytes, want 20", len(full))
	}
	_, _, err := packet.ParseIPv4Header(full[:19]) // one byte short of the minimum header
	testutil.ExpectError(t, err)

	// IHL says 6 (24-byte header) but the buffer holds only the 20 fixed bytes.
	opts := append([]byte(nil), full...)
	opts[0] = 0x46
	_, _, err = packet.ParseIPv4Header(opts)
	testutil.ExpectError(t, err)
}

func TestCh03_Parse_RejectsTotalLengthSmallerThanHeader(t *testing.T) {
	// Checksum is valid (Marshal computes it) — the ONLY thing wrong is TotalLength < IHL*4.
	b := packet.IPv4Header{TotalLength: 19, TTL: 64, Protocol: packet.ProtocolTCP}.Marshal()
	if len(b) != 20 {
		t.Fatalf("Marshal produced %d bytes, want 20", len(b))
	}
	_, _, err := packet.ParseIPv4Header(b)
	testutil.ExpectError(t, err)
	if !errors.Is(err, packet.ErrInvalidIPv4Header) {
		t.Fatalf("err = %v, want it to wrap ErrInvalidIPv4Header", err)
	}
}

func TestCh03_Parse_RejectsTotalLengthLargerThanBuffer(t *testing.T) {
	// Checksum is valid; TotalLength claims 1500 bytes but the buffer holds 20.
	b := packet.IPv4Header{TotalLength: 1500, TTL: 64, Protocol: packet.ProtocolTCP}.Marshal()
	if len(b) != 20 {
		t.Fatalf("Marshal produced %d bytes, want 20", len(b))
	}
	_, _, err := packet.ParseIPv4Header(b)
	testutil.ExpectError(t, err)
	if !errors.Is(err, packet.ErrInvalidIPv4Header) {
		t.Fatalf("err = %v, want it to wrap ErrInvalidIPv4Header", err)
	}
}

func TestCh03_Parse_RejectsCorruptChecksum(t *testing.T) {
	b := packet.IPv4Header{TotalLength: 20, Identification: 0xBEEF, TTL: 64, Protocol: packet.ProtocolUDP, SourceAddr: addrLANClient, DestAddr: addrGateway}.Marshal()
	if len(b) != 20 {
		t.Fatalf("Marshal produced %d bytes, want 20", len(b))
	}
	b[len(b)-1] ^= 0x01 // flip one bit in the destination address; the checksum no longer matches
	_, _, err := packet.ParseIPv4Header(b)
	testutil.ExpectError(t, err)
	if !errors.Is(err, packet.ErrInvalidIPv4Header) {
		t.Fatalf("err = %v, want it to wrap ErrInvalidIPv4Header", err)
	}
}

func TestCh03_IPv4HeaderChecksum_KnownVector(t *testing.T) {
	zeroed := append([]byte(nil), goldenHeader...)
	zeroed[10], zeroed[11] = 0, 0 // the checksum field must be zeroed by the caller

	if got, want := packet.IPv4HeaderChecksum(zeroed), uint16(0x668f); got != want {
		t.Fatalf("IPv4HeaderChecksum(golden with checksum zeroed) = 0x%04x, want 0x%04x", got, want)
	}
}

func TestCh03_IPv4ToUint32(t *testing.T) {
	valid := map[string]uint32{
		"0.0.0.0":         0x00000000,
		"192.168.1.100":   addrLANClient,
		"255.255.255.255": 0xFFFFFFFF,
		"10.0.0.1":        0x0A000001,
		"1.2.3.04":        0x01020304, // pinned choice: leading zeros are DECIMAL (strconv.Atoi), not octal
	}
	for in, want := range valid {
		got, err := packet.IPv4ToUint32(in)
		if err != nil {
			t.Errorf("IPv4ToUint32(%q): unexpected error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("IPv4ToUint32(%q) = 0x%08x, want 0x%08x", in, got, want)
		}
	}
	invalid := []string{"", "1.2.3", "1.2.3.4.5", "256.1.1.1", "1.2.3.-1", "1.2.3.", ".1.2.3.4", " 1.2.3.4", "1.2.3.4 ", "a.b.c.d", "1.2.3.300"}
	for _, in := range invalid {
		if _, err := packet.IPv4ToUint32(in); err == nil {
			t.Errorf("IPv4ToUint32(%q) succeeded, want an error", in)
		} else {
			testutil.ExpectError(t, err)
		}
	}
}

func TestCh03_Uint32ToIPv4(t *testing.T) {
	cases := map[uint32]string{
		0x00000000: "0.0.0.0",
		0x0A141E28: "10.20.30.40",
		0xC0A80164: "192.168.1.100",
		0xFFFFFFFF: "255.255.255.255",
	}
	for in, want := range cases {
		if got := packet.Uint32ToIPv4(in); got != want {
			t.Errorf("Uint32ToIPv4(0x%08x) = %q, want %q", in, got, want)
		}
	}
}

func TestCh03_CIDRToMask_ValuesAndRoundTrips(t *testing.T) {
	want := map[int]uint32{0: 0x00000000, 8: 0xFF000000, 16: 0xFFFF0000, 24: 0xFFFFFF00, 32: 0xFFFFFFFF}
	for bits, mask := range want {
		got, err := packet.CIDRToMask(bits)
		if err != nil {
			t.Fatalf("CIDRToMask(%d): unexpected error: %v", bits, err)
		}
		if got != mask {
			t.Errorf("CIDRToMask(%d) = 0x%08x, want 0x%08x", bits, got, mask)
		}
	}
	for bits := 0; bits <= 32; bits++ {
		mask, err := packet.CIDRToMask(bits)
		if err != nil {
			t.Fatalf("CIDRToMask(%d): %v", bits, err)
		}
		back, err := packet.MaskToCIDR(mask)
		if err != nil {
			t.Fatalf("MaskToCIDR(0x%08x): %v", mask, err)
		}
		if back != bits {
			t.Errorf("MaskToCIDR(CIDRToMask(%d)) = %d", bits, back)
		}
	}
}

func TestCh03_CIDRToMask_RejectsOutOfRange(t *testing.T) {
	for _, bits := range []int{-1, -32, 33, 1000} {
		if _, err := packet.CIDRToMask(bits); err == nil {
			t.Errorf("CIDRToMask(%d) succeeded, want an error", bits)
		} else {
			testutil.ExpectError(t, err)
		}
	}
}

func TestCh03_MaskToCIDR_RejectsNonContiguous(t *testing.T) {
	for _, mask := range []uint32{0xF0F0F0F0, 0x7FFFFFFF, 0x80000001, 0x00000001, 0x0F0F0F0F} {
		if _, err := packet.MaskToCIDR(mask); err == nil {
			t.Errorf("MaskToCIDR(0x%08x) succeeded, want an error (mask bits must be 1*0*)", mask)
		} else {
			testutil.ExpectError(t, err)
		}
	}
	for mask, want := range map[uint32]int{0xFFFFFFFF: 32, 0x00000000: 0, 0xFFFFFF00: 24, 0xFF000000: 8} {
		got, err := packet.MaskToCIDR(mask)
		if err != nil {
			t.Fatalf("MaskToCIDR(0x%08x): unexpected error: %v", mask, err)
		}
		if got != want {
			t.Errorf("MaskToCIDR(0x%08x) = %d, want %d", mask, got, want)
		}
	}
}

func TestCh03_ParseCIDR_Valid(t *testing.T) {
	cases := []struct {
		in   string
		ip   uint32
		bits int
	}{
		{"10.0.0.0/8", 0x0A000000, 8},
		{"192.168.1.0/24", 0xC0A80100, 24},
		{"0.0.0.0/0", 0x00000000, 0},
		{"255.255.255.255/32", 0xFFFFFFFF, 32},
	}
	for _, c := range cases {
		ip, bits, err := packet.ParseCIDR(c.in)
		if err != nil {
			t.Errorf("ParseCIDR(%q): unexpected error: %v", c.in, err)
			continue
		}
		if ip != c.ip || bits != c.bits {
			t.Errorf("ParseCIDR(%q) = (0x%08x, %d), want (0x%08x, %d)", c.in, ip, bits, c.ip, c.bits)
		}
	}
}

func TestCh03_ParseCIDR_RejectsBadInput(t *testing.T) {
	for _, in := range []string{"10.0.0.0", "10.0.0.0/33", "10.0.0.0/-1", "10.0.0.0/abc", "999.1.1.1/8", "10.0.0.0/", "/8", "10.0.0.0/8/9", ""} {
		if _, _, err := packet.ParseCIDR(in); err == nil {
			t.Errorf("ParseCIDR(%q) succeeded, want an error", in)
		} else {
			testutil.ExpectError(t, err)
		}
	}
}

func TestCh03_NetworkAndBroadcastAddresses(t *testing.T) {
	ip := uint32(0xC0A8014D) // 192.168.1.77
	network := map[int]uint32{0: 0x00000000, 16: 0xC0A80000, 24: 0xC0A80100, 32: ip}
	for bits, want := range network {
		if got := packet.NetworkAddress(ip, bits); got != want {
			t.Errorf("NetworkAddress(192.168.1.77, %d) = 0x%08x, want 0x%08x", bits, got, want)
		}
	}
	broadcast := map[int]uint32{0: 0xFFFFFFFF, 16: 0xC0A8FFFF, 24: 0xC0A801FF, 32: ip}
	for bits, want := range broadcast {
		if got := packet.BroadcastAddress(ip, bits); got != want {
			t.Errorf("BroadcastAddress(192.168.1.77, %d) = 0x%08x, want 0x%08x", bits, got, want)
		}
	}
}

func TestCh03_HostCount(t *testing.T) {
	cases := map[int]uint64{0: 4294967294, 8: 16777214, 16: 65534, 24: 254, 30: 2, 31: 2, 32: 1}
	for bits, want := range cases {
		if got := packet.HostCount(bits); got != want {
			t.Errorf("HostCount(/%d) = %d, want %d", bits, got, want)
		}
	}
}

func TestCh03_InSameSubnet(t *testing.T) {
	a, b := uint32(0xC0A8010A), uint32(0xC0A80155) // 192.168.1.10, 192.168.1.85
	c := uint32(0xC0A8020A)                        // 192.168.2.10
	if !packet.InSameSubnet(a, b, 24) {
		t.Error("192.168.1.10 and 192.168.1.85 should share a /24")
	}
	if packet.InSameSubnet(a, c, 24) {
		t.Error("192.168.1.10 and 192.168.2.10 should NOT share a /24")
	}
	if !packet.InSameSubnet(a, c, 16) {
		t.Error("192.168.1.10 and 192.168.2.10 SHOULD share a /16")
	}
	if packet.InSameSubnet(a, b, 32) {
		t.Error("/32 must match only the identical address")
	}
	if !packet.InSameSubnet(a, a, 32) {
		t.Error("/32 must match the identical address")
	}
	if !packet.InSameSubnet(a, c, 0) {
		t.Error("/0 must match everything")
	}
}

func TestCh03_RouteTable_LongestPrefixWins(t *testing.T) {
	var table packet.RouteTable
	routes := []packet.Route{
		{Dest: 0x00000000, Bits: 0, NextHop: "default-gw"},
		{Dest: 0x0A000000, Bits: 16, NextHop: "gw-10-0"},
		{Dest: 0x0A010000, Bits: 24, NextHop: "gw-10-1"},
	}
	for _, r := range routes {
		if err := table.Add(r); err != nil {
			t.Fatalf("Add(%+v): %v", r, err)
		}
	}
	cases := []struct {
		ip   uint32
		want string
	}{
		{0x0A01007B, "gw-10-1"},    // 10.1.0.123 → the /24 (10.1.0.0/24), not the /16, not the default
		{0x0A00CC01, "gw-10-0"},    // 10.0.204.1 → the /16 (10.0.0.0/16), not the default
		{0xC0A80101, "default-gw"}, // 192.168.1.1 → default route
	}
	for _, c := range cases {
		got, ok := table.Lookup(c.ip)
		if !ok {
			t.Fatalf("Lookup(0x%08x) found nothing, want %q", c.ip, c.want)
		}
		if got.NextHop != c.want {
			t.Errorf("Lookup(0x%08x).NextHop = %q, want %q", c.ip, got.NextHop, c.want)
		}
	}
}

func TestCh03_RouteTable_EqualPrefixKeepsEarliest(t *testing.T) {
	var table packet.RouteTable
	for _, hop := range []string{"first", "second"} {
		if err := table.Add(packet.Route{Dest: 0x0A000000, Bits: 16, NextHop: hop}); err != nil {
			t.Fatalf("Add(%q): %v", hop, err)
		}
	}
	got, ok := table.Lookup(0x0A000C0D) // 10.0.12.13, inside 10.0.0.0/16
	if !ok {
		t.Fatal("Lookup found nothing, want the earliest /16")
	}
	if got.NextHop != "first" {
		t.Fatalf("equal-prefix tie went to %q, want %q (the one added first)", got.NextHop, "first")
	}
}

func TestCh03_RouteTable_NoMatchReturnsFalse(t *testing.T) {
	var empty packet.RouteTable
	if _, ok := empty.Lookup(0x0A000001); ok {
		t.Fatal("empty table matched something, want ok == false")
	}
	var table packet.RouteTable
	if err := table.Add(packet.Route{Dest: 0xC0A80000, Bits: 16, NextHop: "lan-gw"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, ok := table.Lookup(0x0A000001); ok { // 10.0.0.1 is not under 192.168.0.0/16
		t.Fatal("Lookup matched a route that does not cover the address, want ok == false")
	}
}

func TestCh03_RouteTable_AddValidatesBits(t *testing.T) {
	var table packet.RouteTable
	for _, bits := range []int{-1, 33, 1000} {
		if err := table.Add(packet.Route{Dest: 0, Bits: bits, NextHop: "bad"}); err == nil {
			t.Errorf("Add(Bits=%d) succeeded, want an error", bits)
		} else {
			testutil.ExpectError(t, err)
		}
	}
	for _, bits := range []int{0, 32} {
		if err := table.Add(packet.Route{Dest: 0, Bits: bits, NextHop: "edge"}); err != nil {
			t.Errorf("Add(Bits=%d): unexpected error: %v", bits, err)
		}
	}
}
