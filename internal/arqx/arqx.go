// Package arqx implements stop-and-wait ARQ (Automatic Repeat reQuest) on top of an
// unreliable datagram link: sequence numbers, acknowledgements, retransmission on timeout,
// and a CRC on every packet. This is the heart of what TCP does for you.
//
// See docs/chapters/ch05-stop-and-wait-arq.md for the guided lesson.
package arqx

import (
	"time"

	"netlab/internal/todo"
)

// These are fixed by the protocol, not by taste — see the lesson.
const (
	maxAttempts = 10 // transmissions per packet (1 initial + 9 retransmissions), then ErrTransferFailed
	ackTimeout  = 50 * time.Millisecond
)

// EncodePacket encodes p to wire bytes.
//
// Wire format (big-endian everywhere):
//
//	[seq uint32][ack uint32][flags uint8][length uint16][payload...][crc uint32]
//
// length = len(p.Payload) in bytes (everything before the crc, minus HeaderSize).
// crc = CRC-32 (hash/crc32, IEEE polynomial) of ALL preceding bytes (HEADER + payload).
// Total wire size = HeaderSize + len(p.Payload) + 4.
//
// A payload larger than MaxPayload (1024) is rejected with ErrPacketTooLarge
// (wrap it with %w and include the actual size, e.g. fmt.Errorf("%w: %d bytes", ErrPacketTooLarge, len)).
// The returned slice is freshly allocated; the caller may keep and mutate it.
func EncodePacket(p Packet) ([]byte, error) {
	// TODO Ch05: length check → ErrPacketTooLarge; big-endian header; copy payload; append CRC.
	return nil, todo.NotImplemented("Ch05: EncodePacket")
}

// DecodePacket decodes wire bytes back into a Packet.
//
// It rejects three kinds of bad input, all with an error wrapping ErrPacketCorrupted
// (add context with %w, never bare):
//
//  1. len(b) < HeaderSize + 4                      → "packet too short"
//  2. len(b) != HeaderSize + uint16(b[9:11]) + 4   → "length field ... doesn't match"
//  3. CRC-32 of b[:len(b)-4] != big-endian uint32 at b[len(b)-4:] → "crc mismatch"
//
// On success the Payload is a COPY (not a slice of b) so the caller may reuse b.
// DecodePacket does NOT enforce MaxPayload: the CRC is the arbiter of validity on the wire.
func DecodePacket(b []byte) (Packet, error) {
	// TODO Ch05: the three checks above, then big-endian reads; copy the payload.
	return Packet{}, todo.NotImplemented("Ch05: DecodePacket")
}

// SendAll reliably delivers data over link using stop-and-wait ARQ, and returns nil
// once the final FIN is acknowledged.
//
// The protocol:
//
//  1. Fragment data into chunks of at most MaxPayload bytes. Chunk i becomes a DATA packet
//     with Seq = i (flags = FlagData).
//  2. Transmit each packet and wait for its ack: a packet with FlagAck set and Ack == our Seq.
//     Wait ackTimeout (50 ms) per attempt; on timeout, retransmit. At most maxAttempts
//     (10) transmissions per packet — then return ErrTransferFailed (wrap it: %w, include the seq).
//  3. After the LAST data packet, send a FIN packet (flags = FlagFIN, empty payload,
//     Seq = number of data packets) and wait for its ack too.
//  4. An empty data slice sends only the FIN (Seq = 0).
//
// Things to watch out for:
//   - link.Next() blocks, so run it in a dedicated goroutine feeding a channel; call it from
//     EXACTLY ONE goroutine (MemLink forbids concurrent Next).
//   - Corrupted packets are not acks: ignore them (the 50 ms timer will fire and you will retransmit).
//     A corrupted ACK is the receiver-of-the-ack problem — same handling.
//   - Payload slices you hand to EncodePacket must not alias the caller's data in a way that
//     surprises you: slices here are fine (data[i:j] shares memory, nobody mutates it).
//   - FIN carrying an empty payload still gets a real CRC — every packet on the wire has one.
//   - If link.Send returns an error (e.g. io.ErrClosedPipe after Close), propagate it.
//
// See the lesson for the full timeline diagram.
func SendAll(link Link, data []byte) error {
	// TODO Ch05: ack-pump goroutine → per packet: encode, send, waitForAck with select+time.After;
	// retransmit on timeout; ErrTransferFailed after maxAttempts; then the FIN packet.
	return todo.NotImplemented("Ch05: SendAll")
}

// ReceiveAll receives a stop-and-wait transfer from link and returns the concatenated payload.
//
// The receiver side of the protocol:
//
//  1. Packets arrive via link.Next(). Decode each one; a decode error is a CORRUPTED packet:
//     drop it silently, keep waiting, and count it. Reset the counter on any valid packet.
//     After 5 CONSECUTIVE corrupted packets, give up and return an error wrapping ErrPacketCorrupted
//     (that is not loss, that is a broken link).
//  2. A valid packet whose Seq == the next expected seq (starts at 0) and has FlagData:
//     append its payload, send an ack for it. A valid packet with FlagFIN set instead:
//     send an ack for it too — then STOP accumulating payload (but see 4).
//  3. DUPLICATES: a packet whose Seq == the last acked seq (the sender never saw our ack and
//     retransmitted) → re-ack it, do NOT append its payload again.
//  4. FIN + EOF: the FIN-ack itself can be LOST, so the sender keeps retransmitting its FIN
//     until an ack arrives. If you returned immediately after the first FIN, those retransmissions
//     would meet a vanished receiver and the sender would burn all 10 attempts. So:
//     after the FIN, keep READING — answer any duplicate FIN with a fresh ack, ignore everything
//     else — and return the payload once the link is CLOSED (Next returns io.EOF).
//     Tests always close the link after the sender is done.
//     If the link closes BEFORE a FIN arrives, return an error wrapping io.EOF.
//  5. An empty transfer (a lone FIN) returns a non-nil, empty slice — not nil.
//  6. Acks are packets too: Seq = 0, Flags = FlagAck, Ack = seq of the acked packet, empty payload.
//
// No global deadline: a healthy-but-slow sender can take as long as it needs. 5 consecutive
// corrupted packets is the only "give up" condition (besides the link closing).
func ReceiveAll(link Link) ([]byte, error) {
	// TODO Ch05: loop over link.Next(); decode; count consecutive corrupted (>5 → give up);
	// in-order DATA → append + ack; in-order FIN → ack, set finSeen; duplicate → re-ack only;
	// io.EOF → return payload if finSeen else an error wrapping io.EOF.
	return nil, todo.NotImplemented("Ch05: ReceiveAll")
}
