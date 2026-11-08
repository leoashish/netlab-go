# Chapter 02 — Checksums and framing: the Internet checksum, CRC-32 and length-prefixed frames

| | |
|---|---|
| **Build** | `InternetChecksum`, `InternetChecksumVerify`, `CRC32IEEE`, `AppendFrame`, `ParseFrame`, `FrameReader.ReadFrame`, `FrameWriter.WriteFrame` — `internal/frame/frame.go` |
| **Given** | `ErrFrameTooLarge`, `DefaultMaxFrame`, the `FrameReader`/`FrameWriter` types and their constructors |
| **Tests** | `internal/frame/frame_test.go` — 21 tests |
| **Run** | `go test ./internal/frame -run Ch02 -race -v` |
| **Prerequisites** | [prereq 01 — bits, bytes and binary](../prereq/01-bits-bytes-and-binary.md) (endianness, checksums, framing), [prereq 02 — Go essentials](../prereq/02-go-essentials.md) (`io.Reader`, `io.ReadFull`, sentinel errors) |
| **Time** | ~90 min |

## 1. Why this matters

Every byte you read from a socket has survived a trip through memory, the kernel, a NIC, cables, radios and a dozen machines. Two questions decide whether you can *use* what arrived:

1. **Is it intact?** Bits flip (a bad cable, cosmic rays, a buggy driver); sometimes the corruption is exactly the kind that turns a length into 4 GiB. A **checksum** lets the receiver notice and drop the damage instead of parsing garbage.
2. **Where does the message end?** A TCP connection is a **byte stream**, not a sequence of messages. The network promises the bytes, in order — it says nothing about the boundaries between your `Write` calls. Without framing, a reader literally cannot tell where one message ends and the next starts.

Both problems have classic, small, deceptively subtle solutions: the **Internet checksum** (RFC 1071, in every IP/ICMP/TCP/UDP header), **CRC-32** (Ethernet's frame check sequence, every ZIP/gzip/PNG file) and **length-prefixed framing** (TLS records, HTTP/2 frames, WebSocket). And one warning before we start: **checksums detect accidents, not attackers.** A CRC is a parity trick with good manners; it is trivially forgeable. If you need "this came from who it claims", that is a MAC/signature — Chapter 12 (TLS).

## 2. The lesson, step by step

### Step 1 — why TCP forces you to invent message boundaries

Write two 100-byte messages back to back, and the receiver may see one 200-byte `Read`, or 200 one-byte `Read`s, or anything between. `Write` calls are *not* message boundaries and TCP never preserves them (UDP datagrams do — that is its one advantage). The fix is to put the boundary *in the data*:

```
| 4 bytes big-endian length | payload                |
| 00 00 00 03               | 'a' 'b' 'c'            |
```

`AppendFrame` writes exactly that; `ParseFrame` reads the first frame out of a buffer and reports how many bytes it ate (`n`), so `b = b[n:]` walks a concatenation. On a stream (where you can't wait for "the whole buffer"), `FrameReader` must first read **exactly 4 bytes** of header — never assume one `Read` gives you 4 — then exactly `length` bytes of payload. `io.ReadFull` is the tool: it keeps calling `Read` until the buffer is full or the stream fails. The tests feed `FrameReader` an adversarial reader that returns **one byte per call**; the frames must still come out intact.

### Step 2 — the Internet checksum, computed by hand

Rule (RFC 1071): read the bytes as big-endian 16-bit words, add them with **end-around carry**, return the **one's complement** (`~`, i.e. flip all 16 bits).

RFC 1071's example bytes, computed line by line:

```
bytes:   00 01    f2 03    f4 f5    f6 f7
words:   0x0001   0xf203   0xf4f5   0xf6f7

sum = 0x0001
sum = 0x0001 + 0xf203 = 0xf204
sum = 0xf204 + 0xf4f5 = 0x1e6f9          ← 17 bits: a carry left bit 15
      end-around carry: 0x1e6f9 = 0xe6f9 + 0x10000,
      and 0x10000 ≡ 0x0001 in one's-complement 16-bit arithmetic
      (because 0xffff ≡ 0), so fold: 0xe6f9 + 0x0001 = 0xe6fa
sum = 0xe6fa + 0xf6f7 = 0x1ddf1          ← carry again
      fold: 0xddf1 + 0x0001 = 0xddf2
one's complement: ~0xddf2 = 0x220d       ← the checksum
```

Verification is the *same* computation with a different expected answer. The sender stores `~sum(data)`; the receiver sums **all** words, checksum included: `0xddf2 + 0x220d = 0xffff`, and `~0xffff = 0x0000`. So:

- `InternetChecksum(b)` — compute;
- `InternetChecksumVerify(b)` — just `InternetChecksum(b) == 0x0000` (the packet carries its own checksum field).

### Step 3 — odd lengths, and a padding gotcha

If the byte count is odd, the last byte forms a word with a zero pad. RFC 1071 puts the pad on the **low** side: byte `f6` becomes the word `0xf600` (`[f6, 00]`), *not* `0x00f6`. Example — the RFC bytes minus the last one:

```
00 01 f2 03 f4 f5 f6  →  words 0001, f203, f4f5, f600
0x0001 + 0xf203 = 0xf204
0xf204 + 0xf4f5 = 0x1e6f9 → 0xe6fa
0xe6fa + 0xf600 = 0x1dcfa → 0xdcfb
~0xdcfb = 0x2304
```

The gotcha is on the **packet layout** side: if you append a 2-byte checksum as a *trailer* after odd-length data, insert the zero pad **between data and checksum**. Otherwise the receiver's 16-bit words run `[lastByte, csHi]`, `[csLo, pad]` while the sender summed `[lastByte, pad]`, `[csHi, csLo]` — different words, checksum fails. (When the checksum has a fixed, even-aligned position — IPv4's header field, UDP's checksum field — the layout prevents this automatically. The test shows both the odd and even cases.)

### Step 4 — what the Internet checksum cannot do

It is cheap (one add per word) and detects every **single**-bit error, but it is not magic:

| Blind spot | Why | Test |
|---|---|---|
| **Reordering** of 16-bit words | addition is commutative; the sum does not care what came first | `..._BlindSpotsAreReal` |
| Inserting/deleting `0x00 0x00` | adding zero changes nothing | (same idea) |
| **Cancelling flips**: set a bit in one word (+2ᵏ) and clear the same-weight bit in another (−2ᵏ) | the changes cancel; even two single-bit errors slip through | `..._BlindSpotsAreReal` |
| **Anything deliberate** | an attacker recomputes the checksum after editing | by construction |

Reordering is usually caught by a layer above (TCP sequence numbers); the last row is why TLS uses MACs/AEADs. Still, for "did a router drop a bit in this header?", 16 bits of one's-complement sum are a great deal for the price.

### Step 5 — CRC-32: polynomial division in a shift register

A **CRC** treats the message's bits as the coefficients of a huge polynomial `M(x)` over GF(2) — where addition *is* XOR, so there are no carries. Appending 32 zero bits multiplies by `x³²`. The CRC is the **remainder** of `M(x)·x³²` divided by a fixed generator polynomial `G(x)`. IEEE 802.3's generator is

```
x³² + x²⁶ + x²³ + x²² + x¹⁶ + x¹² + x¹¹ + x¹⁰ + x⁸ + x⁷ + x⁵ + x⁴ + x² + x + 1
```

which as a bit pattern is `0x04C11DB7`. Hardware divides MSB-first; software uses the **reflected** form, `0xEDB88320` (all bits reversed), so the register shifts *right* and we test the *low* bit:

```go
crc := ^uint32(0)             // init to 0xFFFFFFFF
for _, v := range b {
    crc ^= uint32(v)          // bring the byte in
    for i := 0; i < 8; i++ {  // 8 rounds: one per bit
        if crc&1 != 0 {
            crc = crc>>1 ^ 0xEDB88320   // subtract (XOR) G if the bit leaving is 1
        } else {
            crc >>= 1
        }
    }
}
return ^crc                   // final XOR
```

Why the init/final XORs: initialising to all ones makes a run of **leading zero** bytes change the CRC, and the final XOR makes **trailing zero** bytes matter too — otherwise `"a"`, `"\x00a"` and `"a\x00"` could collide on some generators. Result: a *position-sensitive* function that detects every burst error up to 32 bits long, and (for messages of ordinary frame size, up to ~1145 bytes) all 1- and 2-bit errors. It agrees with `hash/crc32.ChecksumIEEE`, which the random-corpus test checks 200 times.

### Step 6 — the frame format, and a hostile length field

`ParseFrame` must check things **in this order**, and the order is the lesson:

1. `len(b) < 4` → `io.ErrUnexpectedEOF` (the caller gave us a truncated buffer);
2. declared length `> DefaultMaxFrame` → `ErrFrameTooLarge` — **before** trusting or slicing the length. A 32-bit length field can claim 4 294 967 295 bytes; `make([]byte, 4<<30)` before you realise it is a lie is how a small program dies of OOM from one hostile packet;
3. `len(b) < 4 + declared` → `io.ErrUnexpectedEOF`.

`DefaultMaxFrame` is 16 MiB: far above anything this course sends, far below "please allocate 4 GiB". Exactly 16 MiB is allowed — the cap is `>`, not `>=`. Note that `AppendFrame` deliberately does **not** enforce the cap (it has no error to return; the length field can't even describe a `> MaxUint32` payload): the cap lives where it protects against *remote* data — `FrameReader`/`ParseFrame` on the way in, and `FrameWriter` before sending. `FrameWriter.WriteFrame` writes the 4 header bytes and the payload as **two** `Write` calls; if the second fails, a partial frame is on the wire, and the peer's reader will tell you with `io.ErrUnexpectedEOF`.

### Step 7 — EOF vs truncation: the reader's contract

`FrameReader.ReadFrame` must distinguish three ways a stream stops:

- **`io.EOF`** — zero bytes of the next frame arrived: a clean end at a frame boundary. A `for` loop over frames ends normally.
- **`io.ErrUnexpectedEOF`** — the next frame *started* (part of the header, or the whole header, arrived) but the rest never did. The connection died mid-frame; the partial bytes are garbage.
- **`ErrFrameTooLarge`** — the header is complete and declares a size over the cap. Only 4 bytes were consumed; the stream is now out of sync, so abandon the reader (close the connection).

`io.ReadFull` gives you the first two almost for free: on the 4-byte header it returns `io.EOF` only when it read **nothing** and `io.ErrUnexpectedEOF` for a partial header. The one case you must handle yourself is the payload: if it read zero payload bytes when the header promised some, `io.ReadFull` says `io.EOF` — but that is a truncated frame, so convert it to `io.ErrUnexpectedEOF`. The tests cut a real frame at every interesting offset and demand exactly these distinctions.

## 3. Your challenge

Implement the seven functions in `internal/frame/frame.go` (the doc comments restate each contract):

```go
func InternetChecksum(b []byte) uint16
func InternetChecksumVerify(b []byte) bool
func CRC32IEEE(b []byte) uint32
func AppendFrame(dst, payload []byte) []byte
func ParseFrame(b []byte) (payload []byte, n int, err error)
func (f *FrameReader) ReadFrame() ([]byte, error)
func (f *FrameWriter) WriteFrame(payload []byte) error
```

Run `go test ./internal/frame -run Ch02 -race -v`: **21 red tests** fail until every contract holds. The tests never need the network — everything is buffers and byte slices — and they never flake: the "random" corpora use fixed seeds.

## 4. Hints

<details><summary>Hint 1 — the checksum loop, end-around carry included</summary>

```go
var sum uint32
for len(b) >= 2 {
    sum += uint32(b[0])<<8 | uint32(b[1])   // big-endian word
    sum = sum&0xffff + sum>>16              // fold NOW: the carry re-enters at bit 0
    b = b[2:]
}
if len(b) == 1 {
    sum += uint32(b[0]) << 8                // odd byte: HIGH byte, low byte is the pad
}
for sum>>16 != 0 {                          // a final fold (folding each step also works)
    sum = sum&0xffff + sum>>16
}
return ^uint16(sum)
```

Folding each iteration keeps `sum` under 17 bits, so no wider accumulator is needed and no input length can overflow it.
</details>

<details><summary>Hint 2 — verify and the trailer padding</summary>

`InternetChecksumVerify(b)` is a one-liner: `return InternetChecksum(b) == 0x0000`. Appending `~sum` makes the new total `sum + ~sum = 0xffff ≡ 0`, and `~0xffff = 0`. For an odd-length trailer packet, append `0x00` **before** the two checksum bytes (see Step 3).
</details>

<details><summary>Hint 3 — CRC-32 in eight lines</summary>

The loop from Step 5: `crc := ^uint32(0)`; per byte `crc ^= uint32(v)`; eight rounds of `if crc&1 != 0 { crc = crc>>1 ^ 0xEDB88320 } else { crc >>= 1 }`; return `^crc`. If the random-corpus test disagrees, the usual suspects are: forgot the final XOR, used `0x04C11DB7` (unreflected) with a right shift, or initialised to 0.
</details>

<details><summary>Hint 4 — framing: read exactly, check before you allocate</summary>

```go
var hdr [4]byte
if _, err := io.ReadFull(f.r, hdr[:]); err != nil { return nil, err } // io.EOF only if 0 bytes read
size := binary.BigEndian.Uint32(hdr[:])
max := f.max
if max == 0 { max = DefaultMaxFrame }
if size > max { return nil, ErrFrameTooLarge }        // BEFORE make()
payload := make([]byte, size)
if _, err := io.ReadFull(f.r, payload); err != nil {
    if err == io.EOF { err = io.ErrUnexpectedEOF }    // header promised payload: not a clean boundary
    return nil, err
}
return payload, nil
```

`ParseFrame` orders its checks: length ≥ 4 → cap → complete payload. Use `encoding/binary.BigEndian` for every 4-byte integer.
</details>

## 5. Stretch challenges

1. **Table-driven CRC.** Generate a `[256]uint32` table from the bitwise routine (the `crc32.MakeTable` trick), then rewrite `CRC32IEEE` to be byte-at-a-time (`crc = table[byte(crc)^v] ^ crc>>8`). Prove both implementations agree on the random corpus, and benchmark `go test ./internal/frame -bench .` to see the speed-up.
2. **CRC-framed mode.** Add `AppendFrameCRC(dst, payload []byte) []byte` and `ParseFrameCRC(b []byte) (payload []byte, n int, err error)`: frame = `[4-byte length][payload][4-byte CRC32 of the length+payload]`. A flipped bit anywhere (test every byte!) must come back as an error — the length must be checksummed too, or a corrupt length misdescribes where the next frame starts.
3. **Fuzz the parser.** Write `FuzzParseFrame` (`go test -fuzz FuzzParseFrame -fuzztime 30s`) that feeds arbitrary bytes and asserts: no panic; the error, when non-nil, is exactly `io.ErrUnexpectedEOF` or `ErrFrameTooLarge`; and when it is nil, `n == 4+len(payload)`. Seed the corpus with real frames. Then try the same for `FrameReader` over `bytes.NewReader`.
4. **Reordering, caught.** Extend the frame format with a 4-byte big-endian sequence number inside the payload; a reader that receives sequence 2 before 1 must buffer or reject. Compare with what TCP does.

## 6. Reflect

- Checksums are "detect accidents, not attackers". Name one attack that an Internet-checksum-protected protocol still allows, and the primitive that fixes it.
- `ParseFrame` returns `n` as well as the payload. What breaks if you ignore `n` and always do `b = b[4+len(payload):]`? (Hint: empty frames, and buffers that are *longer* than one frame.)
- Why must the cap check happen before `make([]byte, size)` rather than after? What is the worst the sender can do to you if you get the order wrong?
- `FrameReader` returns `io.EOF` for a clean boundary and `io.ErrUnexpectedEOF` mid-frame. Why is conflating them a data-corruption bug, not a cosmetic one?
- `AppendFrame` appends to `dst` and returns the grown slice; why does the pattern `dst = frame.AppendFrame(dst, p)` matter inside a send loop? (See `append` and backing arrays in prereq 02.)
- CRC-32 is strong for accidents and useless for security. Given that TLS still *has* a record length field and its own integrity tag, what job does each layer do?
