# Chapter 05 — Building reliability on top of loss: stop-and-wait ARQ

| | |
|---|---|
| **Build** | `EncodePacket`, `DecodePacket`, `SendAll`, `ReceiveAll` — `internal/arqx/arqx.go` |
| **Given** | `internal/arqx/memlink.go` (a deterministic in-memory lossy link), `internal/arqx/spec.go` (`Link`, `Packet`, flags, errors) |
| **Tests** | `internal/arqx/arqx_test.go` — 13 tests (+3 subtests) |
| **Run** | `go test ./internal/arqx -run Ch05 -race -v` |
| **Prerequisites** | Chapter 02 (binary encoding), Chapter 04 (timeouts and retries); [prereq 04 — TCP](../prereq/04-how-networks-work.md), [prereq 05 — RTT/BDP](../prereq/05-networking-vocabulary.md) |
| **Time** | ~2 h |

## 1. Why this matters

Every network you will ever program against **loses and corrupts packets** — Wi-Fi interference, congested routers dropping tail packets, a cosmic ray in a RAM module. Link layers hand you this choice: *drop it and pretend nothing happened*, or *fix it for the app*. Ethernet retries inside a LAN, but end-to-end across the internet, **only the endpoints can be sure** — which is why reliability lives in the transport layer, i.e. in TCP, i.e. in *your* code if you use UDP.

This chapter strips TCP down to its four irreducible organs and rebuilds them:

| TCP ingredient | What it does | What you build |
|---|---|---|
| Sequence numbers | Detect loss, duplication, reordering | `Seq uint32`, fragments start at 0 |
| ACKs | "I have everything up to N" | `Ack uint32` + `FlagAck` |
| Retransmission + timer | Recover from loss (data *and* acks) | 50 ms timeout, ≤ 10 transmissions, `ErrTransferFailed` |
| Checksum | Detect corruption (never trust mangled bytes) | CRC-32 over the whole packet |

What is *not* here: flow control, congestion control, connection setup, TIME_WAIT — real TCP has them; the stretch challenges gesture at why you need them.

## 2. The lesson, step by step

### Step 1 — the wire format (learn it once, it's tiny)

```
[seq uint32 BE][ack uint32 BE][flags uint8][length uint16 BE][payload...][crc uint32 BE]
└──────────────── 11 bytes = HeaderSize ─────────────────┘            └─ 4 bytes ─┘
```

- **flags** · `FlagData` (0x01) · `FlagAck` (0x02) · `FlagFIN` (0x04) — may be OR-combined. `FlagFIN` carries **no payload**.
- **length** · payload bytes only (not header, not crc). `0..MaxPayload` (1024).
- **crc** · CRC-32 (IEEE, same polynomial as Ethernet FCS and zip) of **all preceding bytes** — header and payload together. One bit flip anywhere → CRC mismatch → the packet is **discarded silently** as if it never existed.
- Total on the wire = `HeaderSize + length + 4`. Encode rejects `len(Payload) > MaxPayload` with `ErrPacketTooLarge`; decode rejects too-short packets, length-field/size mismatches and CRC failures, all wrapping `ErrPacketCorrupted`.
- `Packet` in memory: `{Seq, Ack uint32; Flags uint8; Payload []byte}`.

### Step 2 — the lossy link you are given (`memlink.go`, do not modify)

`NewMemLinkPair(dropEvery, corruptEvery uint64) (a, b *MemLink)` wires two ends together. Loss is **deterministic**:

- `dropEvery = N` → every **Nth `Send` on that direction** silently vanishes. `0` = never.
- `corruptEvery = N` → every **Nth `Send` on that direction** has **one bit flipped**, at position `(count*7) % len`. CRC catches it with certainty.
- Counting is **per direction** and spans **data packets and acks alike** — so retransmissions shift the sequence of "which packet gets hit". Both directions use the same `N`, but count independently.
- `Send` copies your slice, **never blocks** (cap 4096; a full buffer drops silently, counted), returns `io.ErrClosedPipe` after `Close`.
- `Next()` **blocks**, returns a copy, and returns `io.EOF` once the peer closed and the queue drained. **Exactly one goroutine may call `Next` per link.**
- Counters per direction: `Drops()`, `Corrupted()`, `Sent()` — handy for asserting your test really exercised loss.
- `Close` is idempotent; datagrams already queued are **not** discarded.

Because the loss pattern is a pure function of the send counter, every test in this chapter is reproducible — same seed, same result, every run.

### Step 3 — the sender: one packet in flight

```
fragment data into ≤1024-byte chunks → packet i (Seq = i, FlagData)
after the last chunk (or immediately, if data is empty): FIN packet, Seq = #chunks
for every packet: encode → send → wait for its ack → (retransmit on timeout)
```

- An **ack** is valid iff `Flags & FlagAck != 0 && Ack == ourSeq`. Anything else — a data packet, a corrupted packet, an ack for a *different* seq — is ignored.
- **The timer.** `link.Next()` has no deadline, so run it in one dedicated goroutine feeding an ack channel, and `select { case res := <-acks: ...; case <-time.After(50ms): }` for each attempt. Never let two goroutines call `Next`.
- **10 transmissions** per packet (the first plus 9 retransmissions — the timer spans 50 ms each). Exhausted → `ErrTransferFailed` (wrap it with the seq).
- Corrupted packet in transit? The receiver drops it (Step 4), sends nothing, your timer fires, you retransmit. The sender never learns *why* there was no ack — **loss and delayed ack are indistinguishable** — and stop-and-wait doesn't care: it just tries again.

### Step 4 — the receiver: in-order delivery, dedup, silence on garbage

State: `expectedSeq` (starts 0) and `lastAcked`. Per packet:

| Packet | Action |
|---|---|
| Corrupt (bad CRC / length) | Drop silently; count it. 5 **consecutive** corrupts → give up with an error wrapping `ErrPacketCorrupted`. Any valid packet resets the count. **No global deadline** otherwise. |
| `Seq == expectedSeq`, FlagData | Append payload, **send ack(Seq)**, `expectedSeq++` |
| `Seq == expectedSeq`, FlagFIN | **send ack(Seq)**, stop accumulating, note `finSeen` |
| `Seq == lastAcked` (duplicate) | **Re-ack, do not append** — the sender never saw our ack |
| Anything else | Ignore silently |

Why duplicates exist at all: from the sender's chair, "my packet was dropped" and "your ack was dropped" look **identical** — silence either way. So it retransmits, and the receiver may see the same packet twice. If the receiver re-appended, every lost ack would corrupt the payload. Dedup by sequence number is not optional.

Why silence on corruption: if a mangled packet were **acked**, the sender would conclude bytes arrived that never did — silent data corruption, the worst outcome. If it were **NAKed**, every bit-flip would double the traffic on an already-sick link (and a NAK is itself a packet that can be lost). Dropping + timer is the simplest recovery that cannot lie. *Silence is not rude; silence is the protocol.*

### Step 5 — the FIN, and why the receiver lingers

After the last data packet, the sender transmits a **FIN** (`FlagFIN`, empty payload, Seq = #chunks) and waits for its ack **exactly as for data**.

That ack can be lost too. Then the sender retransmits the FIN — and its retransmissions must meet **a receiver that is still alive to re-ack them**. A receiver that returned the moment it acked its first FIN would leave those retransmissions unread, and the sender would burn all 10 attempts and report `ErrTransferFailed` on a perfectly delivered 4 KB payload. You can verify this yourself: make `ReceiveAll` return at the FIN and watch `TestCh05_DropEvery2_Transfer4KB_Succeeds` fail deterministically.

So this chapter's `ReceiveAll` semantics are:

1. Read packets until a FIN is received; **stop accumulating payload at the FIN**.
2. After the FIN, keep reading: re-ack any duplicate FIN, ignore everything else.
3. **Return the concatenated payload once the link is closed** (`Next` → `io.EOF`). If the link closes before any FIN arrives, return an error wrapping `io.EOF`.

This is the same gesture as TCP's linger/TIME_WAIT: *the side that says goodbye stays around a little to answer the last retransmissions*. It means every transfer test **closes both link ends** after `SendAll` returns — that is what releases the receiver.

Edge case — **empty data**: `SendAll(link, []byte{})` sends only the FIN (Seq 0). `ReceiveAll` returns 0 bytes (a non-nil `[]byte{}`). Documented, tested.

### Step 6 — a lost packet, frame by frame

```
sender                                       receiver
  | ─── DATA seq=0 ──────────────────────▶     |  CRC ok, in order → append, ack
  | ◀── ACK ack=0 ──────────────────────       |
  | ─── DATA seq=1 ───────────╳                |  dropped on the "wire"
  |          (50 ms … no ack …)                |
  | ─── DATA seq=1  (retransmit) ─────────▶    |  duplicate! re-ack, don't append
  | ◀── ACK ack=1 ──────────────────────       |
  | ─── FIN seq=2 ───────────────────────▶     |  in order → ack, finSeen
  | ◀── ACK ack=2 ───────────────╳             |  FIN-ack dropped!
  |          (50 ms … no ack …)                |
  | ─── FIN seq=2  (retransmit) ─────────▶     |  duplicate FIN → re-ack
  | ◀── ACK ack=2 ──────────────────────       |  ← transfer complete
  | ─── link.Close() ─────────────────────▶    |  EOF → ReceiveAll returns payload
```

Same story for a **corrupted ack**: CRC fails on the sender side → ignored → timer fires → retransmit → receiver re-acks (duplicate) → ack #2 arrives (clean or corrupt, one will pass within 10 attempts on any sane loss pattern).

### Step 7 — stop-and-wait is a terrible deal (on purpose)

Throughput ≈ one `MaxPayload` per RTT: **1024 B / 50 ms ≈ 20 KB/s** — on a real 100 ms transatlantic link, ~10 KB/s, with the link 99% idle. Stop-and-wait is the price of absolute simplicity and correctness. TCP pays it only in the edge case; it normally keeps a **window** of bytes in flight (the bandwidth-delay product — prereq 05) and uses cumulative/per-packet acks. That is the stretch challenge, and that is why TCP is fast on healthy links and merely functional on dying ones.

## 3. Your challenge

Implement the four functions in `internal/arqx/arqx.go`. `memlink.go` and `spec.go` are given — read them, don't edit them. The tests start **red** (they fail with `not implemented yet`); finish them one by one.

Things that bite:

- **Tests on lossy links take real time, by design** — `dropEvery=2` needs retransmissions (~0.6 s), `dropEvery=1` burns 10 × 50 ms before `ErrTransferFailed` (~0.5 s). If a test takes seconds, it is working, not hanging.
- The lossy tests assert **identical bytes** — if you append a duplicate payload, they fail loudly. Dedup is not decorative.
- `go test ./internal/arqx -run Ch05 -race -v` — `-race` is mandatory: this chapter's bugs are interleavings.
- 13 tests: packet codec (4), clean-link transfers (3), lossy transfers (4), the receiver's dedup unit test, FIN-only and empty-payload edges, error propagation.
- `testutil.NoLeaks` guards against goroutines outliving a test — every helper goroutine you spawn (the ack pump) must terminate when the link closes.

## 4. Hints

<details><summary>Hint 1 — do Encode/Decode first, test them in isolation</summary>

```go
buf := make([]byte, HeaderSize+len(p.Payload)+4)
binary.BigEndian.PutUint32(buf[0:4], p.Seq)   // Ack at [4:8], Flags at [8], length at [9:11]
crc32.ChecksumIEEE(buf[:HeaderSize+len(p.Payload)])
```
Decode order: length-of-slice check → length-field-vs-actual check → CRC check **last** (only trusted bytes pass). Copy the payload out (`append([]byte(nil), ...)`) — never return a slice aliasing the input.
</details>

<details><summary>Hint 2 — the sender's ack pump</summary>

One goroutine owns `link.Next()` forever, pushing `{packet, err}` into a channel the main sender reads. Unbuffered channel + a `done` channel the pump selects on alongside its send: after `SendAll` returns, it closes `done` and the test closes the link — pump sees `io.EOF`, exits, `NoLeaks` stays green. A pump blocked on a bare `ch <- pkt` with nobody reading is the classic leak here.
</details>

<details><summary>Hint 3 — "transfer fails with ErrTransferFailed even though everything looks delivered"</summary>

That's the **FIN-ack** being dropped (counters like `dropEvery=2` drop every 2nd datagram *including acks*, and retransmissions shift the parity into the FIN's ack). Your receiver is probably returning right after its first FIN ack, so the sender's retransmitted FINs get read by nobody. Re-read Step 5. The receiver must keep the link open, re-acking duplicate FINs, and the **test closes the link** to let it return.
</details>

<details><summary>Hint 4 — "the test hangs / the watchdog dumps goroutines"</summary>

A hang means somebody blocked forever: usually `Next()` called with no sender still alive, or a `close` you forgot so the receiver never sees `io.EOF`. The 90-second watchdog (`testutil.Main`) prints every goroutine's stack — look at the ones stuck in **your** code: `arqx.go` frames will tell you whether it's the sender's wait loop or the receiver's read loop. Also check you're not calling `Next` from two goroutines (MemLink forbids it — it *works* by accident most of the time, corrupting your reasoning, not your data... usually).
</details>

<details><summary>Hint 5 — counting in MemLink shifts with retransmissions</summary>

`dropEvery` counts **sends, including retransmits** — so adding more retransmissions *changes which positions are dropped*. Don't try to reason about "the 8th ack" statically; write the code, run `-v`, and print `Drops()`/`Corrupted()` from the test if you want to see the dance. The pattern is deterministic, not intuitive.
</details>

## 5. Stretch challenges

1. **Sliding window (Go-Back-N).** Extend the sender to keep a window of `W` unacked packets in flight; acks become cumulative (`Ack = highest contiguous seq + 1` is simplest) or per-packet. Keep `SendAllStopAndWait` for comparison. Measure throughput for W ∈ {1, 4, 16, 64} against the MaxPayload/RTT prediction.
2. **Reordering.** Add a `reorderEvery` to a MemLink variant that delays every 2nd datagram by one "slot" (swap adjacent datagrams). Stop-and-wait is already safe; make Go-Back-N survive it with an explicit reordering buffer — and watch head-of-line blocking: one lost packet stalls the whole window. (Prereq 05, §HOL blocking.)
3. **Sequence wraparound test.** Shrink the seq space for a test build (e.g. mask seqs to 4 bits behind a helper) and prove dedup survives the 15→0 wrap using modular comparison (`seq-lastAcked == 1`) instead of equality. The real protocol's 32-bit seq wraps once per 4 GB — irrelevant in practice, but the *logic* should be wrap-proof anyway.
4. **Weak checksum duel.** Swap CRC-32 for a 16-bit XOR checksum and find a two-bit flip that passes it. Then read why TCP/Ethernet/zip all chose real CRCs.
5. **NAK vs silence.** Add a `FlagNAK` experiment: receiver actively NAKs corrupted packets. Measure total packets sent on `corruptEvery=4` with and without NAK. (Spoiler: silence wins when loss is random, not when it's systematic.)
6. **Throughput graph.** A scratch `main` that moves 1 MB at various window sizes and loss rates; plot window vs goodput. This is the graph that explains why TCP's AIMD looks the way it does.

## 6. Reflect

- Why is **silence** the correct response to a corrupted packet — what does the sender conclude, and why can't it distinguish which of *its* mechanisms fired?
- Why must the receiver stay alive after the FIN? Name the TCP behavior this mirrors and what it costs.
- Why CRC on **every** packet instead of trusting the link (Ethernet already has FCS)? (Hint: the *end-to-end argument* — corruption happens in memory, drivers and switches too, not only on the wire.)
- What does stop-and-wait buy you, and at what price? When is it actually the right choice (e.g. in-order, tiny, latency-insensitive telemetry on a lossy radio)?
- Which two TCP mechanisms from prereq 04 are missing here, and why are they hard to bolt on afterwards rather than designed in from the start?
- If you deleted `ErrPacketCorrupted` handling and acked corrupted packets anyway, exactly what byte-sequence could emerge at the receiver? Walk it through.
