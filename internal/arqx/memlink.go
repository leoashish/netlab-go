// GIVEN: provided by the course. Do not modify.
package arqx

import (
	"io"
	"sync"
	"sync/atomic"
)

// MemLink is a deterministic, in-memory, UNRELIABLE datagram link between two endpoints.
// Use NewMemLinkPair to create a connected pair; packets sent on one end arrive on the
// other (or are dropped / corrupted, according to the configured loss).
//
// It is the test double that lets you build reliability (Chapter 05) without a network:
// no sockets, no goroutines of its own, no real time — every send is decided by a counter.
//
//	dropEvery    = N > 0 → every Nth Send on this direction is silently dropped. 0 = never.
//	corruptEvery = N > 0 → every Nth Send on this direction has ONE bit flipped
//	               (at position (count*7) % len(packet)). 0 = never.
//
// Counting is PER DIRECTION and covers EVERY Send — data packets AND acks — so the loss
// pattern is fully deterministic and reproduces exactly on every run.
//
// Wire format (documented in spec.go): [seq 4][ack 4][flags 1][length 2][payload][crc 4],
// all integers big-endian; crc = CRC-32 (IEEE) of all preceding bytes.
//
// Concurrency: Send and Close are safe to call from any goroutine. Next is NOT safe for
// concurrent use — exactly one goroutine may call Next on a link at a time (one receiver).
// The returned slice is a copy; the caller may keep or mutate it freely.
type MemLink struct {
	in           <-chan []byte // datagrams arriving from the peer
	out          chan []byte   // datagrams leaving this end (buffered, cap memLinkBuffer)
	dropEvery    uint64
	corruptEvery uint64

	mu        sync.Mutex // guards out and the send counter (Close must not race a Send)
	sent      uint64     // Send calls on this direction, 1-based (dropped ones count too)
	closed    atomic.Bool
	closeOnce sync.Once

	drops     atomic.Uint64 // datagrams silently dropped (dropEvery hit, or buffer full)
	corrupted atomic.Uint64 // datagrams whose single bit was flipped
}

const memLinkBuffer = 4096

// NewMemLinkPair returns a connected pair (a, b): a.Send delivers to b.Next and vice versa.
// dropEvery/corruptEvery apply to each direction independently (same N shared by both directions,
// but each direction counts its own sends — see the type docs).
func NewMemLinkPair(dropEvery, corruptEvery uint64) (a, b *MemLink) {
	aToB := make(chan []byte, memLinkBuffer)
	bToA := make(chan []byte, memLinkBuffer)
	a = &MemLink{in: bToA, out: aToB, dropEvery: dropEvery, corruptEvery: corruptEvery}
	b = &MemLink{in: aToB, out: bToA, dropEvery: dropEvery, corruptEvery: corruptEvery}
	return a, b
}

// Send queues p for delivery to the peer. p is copied, so the caller may reuse its buffer.
//
// Send never blocks. A datagram may silently vanish (that is the point of a lossy link):
//   - every dropEvery-th Send (per direction) is dropped, or
//   - the 4096-slot internal buffer is full (counted as a drop), or
//   - every corruptEvery-th Send has one bit flipped instead of being dropped.
//
// After Close, Send returns io.ErrClosedPipe. Send returns nil otherwise — there is no
// "delivery receipt" at this layer; that is what the ARQ protocol above it is for.
func (l *MemLink) Send(p []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed.Load() {
		return io.ErrClosedPipe
	}
	cp := append([]byte(nil), p...)
	l.sent++
	n := l.sent
	if l.dropEvery > 0 && n%l.dropEvery == 0 {
		l.drops.Add(1)
		return nil
	}
	if l.corruptEvery > 0 && n%l.corruptEvery == 0 && len(cp) > 0 {
		cp[(n*7)%uint64(len(cp))] ^= 0x01
		l.corrupted.Add(1)
	}
	select {
	case l.out <- cp:
	default: // buffer full: like any loss on this link, the datagram vanishes silently
		l.drops.Add(1)
	}
	return nil
}

// Next blocks until the next datagram arrives and returns a COPY of it.
// Once the peer has Closed and every queued datagram has been read, Next returns io.EOF.
// Next is not safe for concurrent use (see the type docs).
func (l *MemLink) Next() ([]byte, error) {
	d, ok := <-l.in
	if !ok {
		return nil, io.EOF
	}
	return append([]byte(nil), d...), nil
}

// Close shuts this end down: peers see io.EOF from Next once the queue drains,
// and this end's Send returns io.ErrClosedPipe. Close is idempotent.
// Close does NOT discard unread datagrams — Next keeps returning them until this end's queue is empty.
func (l *MemLink) Close() error {
	l.closeOnce.Do(func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.closed.Store(true)
		close(l.out)
	})
	return nil
}

// Drops reports how many datagrams this direction silently dropped (dropEvery or a full buffer).
func (l *MemLink) Drops() uint64 { return l.drops.Load() }

// Corrupted reports how many datagrams this direction had one bit flipped.
func (l *MemLink) Corrupted() uint64 { return l.corrupted.Load() }

// Sent reports how many Send calls were attempted on this direction (dropped ones included).
func (l *MemLink) Sent() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sent
}
