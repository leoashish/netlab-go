// Package dnsx is DNS, the protocol, by hand: build the 12-byte header and the question wire format, encode and
// decode names (length-prefixed labels, and compression pointers on parse), and read the answers out of a
// response.
//
// It is deliberately OFFLINE: there is no socket and no resolver configuration anywhere in this package. A
// Transport carries the query bytes to somewhere — the tests script one in memory — so every byte this package
// produces and consumes is visible, deterministic and hand-checkable.
//
// Chapter 10 of netlab. Run: go test ./internal/dnsx -run Ch10 -race -v
package dnsx

import (
	"errors"

	"netlab/internal/todo"
)

// GIVEN constants. Do not change them; every test in the chapter was built against these values.
const (
	TypeA     uint16 = 1
	TypeNS    uint16 = 2
	TypeCNAME uint16 = 5
	TypeTXT   uint16 = 16
	TypeAAAA  uint16 = 28
	ClassIN   uint16 = 1
)

// GIVEN header flags (the ones this course uses).
const (
	FlagQR    uint16 = 1 << 15 // 0 = query, 1 = response
	FlagRD    uint16 = 1 << 8  // recursion desired
	FlagRA    uint16 = 1 << 7  // recursion available
	RcodeMask uint16 = 0x000F  // low 4 bits of the flags word
)

// ErrNXDomain: rcode 3 — the domain exists, the name does not. A sentinel (not an *RcodeError) so callers can
// write errors.Is(err, dnsx.ErrNXDomain) and treat "no such name" as its own case.
var ErrNXDomain = errors.New("dns: no such name (NXDOMAIN)")

// ErrFormat: the message violates the wire format — too short, a label runs past the end, a pointer loops, a
// reserved label type, a section count that exceeds the limits. Everything that is "this packet is nonsense" wraps
// this one.
var ErrFormat = errors.New("dns: malformed message")

// ErrNameTooLong: a label longer than 63 bytes, or a wire-format name longer than 255 bytes (length octets and the
// terminating zero included).
var ErrNameTooLong = errors.New("dns: name exceeds 255 bytes")

// RcodeError is any non-zero rcode that is NOT NXDOMAIN (rcode 3 maps to ErrNXDomain instead, so that callers can
// branch with errors.Is). Rcode carries the numeric code from the response header.
type RcodeError struct{ Rcode uint16 }

// Error returns a line like "dns: server returned rcode 5 (REFUSED)".
func (e *RcodeError) Error() string {
	return "" // TODO Ch10: format the rcode into a readable message
}

type Question struct {
	Name  string
	Type  uint16
	Class uint16
}

// ResourceRecord keeps RData in WIRE form — the bytes exactly as they appeared in the message. What those bytes
// *mean* depends on Type (4 bytes for an A record, more length-prefixed strings for a TXT, ...). Use RDataToIPv4
// for A records; decoding other types is your job if you need them.
//
// RData is a COPY made by ParseMessage: the returned records stay valid after the input slice is reused.
type ResourceRecord struct {
	Name  string
	Type  uint16
	Class uint16
	TTL   uint32 // seconds
	RData []byte
}

// RDataToIPv4 decodes a 4-byte rdata into a dotted quad ("93.184.216.34"). Any other length returns an error
// wrapping ErrFormat.
func RDataToIPv4(d []byte) (string, error) {
	return "", todo.NotImplemented("Ch10: RDataToIPv4")
}

// Message is one DNS message as this course models it: the header fields, the question section and (for
// responses) the answer section. The authority and additional sections are not modelled; ParseMessage still
// walks over them and validates their framing, so a response that is truncated anywhere is rejected.
type Message struct {
	ID        uint16
	Flags     uint16
	Questions []Question
	Answers   []ResourceRecord
}

// NewQuery returns a recursion-desired (RD=1) QUERY message carrying exactly one question of qtype in the IN class.
func NewQuery(id uint16, name string, qtype uint16) *Message {
	return &Message{} // TODO Ch10: build it
}

// EncodeMessage serialises m to the wire format:
//
//   - a 12-byte header: ID, Flags, QDCOUNT = len(m.Questions), ANCOUNT = len(m.Answers), NSCOUNT = 0, ARCOUNT = 0,
//     all multi-byte integers big-endian;
//   - each question as EncodeName(Name) followed by QTYPE and QCLASS (both uint16 BE);
//   - each answer as EncodeName(Name) followed by TYPE, CLASS, TTL (uint32 BE), RDLENGTH (uint16 BE = len(RData)) and
//     the raw RData bytes.
//
// Errors: a nil message or an empty m.Questions returns an error wrapping ErrFormat (a DNS query is a question
// plus nothing); a question or answer count that does not fit 16 bits likewise; RData longer than 65535 bytes
// likewise; any EncodeName error is returned unchanged. On error nothing else is observable — the returned slice
// is nil.
func EncodeMessage(m *Message) ([]byte, error) {
	return nil, todo.NotImplemented("Ch10: EncodeMessage")
}

// ParseMessage decodes b into a Message.
//
// Strictness (all violations return an error wrapping ErrFormat):
//
//   - len(b) < 12; any section truncated; a question or answer whose header or rdata runs past the end;
//   - names violating the label rules (label > 63 bytes cannot occur on the wire — the high two bits of a label's
//     length octet are 00 — but the DECODED name must fit 255 bytes including the final zero);
//   - compression pointers that leave the message, reserved label types (the 01 and 10 bit patterns), and pointer
//     loops (at most 128 jumps);
//   - QDCOUNT > 32 and ANCOUNT > 128. A real resolver sends one question and sees at most a handful of answers;
//     larger counts are a corrupt or hostile packet. The authority and additional sections are skipped but their
//     framing is validated, so a message truncated anywhere fails.
//
// The returned Message shares nothing with b: every RData slice is a copy.
func ParseMessage(b []byte) (*Message, error) {
	return nil, todo.NotImplemented("Ch10: ParseMessage")
}

// EncodeName appends name to dst in the DNS wire format and returns the extended slice, so callers can stream
// several fields into one byte slice.
//
// Wire format: each label is one length octet followed by that many bytes; the name ends with a zero octet. The
// root name is written "." and encodes as a single 0x00. A trailing dot on any other name is accepted and ignored
// ("example.com." == "example.com"). Names are ASCII per this chapter (hostnames; no clever encoding).
//
// Errors (dst is returned unchanged): an empty name ("" — the root is ".", not an empty string); a label longer than
// 63 bytes; a name whose wire form exceeds 255 bytes including the final zero; an empty label anywhere
// ("a..b", ".example.com", ".."). Length violations return an error wrapping ErrNameTooLong; structural ones wrap
// ErrFormat.
func EncodeName(dst []byte, name string) ([]byte, error) {
	return dst, todo.NotImplemented("Ch10: EncodeName")
}

// ParseName decodes a possibly-compressed name starting at offset in b.
//
// A name is a sequence of length-prefixed labels terminated by 0x00; the root decodes as ".". A length octet whose
// top two bits are 11 (0xC0 mask) starts a COMPRESSION POINTER: the next 6 bits and the following octet carry a
// 14-bit offset into b where the rest of the name continues. Pointers exist so a response full of repeated
// example.com names collapses them. A pointer may target anywhere earlier in the message (looping through it is
// possible), so the decoder allows at most 128 jumps.
//
// Returns the decoded name and end, the offset AFTER the name in the original (uncompressed) path. If the name
// contained a pointer, end is the offset after the pointer that terminated the sequence — not after the name the
// pointer jumped to. Callers use end to locate the next field, which is what RFC 1035's format requires.
//
// Errors (all wrap ErrFormat): offset out of range; a label, terminating zero or pointer straddling the end of b;
// a decoded name longer than 255 bytes; a pointer loop; a top-two-bits pattern of 01 or 10 (reserved).
func ParseName(b []byte, offset int) (name string, end int, err error) {
	return "", 0, todo.NotImplemented("Ch10: ParseName")
}

// Transport is the seam between this package and the network: one RoundTrip sends one wire-format query and waits
// for one wire-format response. The tests script it in memory; a real one would be a UDP socket bound to port 53
// of a resolver, or a TCP connection with a two-byte length prefix.
type Transport interface {
	RoundTrip(query []byte) ([]byte, error)
}

// ResolveA sends one A query for name and returns the IPv4 addresses from the answer section, as dotted quads, IN
// ANSWER ORDER.
//
// The checks, in order:
//
//  1. the response is parsed (any parse error is returned as-is);
//  2. the response ID must equal id — otherwise the bytes are for some other query (or a spoof);
//  3. the QR bit (FlagQR) must be set — a message without it is a query, not an answer;
//  4. the rcode (Flags & RcodeMask): 0 → proceed; 3 → return nil, ErrNXDomain (the name does not exist);
//     any other non-zero value → return nil, &RcodeError{Rcode: rcode}.
//
// Then: collect every answer with Type == TypeA and Class == ClassIN; decode each with RDataToIPv4; a malformed A
// record (RData not 4 bytes) is SKIPPED, not fatal. Non-A answers (AAAA, CNAME, ...) are ignored entirely — this
// function does not chase CNAMEs and does not notice them.
func ResolveA(t Transport, id uint16, name string) ([]string, error) {
	return nil, todo.NotImplemented("Ch10: ResolveA")
}
