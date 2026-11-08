// Package packet parses and builds IPv4 packets by hand: the 20-byte IPv4 header, CIDR subnet math, and a
// longest-prefix-match routing table — the exact three things every router on the internet does.
//
// This is the reference solution for Chapter 03. The exercise copy is internal/packet/packet.go.
package packet

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
	"strconv"
	"strings"
)

// Protocol numbers from the IP protocol field (IANA). These are the three you will meet most often.
const ProtocolICMP uint8 = 1
const ProtocolTCP uint8 = 6
const ProtocolUDP uint8 = 17

// ErrInvalidIPv4Header marks every error ParseIPv4Header returns. Use errors.Is to match it.
var ErrInvalidIPv4Header = errors.New("invalid IPv4 header")

// IPv4Header is the fixed 20-byte IPv4 header. Fields not shown here are always zero on Marshal and ignored
// on Parse (DSCP/ECN, flags, fragment offset): this course keeps packets simple and unfragmented.
//
// SourceAddr and DestAddr are uint32, big-endian decoded: IPv4ToUint32("192.168.1.100") == 0xC0A80164.
type IPv4Header struct {
	TotalLength    uint16
	Identification uint16
	TTL            uint8
	Protocol       uint8
	SourceAddr     uint32
	DestAddr       uint32
}

// ParseIPv4Header parses one IPv4 header from the START of b and returns it together with the header length
// in bytes (IHL * 4). The payload, if any, starts at that offset and is NOT copied or validated — only the
// header is.
//
// Validation (any failure returns ErrInvalidIPv4Header):
//
//   - version == 4
//   - IHL (header length field) >= 5  (a smaller value cannot even fit the fixed fields)
//   - len(b) >= IHL * 4               (options, if any, are present and accounted for)
//   - TotalLength >= IHL * 4          (a packet cannot be smaller than its own header)
//   - TotalLength <= len(b)           (the buffer holds at least the whole packet)
//   - the header checksum VERIFIES    (bytes 10-11, one's complement sum of the rest)
func ParseIPv4Header(b []byte) (h IPv4Header, headerLen int, err error) {
	if len(b) < 20 {
		return IPv4Header{}, 0, fmt.Errorf("%w: buffer holds %d bytes, need at least the 20-byte fixed header", ErrInvalidIPv4Header, len(b))
	}
	if version := b[0] >> 4; version != 4 {
		return IPv4Header{}, 0, fmt.Errorf("%w: version %d, want 4", ErrInvalidIPv4Header, version)
	}
	ihl := int(b[0] & 0x0f)
	if ihl < 5 {
		return IPv4Header{}, 0, fmt.Errorf("%w: IHL %d, want >= 5", ErrInvalidIPv4Header, ihl)
	}
	headerLen = ihl * 4
	if len(b) < headerLen {
		return IPv4Header{}, 0, fmt.Errorf("%w: IHL says %d header bytes, buffer holds %d", ErrInvalidIPv4Header, headerLen, len(b))
	}
	total := int(binary.BigEndian.Uint16(b[2:4]))
	if total < headerLen {
		return IPv4Header{}, 0, fmt.Errorf("%w: TotalLength %d is smaller than the %d-byte header", ErrInvalidIPv4Header, total, headerLen)
	}
	if total > len(b) {
		return IPv4Header{}, 0, fmt.Errorf("%w: TotalLength %d is larger than the %d-byte buffer", ErrInvalidIPv4Header, total, len(b))
	}
	if !checksumVerifies(b[:headerLen]) {
		return IPv4Header{}, 0, fmt.Errorf("%w: header checksum mismatch", ErrInvalidIPv4Header)
	}
	h = IPv4Header{
		TotalLength:    uint16(total),
		Identification: binary.BigEndian.Uint16(b[4:6]),
		TTL:            b[8],
		Protocol:       b[9],
		SourceAddr:     binary.BigEndian.Uint32(b[12:16]),
		DestAddr:       binary.BigEndian.Uint32(b[16:20]),
	}
	return h, headerLen, nil
}

// checksumVerifies reports whether the checksum field of hdr matches the rest of the header. It copies hdr
// (never mutates the caller's bytes), zeroes the checksum field, and recomputes.
func checksumVerifies(hdr []byte) bool {
	tmp := make([]byte, len(hdr))
	copy(tmp, hdr)
	want := binary.BigEndian.Uint16(tmp[10:12])
	tmp[10], tmp[11] = 0, 0
	return IPv4HeaderChecksum(tmp) == want
}

// Marshal serializes h into exactly 20 bytes: version=4, IHL=5, DSCP/ECN=0, flags/fragment offset=0, and the
// header checksum computed and written into bytes 10-11. h.TotalLength is copied as-is — when you transmit a
// payload too, set it to 20 + len(payload) yourself.
func (h IPv4Header) Marshal() []byte {
	b := make([]byte, 20)
	b[0] = 0x45 // version 4 in the high nibble, IHL 5 in the low nibble
	binary.BigEndian.PutUint16(b[2:4], h.TotalLength)
	binary.BigEndian.PutUint16(b[4:6], h.Identification)
	b[8] = h.TTL
	b[9] = h.Protocol
	binary.BigEndian.PutUint32(b[12:16], h.SourceAddr)
	binary.BigEndian.PutUint32(b[16:20], h.DestAddr)
	// bytes 10-11 are still zero here, which is exactly what the checksum computation wants.
	binary.BigEndian.PutUint16(b[10:12], IPv4HeaderChecksum(b))
	return b
}

// IPv4HeaderChecksum computes the Internet checksum of a header with the checksum field (bytes 10-11) ZEROED.
// The caller is responsible for zeroing those two bytes first. RFC 1071: big-endian 16-bit words, summed with
// carry wraparound, then one's complemented. An odd trailing byte gets a zero low byte (IPv4 headers are
// always a multiple of 4 bytes, so this only matters for other protocols sharing the algorithm).
func IPv4HeaderChecksum(hdr []byte) uint16 {
	return ^internetChecksum(hdr)
}

// internetChecksum is the raw RFC 1071 accumulator: the folded 16-bit one's complement sum, WITHOUT the final
// complement. IPv4HeaderChecksum complements it; an ICMP/UDP/3 checksum would too.
func internetChecksum(b []byte) uint16 {
	var sum uint32
	for len(b) >= 2 {
		sum += uint32(binary.BigEndian.Uint16(b[:2]))
		b = b[2:]
	}
	if len(b) == 1 { // pad an odd tail with a zero byte
		sum += uint32(b[0]) << 8
	}
	for sum>>16 != 0 { // fold the carry back into the low 16 bits
		sum = sum&0xffff + sum>>16
	}
	return uint16(sum)
}

// IPv4ToUint32 parses a dotted-quad address ("192.168.1.1") into its big-endian uint32 form.
//
// Exactly 4 dot-separated parts, each parsed with strconv.Atoi (base 10, error propagation) and each required
// to be in 0..255. There is NO octal interpretation: leading zeros are accepted and mean decimal, so
// "1.2.3.04" is the address 1.2.3.4. Anything Atoi rejects (letters, spaces, empty parts, more or fewer than
// 4 parts) is an error.
func IPv4ToUint32(s string) (uint32, error) {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return 0, fmt.Errorf("IPv4ToUint32(%q): got %d parts, want 4", s, len(parts))
	}
	var v uint32
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil {
			return 0, fmt.Errorf("IPv4ToUint32(%q): octet %q: %w", s, part, err)
		}
		if n < 0 || n > 255 {
			return 0, fmt.Errorf("IPv4ToUint32(%q): octet %q = %d, want 0..255", s, part, n)
		}
		v |= uint32(n) << (24 - 8*uint(i))
	}
	return v, nil
}

// Uint32ToIPv4 formats a big-endian uint32 as a dotted-quad string: 0xC0A80164 becomes "192.168.1.100".
func Uint32ToIPv4(v uint32) string {
	return fmt.Sprintf("%d.%d.%d.%d", v>>24, v>>16&0xff, v>>8&0xff, v&0xff)
}

// CIDRToMask converts a prefix length to a mask. bits must be in 0..32; anything else is an error.
// CIDRToMask(0) == 0x00000000, CIDRToMask(24) == 0xFFFFFF00, CIDRToMask(32) == 0xFFFFFFFF.
func CIDRToMask(bits int) (uint32, error) {
	if bits < 0 || bits > 32 {
		return 0, fmt.Errorf("CIDRToMask: prefix length %d out of range 0..32", bits)
	}
	if bits == 0 {
		return 0, nil // shifting a 32-bit value by 32 would be 0 in Go, but say it out loud
	}
	return ^uint32(0) << uint(32-bits), nil
}

// maskForBits is the no-error version CIDRToMask for the many callers that clamp instead of erroring.
func maskForBits(bits int) uint32 {
	switch {
	case bits <= 0:
		return 0
	case bits >= 32:
		return ^uint32(0)
	default:
		return ^uint32(0) << uint(32-bits)
	}
}

// MaskToCIDR converts a mask to its prefix length. It errors unless the mask is a run of 1-bits followed by a
// run of 0-bits (i.e. it came from some CIDRToMask): 0xF0F0F0F0 and 0x7FFFFFFF are rejected.
func MaskToCIDR(mask uint32) (int, error) {
	ones := bits.OnesCount32(mask)
	var want uint32
	if ones > 0 {
		want = ^uint32(0) << uint(32-ones)
	}
	if mask != want {
		return 0, fmt.Errorf("MaskToCIDR(0x%08x): not a run of 1s followed by 0s", mask)
	}
	return ones, nil
}

// ParseCIDR parses "a.b.c.d/nn" into its address and prefix length. Both halves are validated: the address
// with IPv4ToUint32 and the prefix with strconv.Atoi, required to be in 0..32. Exactly one slash is allowed.
func ParseCIDR(s string) (ip uint32, bits int, err error) {
	slash := strings.IndexByte(s, '/')
	if slash < 0 {
		return 0, 0, fmt.Errorf("ParseCIDR(%q): missing /prefix", s)
	}
	if strings.IndexByte(s[slash+1:], '/') >= 0 {
		return 0, 0, fmt.Errorf("ParseCIDR(%q): more than one slash", s)
	}
	ip, err = IPv4ToUint32(s[:slash])
	if err != nil {
		return 0, 0, fmt.Errorf("ParseCIDR(%q): %w", s, err)
	}
	n, err := strconv.Atoi(s[slash+1:])
	if err != nil {
		return 0, 0, fmt.Errorf("ParseCIDR(%q): prefix %q: %w", s, s[slash+1:], err)
	}
	if n < 0 || n > 32 {
		return 0, 0, fmt.Errorf("ParseCIDR(%q): prefix length %d out of range 0..32", s, n)
	}
	return ip, n, nil
}

// NetworkAddress returns the network (base) address of ip within the prefix: ip & mask.
// bits is clamped to 0..32 (bits <= 0 returns 0, bits >= 32 returns ip).
func NetworkAddress(ip uint32, bits int) uint32 {
	return ip & maskForBits(bits)
}

// BroadcastAddress returns the broadcast address of the prefix: ip | wildcard, where wildcard is the
// one's complement of the mask. For bits == 32 the wildcard is 0, so the broadcast address IS the address
// itself; for bits == 0 it is 255.255.255.255. bits is clamped to 0..32.
func BroadcastAddress(ip uint32, bits int) uint32 {
	return ip | ^maskForBits(bits)
}

// HostCount returns the number of USABLE host addresses in a prefix: 2^(32-bits) - 2, EXCEPT
//
//   - bits == 32 → 1   (a single host route; every bit is network)
//   - bits == 31 → 2   (point-to-point link, RFC 3021: no network/broadcast addresses are reserved)
//
// bits is clamped to 0..32. For /0 that is 4294967294 usable addresses.
func HostCount(bits int) uint64 {
	switch {
	case bits >= 32:
		return 1
	case bits == 31:
		return 2
	case bits <= 0:
		return (uint64(1) << 32) - 2
	default:
		return (uint64(1) << uint(32-bits)) - 2
	}
}

// InSameSubnet reports whether a and b are in the same subnet under the given prefix:
// (a & mask) == (b & mask). bits == 32 means "only the identical address", bits == 0 means "everything".
func InSameSubnet(a, b uint32, bits int) bool {
	m := maskForBits(bits)
	return a&m == b&m
}

// Route is one row of a routing table: "packets for Dest/Bits go to NextHop". NextHop is opaque to this
// package (usually a dotted-quad string, but a route lookup never inspects it).
type Route struct {
	Dest    uint32 // network address in big-endian form (may carry host bits; both sides are masked on lookup)
	Bits    int    // prefix length, 0..32
	NextHop string // where to send matching packets, e.g. "192.168.1.1"; uninterpreted here
}

// RouteTable is a longest-prefix-match routing table. It is zero-value ready:
//
//	var t RouteTable
//	t.Add(Route{...})
//	r, ok := t.Lookup(ip)
type RouteTable struct {
	routes []Route // in insertion order; equal-specificity ties go to the EARLIEST one added
}

// Add inserts r into the table. It errors unless 0 <= r.Bits <= 32. Overlapping routes are ALLOWED —
// that is the whole point of longest-prefix matching — and are resolved at lookup time.
func (t *RouteTable) Add(r Route) error {
	if r.Bits < 0 || r.Bits > 32 {
		return fmt.Errorf("RouteTable.Add: prefix length %d out of range 0..32", r.Bits)
	}
	t.routes = append(t.routes, r)
	return nil
}

// Lookup returns the most specific route matching ip: the one with the LARGEST Bits whose masked Dest
// equals the masked ip. Ties on Bits go to the route added EARLIEST (stable and deterministic).
// If nothing matches (and no default route 0.0.0.0/0 was added) it returns false.
func (t *RouteTable) Lookup(ip uint32) (Route, bool) {
	best := -1
	for i := range t.routes {
		m := maskForBits(t.routes[i].Bits)
		if ip&m != t.routes[i].Dest&m {
			continue
		}
		// Strictly greater: on a tie the incumbent (added earlier) wins.
		if best < 0 || t.routes[i].Bits > t.routes[best].Bits {
			best = i
		}
	}
	if best < 0 {
		return Route{}, false
	}
	return t.routes[best], true
}
