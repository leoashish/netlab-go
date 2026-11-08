// Package arqx implements stop-and-wait ARQ (Automatic Repeat reQuest) on top of an unreliable
// datagram link: sequence numbers, acknowledgements, retransmission on timeout, and a CRC on
// every packet. See docs/chapters/ch05-stop-and-wait-arq.md for the guided lesson.
//
// Reference implementation — chapter 05 solution.
package arqx

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"time"
)

const (
	maxAttempts = 10 // transmissions per packet (1 initial + 9 retransmissions), then ErrTransferFailed
	ackTimeout  = 50 * time.Millisecond
)

// EncodePacket encodes p to wire bytes:
// [seq 4][ack 4][flags 1][length 2][payload][crc 4] (big-endian), crc = CRC-32-IEEE of all preceding bytes.
// Payloads larger than MaxPayload are rejected with ErrPacketTooLarge.
func EncodePacket(p Packet) ([]byte, error) {
	if len(p.Payload) > MaxPayload {
		return nil, fmt.Errorf("%w: %d bytes (max %d)", ErrPacketTooLarge, len(p.Payload), MaxPayload)
	}
	buf := make([]byte, HeaderSize+len(p.Payload)+4)
	binary.BigEndian.PutUint32(buf[0:4], p.Seq)
	binary.BigEndian.PutUint32(buf[4:8], p.Ack)
	buf[8] = p.Flags
	binary.BigEndian.PutUint16(buf[9:11], uint16(len(p.Payload)))
	copy(buf[HeaderSize:], p.Payload)
	crc := crc32.ChecksumIEEE(buf[:HeaderSize+len(p.Payload)])
	binary.BigEndian.PutUint32(buf[HeaderSize+len(p.Payload):], crc)
	return buf, nil
}

// DecodePacket decodes wire bytes back into a Packet.
// Too short, a length-field mismatch, or a CRC mismatch all return an error wrapping ErrPacketCorrupted.
func DecodePacket(b []byte) (Packet, error) {
	if len(b) < HeaderSize+4 {
		return Packet{}, fmt.Errorf("%w: packet too short (%d bytes, need at least %d)", ErrPacketCorrupted, len(b), HeaderSize+4)
	}
	payloadLen := int(binary.BigEndian.Uint16(b[9:11]))
	if len(b) != HeaderSize+payloadLen+4 {
		return Packet{}, fmt.Errorf("%w: length field says %d payload bytes but the packet is %d bytes on the wire",
			ErrPacketCorrupted, payloadLen, len(b))
	}
	got := crc32.ChecksumIEEE(b[:len(b)-4])
	want := binary.BigEndian.Uint32(b[len(b)-4:])
	if got != want {
		return Packet{}, fmt.Errorf("%w: crc mismatch (computed %#08x, packet carries %#08x)", ErrPacketCorrupted, got, want)
	}
	payload := append([]byte(nil), b[HeaderSize:len(b)-4]...)
	return Packet{
		Seq:     binary.BigEndian.Uint32(b[0:4]),
		Ack:     binary.BigEndian.Uint32(b[4:8]),
		Flags:   b[8],
		Payload: payload,
	}, nil
}

// ackResult carries one decoded packet (or a decode error, for a corrupted one) from the ack pump.
type ackResult struct {
	pkt Packet
	err error
}

// ackPump reads the link in a dedicated goroutine (exactly one concurrent Next caller) and forwards
// every decoded packet to the returned channel. The channel is unbuffered: a result is handed over
// only when the sender is actually waiting for it. done asks the pump to stop forwarding (it exits
// as soon as the link closes).
func ackPump(link Link, done <-chan struct{}) <-chan ackResult {
	results := make(chan ackResult)
	go func() {
		for {
			b, err := link.Next()
			if err != nil {
				return // link closed: nothing more to forward
			}
			pkt, derr := DecodePacket(b)
			res := ackResult{pkt: pkt, err: derr}
			select {
			case results <- res:
			case <-done:
				return
			}
		}
	}()
	return results
}

// SendAll reliably delivers data over link using stop-and-wait ARQ.
//
// data is fragmented into <= MaxPayload-byte chunks; each chunk is one DATA packet (seq 0, 1, ...).
// Every packet — and finally a FIN with seq = number-of-data-packets — is transmitted and the sender
// waits for its ack (Ack == our Seq, FlagAck set) with a 50 ms timeout. If no valid ack arrives in
// time the packet is retransmitted, at most maxAttempts transmissions per packet; then SendAll
// returns ErrTransferFailed. A corrupted or unexpected packet arriving while waiting is ignored.
//
// An empty data slice sends only the FIN packet (seq 0).
// Sends on a closed link surface the link's error (e.g. io.ErrClosedPipe).
// SendAll returns nil once the FIN is acknowledged.
func SendAll(link Link, data []byte) error {
	done := make(chan struct{})
	defer close(done) // tell the ack pump to stop forwarding; the pump exits when the link closes
	results := ackPump(link, done)

	seq := uint32(0)
	for off := 0; off < len(data); off += MaxPayload {
		end := off + MaxPayload
		if end > len(data) {
			end = len(data)
		}
		if err := transmitAndAwaitAck(link, results, Packet{Seq: seq, Flags: FlagData, Payload: data[off:end]}); err != nil {
			return err
		}
		seq++
	}
	fin := Packet{Seq: seq, Flags: FlagFIN}
	if err := transmitAndAwaitAck(link, results, fin); err != nil {
		return err
	}
	return nil
}

func transmitAndAwaitAck(link Link, results <-chan ackResult, pkt Packet) error {
	enc, err := EncodePacket(pkt)
	if err != nil {
		return err
	}
	for attempt := 1; ; attempt++ {
		if err := link.Send(enc); err != nil {
			return err
		}
		acked, err := waitForAck(results, pkt.Seq, ackTimeout)
		if err != nil {
			return err
		}
		if acked {
			return nil
		}
		if attempt == maxAttempts {
			return fmt.Errorf("%w (seq %d after %d transmissions)", ErrTransferFailed, pkt.Seq, maxAttempts)
		}
	}
}

// waitForAck consumes packets until an ack for seq arrives (returns true) or the timeout fires
// (returns false, nil). Corrupted packets and packets that are not the awaited ack are ignored.
func waitForAck(results <-chan ackResult, seq uint32, timeout time.Duration) (bool, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case res := <-results:
			if res.err != nil {
				continue // corrupted packet (bad CRC): ignore it, the timer is the arbiter
			}
			p := res.pkt
			if p.Has(FlagAck) && p.Ack == seq {
				return true, nil
			}
		case <-timer.C:
			return false, nil
		}
	}
}

func sendAck(link Link, seq uint32) error {
	enc, err := EncodePacket(Packet{Ack: seq, Flags: FlagAck})
	if err != nil {
		return err
	}
	return link.Send(enc)
}

// ReceiveAll receives a stop-and-wait transfer from link and returns the concatenated payload.
//
// Corrupted packets are dropped silently and counted; any valid packet resets the counter.
// After 5 consecutive corrupted packets ReceiveAll gives up and returns an error wrapping
// ErrPacketCorrupted (more than that means something is fundamentally broken, not just lossy).
//
// Duplicate packets (seq == the last acked seq — e.g. the sender's retransmission after our ack
// was lost) are acked again but their payload is NOT appended a second time.
//
// FIN handling: a FIN packet (FlagFIN, seq == expectedSeq) is acknowledged exactly like a data
// packet. Because the acknowledgement itself can be lost, the sender retransmits its FIN until an
// ack arrives — so after the FIN, ReceiveAll keeps answering duplicate FINs with fresh acks instead
// of walking away immediately (a receiver that vanished after the first FIN would doom the last
// packet's delivery). ReceiveAll stops accumulating payload at the FIN and returns once the link
// is closed (io.EOF). If the link closes before a FIN arrives, ReceiveAll returns an error
// wrapping io.EOF.
//
// An empty transfer (a lone FIN) returns a non-nil, empty slice.
func ReceiveAll(link Link) ([]byte, error) {
	expectedSeq := uint32(0)
	var lastAcked uint32
	haveLastAcked := false
	finSeen := false
	consecutiveCorrupt := 0
	buf := []byte{}

	ack := func(seq uint32) error {
		lastAcked, haveLastAcked = seq, true
		return sendAck(link, seq)
	}

	for {
		b, err := link.Next()
		if err != nil {
			if finSeen {
				return buf, nil
			}
			return nil, fmt.Errorf("link closed before the FIN packet arrived: %w", err)
		}
		pkt, err := DecodePacket(b)
		if err != nil {
			consecutiveCorrupt++
			if consecutiveCorrupt > 5 {
				return nil, fmt.Errorf("giving up after 5 consecutive corrupted packets: %w", err)
			}
			continue
		}
		consecutiveCorrupt = 0

		if finSeen {
			if haveLastAcked && pkt.Seq == lastAcked {
				_ = ack(pkt.Seq) // duplicate FIN: re-ack, never append
			}
			continue
		}

		switch {
		case pkt.Seq == expectedSeq:
			if pkt.Has(FlagFIN) {
				if err := ack(pkt.Seq); err != nil {
					return nil, err
				}
				finSeen = true
				expectedSeq++
				continue
			}
			if pkt.Has(FlagData) {
				buf = append(buf, pkt.Payload...)
			}
			if err := ack(pkt.Seq); err != nil {
				return nil, err
			}
			expectedSeq++
		case haveLastAcked && pkt.Seq == lastAcked:
			// Duplicate: the sender never saw our ack. Re-ack; do NOT append again.
			if err := sendAck(link, pkt.Seq); err != nil {
				return nil, err
			}
		default:
			// Out-of-order / unexpected packet: ignore silently.
		}
	}
}
