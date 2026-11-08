package arqx_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"netlab/internal/arqx"
	"netlab/internal/testutil"
)

// Chapter 05 — stop-and-wait ARQ over an unreliable link.   Run:  go test ./internal/arqx -run Ch05 -race -v

type transferResult struct {
	data []byte
	err  error
}

// runTransfer runs a full transfer: ReceiveAll in a goroutine, SendAll in the foreground,
// then closes both link ends so the receiver can finish (it stays alive after the FIN to
// re-ack retransmitted FINs, and returns when the link closes). Returns the received bytes
// and both errors. Deterministic: same seed data, same loss pattern, every run.
func runTransfer(t *testing.T, dropEvery, corruptEvery uint64, data []byte) (payload []byte, sendErr, recvErr error) {
	t.Helper()
	a, b := arqx.NewMemLinkPair(dropEvery, corruptEvery)

	result := make(chan transferResult, 1)
	go func() {
		got, err := arqx.ReceiveAll(b)
		result <- transferResult{got, err}
	}()

	sendErr = arqx.SendAll(a, data)
	if err := a.Close(); err != nil { // lets the receiver see io.EOF once the queue drains
		t.Fatalf("close send link: %v", err)
	}
	if err := b.Close(); err != nil { // lets a lingering ack pump exit
		t.Fatalf("close recv link: %v", err)
	}
	res := <-result
	return res.data, sendErr, res.err
}

func randBytes(seed int64, n int) []byte {
	rng := testutil.Rand(seed)
	buf := make([]byte, n)
	if _, err := rng.Read(buf); err != nil {
		panic(err)
	}
	return buf
}

func TestCh05_EncodeDecode_RoundTrip(t *testing.T) {
	testutil.NoLeaks(t)
	cases := []arqx.Packet{
		{Seq: 7, Ack: 6, Flags: arqx.FlagData, Payload: []byte("hello, lossy world")},
		{Seq: 0, Ack: 0, Flags: arqx.FlagData, Payload: randBytes(1, arqx.MaxPayload)},
		{Seq: 42, Ack: 41, Flags: arqx.FlagAck},
		{Seq: 99, Ack: 98, Flags: arqx.FlagFIN},
		{Seq: 0xFFFFFFFF, Ack: 0xFFFFFFFE, Flags: arqx.FlagData | arqx.FlagAck, Payload: []byte{0x00, 0x01, 0x7F}},
	}
	for i, want := range cases {
		enc, err := arqx.EncodePacket(want)
		if err != nil {
			t.Fatalf("case %d: EncodePacket: %v", i, err)
		}
		got, err := arqx.DecodePacket(enc)
		if err != nil {
			t.Fatalf("case %d: DecodePacket: %v", i, err)
		}
		if got.Seq != want.Seq || got.Ack != want.Ack || got.Flags != want.Flags || !bytes.Equal(got.Payload, want.Payload) {
			t.Fatalf("case %d: round trip = %+v, want %+v", i, got, want)
		}
		// mutating the decoded payload must not affect... just check sizes:
		if len(enc) != arqx.HeaderSize+len(want.Payload)+4 {
			t.Fatalf("case %d: wire size = %d, want %d", i, len(enc), arqx.HeaderSize+len(want.Payload)+4)
		}
	}
}

func TestCh05_EncodePacket_RejectsOversizedPayload(t *testing.T) {
	testutil.NoLeaks(t)
	if _, err := arqx.EncodePacket(arqx.Packet{Seq: 1, Flags: arqx.FlagData, Payload: make([]byte, arqx.MaxPayload)}); err != nil {
		t.Fatalf("payload of exactly MaxPayload must encode, got %v", err)
	}
	_, err := arqx.EncodePacket(arqx.Packet{Seq: 1, Flags: arqx.FlagData, Payload: make([]byte, arqx.MaxPayload+1)})
	testutil.ExpectError(t, err)
	if !errors.Is(err, arqx.ErrPacketTooLarge) {
		t.Fatalf("errors.Is(err, ErrPacketTooLarge) = false for %v", err)
	}
}

func TestCh05_DecodePacket_BitFlip_FailsCRC(t *testing.T) {
	testutil.NoLeaks(t)
	enc, err := arqx.EncodePacket(arqx.Packet{Seq: 3, Flags: arqx.FlagData, Payload: []byte("integrity matters")})
	if err != nil {
		t.Fatal(err)
	}
	for _, pos := range []int{0, 5, arqx.HeaderSize, len(enc) - 1} { // header, payload, crc itself
		corrupt := append([]byte(nil), enc...)
		corrupt[pos] ^= 0x01
		if _, err := arqx.DecodePacket(corrupt); !errors.Is(err, arqx.ErrPacketCorrupted) {
			t.Fatalf("flip at byte %d: err = %v, want ErrPacketCorrupted", pos, err)
		}
	}
}

func TestCh05_DecodePacket_TruncatedOrLengthMismatch(t *testing.T) {
	testutil.NoLeaks(t)
	enc, err := arqx.EncodePacket(arqx.Packet{Seq: 3, Flags: arqx.FlagData, Payload: []byte("some payload bytes")})
	if err != nil {
		t.Fatal(err)
	}
	truncated := enc[:len(enc)-1]
	if _, err := arqx.DecodePacket(truncated); !errors.Is(err, arqx.ErrPacketCorrupted) {
		t.Fatalf("truncated: err = %v, want ErrPacketCorrupted", err)
	}
	if _, err := arqx.DecodePacket([]byte{0xDE, 0xAD}); !errors.Is(err, arqx.ErrPacketCorrupted) {
		t.Fatalf("2-byte packet: err = %v, want ErrPacketCorrupted", err)
	}
	// length field says 100 payload bytes, packet carries 17: rejected even with a "plausible" crc region
	faked := append([]byte(nil), enc...)
	faked[10] = 100
	if _, err := arqx.DecodePacket(faked); !errors.Is(err, arqx.ErrPacketCorrupted) {
		t.Fatalf("length mismatch: err = %v, want ErrPacketCorrupted", err)
	}
}

func TestCh05_CleanLink_5KB_TransfersExactBytes(t *testing.T) {
	testutil.NoLeaks(t)
	data := randBytes(5, 5120) // 5 full 1024-byte chunks

	got, sendErr, recvErr := runTransfer(t, 0, 0, data)

	if sendErr != nil || recvErr != nil {
		t.Fatalf("sendErr = %v, recvErr = %v", sendErr, recvErr)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("received %d bytes, want %d bytes (identical)", len(got), len(data))
	}
}

func TestCh05_CleanLink_ExactlyMaxPayload_And1025(t *testing.T) {
	testutil.NoLeaks(t)
	for _, tc := range []struct {
		name string
		n    int
	}{
		{"exactly 1024, one full chunk", 1024},
		{"1025, full chunk plus one byte", 1025},
		{"1 byte", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testutil.NoLeaks(t)
			data := randBytes(int64(tc.n), tc.n)
			got, sendErr, recvErr := runTransfer(t, 0, 0, data)
			if sendErr != nil || recvErr != nil {
				t.Fatalf("sendErr = %v, recvErr = %v", sendErr, recvErr)
			}
			if !bytes.Equal(got, data) {
				t.Fatalf("received %d bytes, want %d bytes", len(got), len(data))
			}
		})
	}
}

func TestCh05_EmptyData_FinOnlyRoundTrip(t *testing.T) {
	testutil.NoLeaks(t)
	data := []byte{}
	got, sendErr, recvErr := runTransfer(t, 0, 0, data)
	if sendErr != nil || recvErr != nil {
		t.Fatalf("sendErr = %v, recvErr = %v", sendErr, recvErr)
	}
	if len(got) != 0 {
		t.Fatalf("received %d bytes, want 0", len(got))
	}
}

func TestCh05_DropEvery2_Transfer4KB_Succeeds(t *testing.T) {
	testutil.NoLeaks(t)
	data := randBytes(7, 4096)

	got, sendErr, recvErr := runTransfer(t, 2, 0, data)

	if sendErr != nil || recvErr != nil {
		t.Fatalf("sendErr = %v, recvErr = %v", sendErr, recvErr)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("received bytes differ (%d bytes, want %d identical)", len(got), len(data))
	}
}

func TestCh05_DropEvery3_ReceiverDeduplicates_DeliversExactBytes(t *testing.T) {
	testutil.NoLeaks(t)
	data := randBytes(8, 4096)

	got, sendErr, recvErr := runTransfer(t, 3, 0, data)

	if sendErr != nil || recvErr != nil {
		t.Fatalf("sendErr = %v, recvErr = %v", sendErr, recvErr)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("bytes differ (%d received, want %d): duplicates were appended?", len(got), len(data))
	}
}

func TestCh05_DropEvery3_CorruptEvery4_Transfer8KB_Succeeds(t *testing.T) {
	testutil.NoLeaks(t)
	data := randBytes(9, 8192)

	got, sendErr, recvErr := runTransfer(t, 3, 4, data)

	if sendErr != nil || recvErr != nil {
		t.Fatalf("sendErr = %v, recvErr = %v", sendErr, recvErr)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("bytes differ (%d received, want %d)", len(got), len(data))
	}
}

func TestCh05_DropEvery1_EverythingLost_TransferFailed(t *testing.T) {
	testutil.NoLeaks(t)
	a, b := arqx.NewMemLinkPair(1, 0) // every single datagram in both directions is dropped

	result := make(chan transferResult, 1)
	go func() {
		got, err := arqx.ReceiveAll(b)
		result <- transferResult{got, err}
	}()

	sendErr := arqx.SendAll(a, randBytes(10, 64))

	testutil.ExpectError(t, sendErr)
	if !errors.Is(sendErr, arqx.ErrTransferFailed) {
		t.Fatalf("sendErr = %v, want ErrTransferFailed", sendErr)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	res := <-result
	testutil.ExpectError(t, res.err)
	if !errors.Is(res.err, io.EOF) {
		t.Fatalf("recvErr = %v, want it wrapping io.EOF", res.err)
	}
	if a.Drops() != a.Sent() {
		t.Fatalf("a.Drops() = %d of a.Sent() = %d; every retransmission must have been dropped", a.Drops(), a.Sent())
	}
	if b.Drops() != 0 {
		t.Fatalf("b.Drops() = %d; the receiver should never have seen a valid packet to ack", b.Drops())
	}
}

func TestCh05_ReceiveAll_DuplicatesAreReacked_NotAppendedTwice(t *testing.T) {
	testutil.NoLeaks(t)
	a, b := arqx.NewMemLinkPair(0, 0)
	defer b.Close()

	payload := []byte("dup me, but only keep me once")
	encode := func(p arqx.Packet) []byte {
		buf, err := arqx.EncodePacket(p)
		if err != nil {
			t.Fatal(err)
		}
		return buf
	}
	dataPkt := arqx.Packet{Seq: 0, Flags: arqx.FlagData, Payload: payload}
	finPkt := arqx.Packet{Seq: 1, Flags: arqx.FlagFIN}
	for _, p := range []arqx.Packet{dataPkt, dataPkt, finPkt, finPkt} { // each datagram sent twice on purpose
		if err := a.Send(encode(p)); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Close(); err != nil { // EOF after the queued datagrams drain
		t.Fatal(err)
	}

	got, err := arqx.ReceiveAll(b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload = %q, want %q exactly once", got, payload)
	}
	// Every packet was acked — including both duplicates.
	for i, wantAck := range []uint32{0, 0, 1, 1} {
		raw, err := a.Next()
		if err != nil {
			t.Fatalf("ack %d: %v", i, err)
		}
		ack, err := arqx.DecodePacket(raw)
		if err != nil {
			t.Fatalf("ack %d: %v", i, err)
		}
		if !ack.Has(arqx.FlagAck) || ack.Ack != wantAck {
			t.Fatalf("ack %d = %+v, want Ack=%d with FlagAck set", i, ack, wantAck)
		}
	}
}

func TestCh05_SendAll_ClosedLink_ReturnsClosedPipe(t *testing.T) {
	testutil.NoLeaks(t)
	a, b := arqx.NewMemLinkPair(0, 0)
	defer b.Close() // lets the sender's ack pump observe io.EOF and exit
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}

	err := arqx.SendAll(a, []byte("hello?"))

	testutil.ExpectError(t, err)
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("err = %v, want io.ErrClosedPipe", err)
	}
}
