// GIVEN: provided by the course. Do not modify.
package arqx

import (
	"errors"
	"fmt"
)

// ---------------------------------------------------------------------------
// Wire format (all integers big-endian). The SAME layout is documented in
// memlink.go and in docs/chapters/ch05-stop-and-wait-arq.md.
//
//	[seq uint32][ack uint32][flags uint8][length uint16][payload...][crc uint32]
//
//	seq, ack, flags, length   = HeaderSize (11) bytes of header
//	length                    = size of payload in bytes (NOT including the header or the crc)
//	payload                   = 0..MaxPayload (1024) bytes
//	crc                       = CRC-32 (IEEE, as in Ethernet/zip) of ALL preceding bytes
//	total on the wire         = HeaderSize + length + 4
//
// Example: a data packet with seq=7, ack=6, payload "hi" (2 bytes) is
// 00 00 00 07 00 00 00 06 01 00 02 68 69 <4-byte crc> = 17 bytes.
// ---------------------------------------------------------------------------

// Link is what the ARQ machinery talks to. MemLink implements it for tests; a real
// UDP socket would implement it the same way. Implementations must be safe for
// concurrent use by a sender goroutine and a receiver goroutine. Next must NOT be
// called concurrently from multiple goroutines (see MemLink docs).
type Link interface {
	Send([]byte) error
	Next() ([]byte, error)
	Close() error
}

// FlagData marks a packet carrying payload bytes (flags=0x01).
const FlagData uint8 = 0x01

// FlagAck marks a packet that acknowledges another packet (flags=0x02).
const FlagAck uint8 = 0x02

// FlagFIN marks the end-of-transfer packet (flags=0x04). A FIN carries no payload.
// Flags may be OR-combined (e.g. FlagAck), although this course never sets FlagData|FlagFIN together.
const FlagFIN uint8 = 0x04

// HeaderSize is the size in bytes of everything before the payload: seq(4)+ack(4)+flags(1)+length(2).
const HeaderSize = 11

// MaxPayload is the largest payload a single packet may carry, in bytes.
const MaxPayload = 1024

// Packet is one decoded packet. Seq is the packet's own sequence number, Ack is the
// sequence number it acknowledges (set on ack packets). Payload is nil for acks and FINs.
type Packet struct {
	Seq     uint32
	Ack     uint32
	Flags   uint8
	Payload []byte
}

// Has reports whether every flag in bits is set on p.
func (p Packet) Has(bits uint8) bool { return p.Flags&bits == bits }

var ErrPacketCorrupted = errors.New("corrupted packet (checksum mismatch)")
var ErrPacketTooLarge = errors.New("packet payload exceeds MaxPayload")
var ErrTransferFailed = errors.New("transfer failed after too many retransmissions")

// String renders a packet for debugging and test failure messages.
func (p Packet) String() string {
	flags := ""
	if p.Has(FlagData) {
		flags += "DATA "
	}
	if p.Has(FlagAck) {
		flags += "ACK "
	}
	if p.Has(FlagFIN) {
		flags += "FIN "
	}
	if flags == "" {
		flags = "none "
	}
	return fmt.Sprintf("{seq=%d ack=%d flags=%s payload=%dB}", p.Seq, p.Ack, flags, len(p.Payload))
}
