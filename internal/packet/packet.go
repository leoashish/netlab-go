// Package packet parses and builds IPv4 packets by hand: the 20-byte IPv4 header, CIDR subnet math, and a
// longest-prefix-match routing table — the exact three things every router on the internet does.
//
// Everything here is standard-library only and works on raw bytes and uint32 addresses. The tests for this
// package are offline: no network is ever touched.
//
// Contract of this package (read once, learn once):
//
//   - Errors wrapping ErrInvalidIPv4Header mean "these bytes are not a valid IPv4 header" — callers can
//     match them with errors.Is(err, packet.ErrInvalidIPv4Header).
//   - Addresses travel as uint32 in BIG-ENDIAN order (the byte order they have on the wire): the most
//     significant byte is the first octet, so 192.168.1.100 == 0xC0A80164.
//   - Prefix lengths ("bits") are always 0..32, inclusive. /32 is one host, /31 is two (point-to-point),
//     /0 matches everything.
package packet

import (
	"errors"

	"netlab/internal/todo"
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
	TotalLength    uint16 // header + payload, in bytes, including the header itself
	Identification uint16 // used by fragmentation/reassembly; copied through untouched
	TTL            uint8  // hops left; routers decrement it and drop the packet at 0
	Protocol       uint8  // ProtocolICMP, ProtocolTCP, ProtocolUDP, ...
	SourceAddr     uint32 // big-endian
	DestAddr       uint32 // big-endian
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
//
// Source and destination addresses are big-endian decoded. Exactly headerLen bytes are consumed; if b holds
// a whole packet, b[headerLen:] is the payload.
func ParseIPv4Header(b []byte) (h IPv4Header, headerLen int, err error) {
	return IPv4Header{}, 0, todo.NotImplemented("Ch03: ParseIPv4Header")
}

// Marshal serializes h into exactly 20 bytes: version=4, IHL=5, DSCP/ECN=0, flags/fragment offset=0, and the
// header checksum computed and written into bytes 10-11. h.TotalLength is copied as-is — when you transmit a
// payload too, set it to 20 + len(payload) yourself.
func (h IPv4Header) Marshal() []byte {
	return nil // TODO Ch03
}

// IPv4HeaderChecksum computes the Internet checksum of a header with the checksum field (bytes 10-11) ZEROED.
// The caller is responsible for zeroing those two bytes first: pass a header whose checksum field is zero
// and the result is the value to write there. For verification, copy the header, zero bytes 10-11, compute,
// and compare against the checksum field you found.
//
// The algorithm is RFC 1071: treat the header as big-endian 16-bit words, sum them with carry wraparound,
// then take the one's complement. An odd trailing byte (never happens for IPv4, which is always a multiple of
// 4 bytes) is padded with a zero low byte. The sum folds repeatedly until it fits in 16 bits.
func IPv4HeaderChecksum(hdr []byte) uint16 {
	return 0 // TODO Ch03
}

// IPv4ToUint32 parses a dotted-quad address ("192.168.1.1") into its big-endian uint32 form.
//
// Exactly 4 dot-separated parts, each parsed with strconv.Atoi (base 10, error propagation) and each required
// to be in 0..255. There is NO octal interpretation: leading zeros are accepted and mean decimal,
// so "1.2.3.04" is the address 1.2.3.4. Anything Atoi rejects (letters, spaces, empty parts, signs on out-of-range
// values, more or fewer than 4 parts) is an error.
func IPv4ToUint32(s string) (uint32, error) {
	return 0, todo.NotImplemented("Ch03: IPv4ToUint32")
}

// Uint32ToIPv4 formats a big-endian uint32 as a dotted-quad string: 0xC0A80164 becomes "192.168.1.100".
func Uint32ToIPv4(v uint32) string {
	return "" // TODO Ch03
}

// CIDRToMask converts a prefix length to a mask. bits must be in 0..32; anything else is an error.
// CIDRToMask(0) == 0x00000000, CIDRToMask(24) == 0xFFFFFF00, CIDRToMask(32) == 0xFFFFFFFF.
func CIDRToMask(bits int) (uint32, error) {
	return 0, todo.NotImplemented("Ch03: CIDRToMask")
}

// MaskToCIDR converts a mask to its prefix length. It errors unless the mask is a run of 1-bits followed by a
// run of 0-bits (i.e. it came from some CIDRToMask): 0xF0F0F0F0 and 0x7FFFFFFF are rejected.
func MaskToCIDR(mask uint32) (int, error) {
	return 0, todo.NotImplemented("Ch03: MaskToCIDR")
}

// ParseCIDR parses "a.b.c.d/nn" into its address and prefix length. Both halves are validated: the address
// with IPv4ToUint32 (so "999.0.0.0/8" is rejected) and the prefix with strconv.Atoi, required to be in 0..32
// (so "10.0.0.0/33" and "10.0.0.0/abc" are rejected). Exactly one slash is allowed.
func ParseCIDR(s string) (ip uint32, bits int, err error) {
	return 0, 0, todo.NotImplemented("Ch03: ParseCIDR")
}

// NetworkAddress returns the network (base) address of ip within the prefix: ip & mask.
// bits is clamped to 0..32 (bits <= 0 returns 0, bits >= 32 returns ip).
func NetworkAddress(ip uint32, bits int) uint32 {
	return 0 // TODO Ch03
}

// BroadcastAddress returns the broadcast address of the prefix: ip | wildcard, where wildcard is the
// one's complement of the mask. For bits == 32 the wildcard is 0, so the broadcast address IS the
// address itself. For bits == 0 it is 255.255.255.255. bits is clamped to 0..32.
func BroadcastAddress(ip uint32, bits int) uint32 {
	return 0 // TODO Ch03
}

// HostCount returns the number of USABLE host addresses in a prefix: 2^(32-bits) - 2, EXCEPT
//
//   - bits == 32 → 1   (a single host route; every bit is network)
//   - bits == 31 → 2   (point-to-point link, RFC 3021: no network/broadcast addresses are reserved)
//
// bits is clamped to 0..32. For /0 that is 4294967294 usable addresses.
func HostCount(bits int) uint64 {
	return 0 // TODO Ch03
}

// InSameSubnet reports whether a and b are in the same subnet under the given prefix:
// (a & mask) == (b & mask). bits == 32 means "only the identical address", bits == 0 means "everything".
// bits is clamped to 0..32.
func InSameSubnet(a, b uint32, bits int) bool {
	return false // TODO Ch03
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
	return todo.NotImplemented("Ch03: RouteTable.Add")
}

// Lookup returns the most specific route matching ip: the one with the LARGEST Bits whose masked Dest
// equals the masked ip. Ties on Bits go to the route added EARLIEST (stable and deterministic).
// If nothing matches (and no default route 0.0.0.0/0 was added) it returns false.
func (t *RouteTable) Lookup(ip uint32) (Route, bool) {
	return Route{}, false // TODO Ch03
}
