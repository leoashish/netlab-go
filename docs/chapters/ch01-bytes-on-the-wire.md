# Chapter 01 — Bytes on the wire: endianness, varints and hexdumps

| | |
|---|---|
| **Build** | `Uint16BE`…`PutUint64LE`, `AppendVarint`, `VarintLen`, `ParseVarint`, `AppendZigZag`, `ParseZigZag`, `HexDump` — `internal/wire/wire.go` |
| **Tests** | `internal/wire/wire_test.go` — 23 tests |
| **Run** | `go test ./internal/wire -run Ch01 -race -v` |
| **Prerequisites** | [prereq 01 — bits, bytes and binary](../prereq/01-bits-bytes-and-binary.md), [prereq 02 — Go essentials](../prereq/02-go-essentials.md) |
| **Time** | ~75 min |

## 1. Why this matters for network programming

A Go `uint32` is not "four bytes". It becomes four bytes only when you choose the **order** to put them in, and if you and the peer choose differently, everything downstream is silently wrong — a port becomes 20480, a length becomes four gigabytes, a packet is discarded by a firewall you cannot see. Nobody crashes; the data is just garbage. These are the hardest bugs to find, which is why every protocol spec writes down byte layouts to the last bit and why you are about to implement the conversions by hand once.

The other half of the chapter is about *seeing* bytes. When a connection misbehaves, your first move is not a debugger, it is a hexdump: print the bytes you sent, print the bytes you expected, diff them. `HexDump` is the tool you build here and use for the rest of the course.

Finally, fixed-width bytes are wasteful for small numbers. **Varints** (protobuf's LEB128) squeeze `300` into two bytes, and **zig-zag** makes small negative numbers just as cheap. Chapter 02 frames messages with a 4-byte big-endian length, Chapter 03 parses the IP header (all big-endian), and several later protocols put varinted fields inside their payloads. Everything starts here.

## 2. The lesson, step by step

### Step 1 — Endianness is a decision, not a property of the number

The value `0x01020304` needs four bytes. Two conventions exist:

```
value:          0x01020304

big-endian:     01 02 03 04      most significant byte FIRST      ("network byte order")
little-endian:  04 03 02 01      most significant byte LAST       (x86, ARM, your laptop)
```

"Most significant" means the byte that contributes the highest powers of 256: in `0x01020304`, `0x01` is worth `0x01000000`, and `0x04` is worth `0x04`. Big-endian puts the `1` first, little-endian puts it last. Both are correct — but only one matches the spec you are implementing.

Your CPU writes `uint32` values in its own order, so network code never gets to rely on memory layout; it converts explicitly at the boundary. Here is the real conversion, in runnable form (`go run main.go`):

```go
package main

import "fmt"

func main() {
	b := []byte{0x01, 0x02, 0x03, 0x04}

	be := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	le := uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24

	fmt.Printf("bytes:        % x\n", b)
	fmt.Printf("as BE:        0x%08x\n", be)
	fmt.Printf("as LE:        0x%08x\n", le)

	out := make([]byte, 4)
	out[0], out[1] = byte(be>>24), byte(be>>16)
	out[2], out[3] = byte(be>>8), byte(be)
	fmt.Printf("BE again:     % x\n", out)
}
```

Reading the shifts out loud: `Uint16BE(b)` is `b[0]<<8 | b[1]`; `Uint16LE(b)` is `b[0] | b[1]<<8`. Reading a *little*-endian number means the **first** byte is the least significant (the low bits of the result) — see the `le` line above. Once you internalise that, all six getters and six putters in this chapter are the same idea at three widths.

### Step 2 — Get the contract right: FIRST N bytes

The signatures all look like they own a slice, but they are careful about what they touch:

- Getters read `b[0]` … `b[N-1]` and **ignore everything after**. A TCP buffer is usually bigger than the field you are reading; you do not copy or reslice, you look at the prefix.
- Putters overwrite `dst[0]` … `dst[N-1]` and **leave the rest of `dst` alone**. A 1500-byte receive buffer is filled in pieces: the Ethernet header at offset 0, IP at 14, TCP at 34, payload after — one `PutUint16BE` writes two bytes into that layout without disturbing its neighbours.

That "first N, rest untouched" rule is exactly what the tests check with sentinel bytes (`0xAA`) after the field.

### Step 3 — Why network order is big-endian

"Network byte order" is just a name for big-endian, fixed in RFC 1700 and used by IP, TCP, UDP, DNS, and most of the internet. Historically it came from big-endian hardware (and from protocols wanting a canonical order); practically, BE is also what humans read best in a hexdump: the leftmost byte in the dump is the most significant byte of the field. Parsing an IP header top-to-bottom is a single left-to-right pass over the bytes.

Your machine is almost certainly little-endian, so `encoding/binary` exists to make this explicit (`binary.BigEndian`, `binary.LittleEndian`). You will use it when convenient later, but now you write it by hand: reproducing a byte layout from a spec is a core skill, and it is how you will spot endianness bugs in other people's code.

### Step 4 — Varints: 300 on the wire

Fixed 4-byte fields are fine for headers, but compact encodings matter for the payloads. A **varint** stores 7 value bits per byte; the byte's most significant bit says "more follows". Least significant group first.

Decode `300` step by step:

```
300 decimal = 0b1_0010_1100        (9 bits)

split into 7-bit groups, LEAST significant first:
    group 0 (bits 0..6):   010 1100
    group 1 (bits 7..8):   000 0010

byte 0 = 1|010 1100 = 0xAC     MSB=1 -> more bytes follow
byte 1 = 0|000 0010 = 0x02     MSB=0 -> stop

on the wire:  AC 02
decoding:     0101100 | (0000010 << 7)
            = 0x2C + 0x100
            = 300  ✓
```

Encoding is a loop: while `v >= 0x80`, emit `byte(v)|0x80` and `v >>= 7`; then emit the final `byte(v)`. That is `AppendVarint`. `VarintLen` is the same loop counting instead of emitting — useful when a protocol has a length-prefix field that only depends on the encoded size.

Decoding is the mirror image, and it must handle two failure modes, which are *different* and lead to different caller behaviour:

- **Short input** (`80` at the end of the buffer): the value might be complete with more bytes, so this is `io.ErrUnexpectedEOF` — "read more and try again". A TCP read handing you half a varint is normal, not an error.
- **Overflow** (`ff ff ff ff ff ff ff ff ff 01` is already the largest uint64): an encoding that needs an 11th byte, or whose 10th byte carries more than one value bit, can never be a uint64. That is `ErrVarintOverflow` — "this input is corrupt or hostile; stop".

64 bits split into 7-bit groups give 10 bytes (9×7 = 63 bits, plus one bit in byte 10). That is why "10 bytes" and "the 10th byte may only contain bit 63" are the two overflow rules.

### Step 5 — Zig-zag: negatives should not cost 10 bytes

A plain varint of `-1` is the varint of `0xFFFFFFFFFFFFFFFF`: **ten bytes of 0xff plus a final 0x01**. Terrible. The fix is to map signed values onto unsigned ones so that small magnitudes — positive *and* negative — stay small:

```
  0 -> 0        -1 -> 1        1 -> 2        -2 -> 3        2 -> 4
```

The formula is `(v << 1) ^ (v >> 63)` (arithmetic shift: for negative `v`, `v>>63` is all ones, which flips every bit of `v<<1`). Decoding inverts it: `(z >> 1) ^ -(z & 1)`. This is exactly protobuf's `sint32`/`sint64`. Note what it does *not* do: it does not make negative numbers small in absolute terms, it makes the pair `-n`/`n` adjacent in the encoding, so a range like `[-100, 100]` still fits in two bytes per value.

### Step 6 — Reading a hexdump

`HexDump` produces `hexdump -C` output. Take one line and split it into its three columns:

```
00000010  48 6f 73 74 3a 20 65 78  61 6d 70 6c 65 2e 63 6f  |Host: example.co|
^^^^^^^^  ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^  ^^^^^^^^^^^^^^^^^
offset    bytes, 16 per row (8 + 8, the extra         printable ASCII:
0x10=16   space between the halves is for humans)      '.' when not 0x20..0x7e
```

Rules you will rely on when debugging with this tool:

- the offset is the byte index of the **first** byte of the row, so you can compare two dumps of the same data by aligning offsets;
- 16 bytes per row is a convention that matches terminal width and makes offsets clean powers of two;
- the gutter is a fast sanity check: readable protocol text (HTTP verbs, DNS labels, a DNS name) shows up on the right;
- a short last row is **padded** so its gutter starts in the same column as every other row — that is why you can scroll a dump vertically and trust the alignment;
- every line, including the last, ends in `\n`.

### Step 7 — Fixed versus varint: choose per field

| | Fixed-width (BE usually) | Varint |
|---|---|---|
| Length | constant, known from the spec | 1–10 bytes, depends on the value |
| Random access | jump to byte 42, read 4 bytes | must scan from the start |
| Cost of a small value | always N bytes | 1 byte |
| Cost of a huge value | always N bytes | up to 10 bytes for 64 bits |
| Use for | header fields, ports, lengths, checksums, anything you must find without scanning | compact payload fields, IDs, counts, deltas |

A protocol can mix both: Chapter 02's frame format is `[4-byte BE length][payload]` (fixed — a reader must know how many bytes to read next), while a payload full of small numbers may be varinted. Chapter 03's IPv4 header is pure fixed-width BE, including the 16-bit total length and the 32-bit addresses.

## 3. Your challenge

Implement everything in `internal/wire/wire.go`. The doc comments restate each contract; the tests are the fine print. Checklist:

- six getters, six putters: first N bytes in, first N bytes out, nothing else touched;
- `AppendVarint` / `VarintLen` agree, max 10 bytes;
- `ParseVarint` returns `(value, bytesConsumed, nil)` on success, `io.ErrUnexpectedEOF` when the input is cut short, `ErrVarintOverflow` when the value cannot fit in 64 bits; on error return `(0, 0, err)`;
- zig-zag round-trips `math.MinInt64`…`math.MaxInt64`, and small negatives stay short;
- `HexDump` matches the documented format exactly — the tests compare golden strings byte for byte.

```bash
go test ./internal/wire -run Ch01 -race -v
```

## 4. Hints

<details><summary>Hint 1 — build getters/putters from shifts, not from copying</summary>

```go
func Uint32BE(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func PutUint32BE(dst []byte, v uint32) {
	dst[0] = byte(v >> 24)
	dst[1] = byte(v >> 16)
	dst[2] = byte(v >> 8)
	dst[3] = byte(v)
}
```

LE is the same shape with the shift order reversed. For 64-bit, a loop `v = v<<8 | uint64(b[i])` keeps it readable. Do **not** reach for `encoding/binary` here — the point is to write it once yourself.
</details>
<details><summary>Hint 2 — the varint loops</summary>

```go
for v >= 0x80 {                 // something above the low 7 bits remains
	dst = append(dst, byte(v)|0x80)   // low 7 bits + continuation bit
	v >>= 7
}
dst = append(dst, byte(v))      // last byte: MSB is 0
```

Decoding: keep an index `i`, do `v |= uint64(b[i]&0x7f) << (7 * uint(i))`, stop when `b[i]&0x80 == 0` and return `i+1`. If the loop runs off the end of the slice, the input was short.
</details>
<details><summary>Hint 3 — the two varint errors, and where they come from</summary>

Track the index. When you are about to read byte `i`:

- `i >= len(b)` → `io.ErrUnexpectedEOF` (the value was never terminated);
- `i == 9` (the 10th byte, zero-based index 9) and the byte is greater than `0x01` → `ErrVarintOverflow`. It may only carry bit 63; a continuation bit or any other value bit means the number needs more than 64 bits.

Both error returns should be `return 0, 0, err`, matching the test expectations.
</details>
<details><summary>Hint 4 — HexDump: pad the hex area, not the gutter</summary>

Iterate rows of 16 (`for off := 0; off < len(b); off += 16`), compute `row := b[off:min(off+16, len(b))]`. Print `%08x  ` for the offset, then always loop `i := 0..15`: real byte → `%02x ` (trailing space), missing byte → three spaces; after `i == 7` add one extra space. Then one space and the `|...|` gutter (`c >= 0x20 && c <= 0x7e` ? `c` : `'.'`), then `\n`. Because the loop always runs all 16 positions, short rows are padded automatically. Remember the empty input case returns `""`, before any of this.
</details>

## 5. Stretch challenges

1. **Bit-reverse a byte.** Write `func ReverseByte(b byte) byte` that returns the byte with its bit order reversed (`0x01 -> 0x80`, `0x0f -> 0xf0`, `0x00 -> 0x00`). Do it with a loop first; then think about whether a 16-entry nibble lookup table is worth it.
2. **Hexdump in IPv6 shape.** Write `HexDumpWords(b []byte) string` that groups bytes in 2-byte units printed as `%04x`, eight units per row (like `hexdump -C` for a `[8]uint16`). Where does the 16-bit BE getter fit? Then try an "IPv6 address line": 16 bytes, four groups of four hex digits separated by `:` — compare with `netip.Addr` output.
3. **Decode by hand.** Without a computer, decode the varint `E5 8E 26` (this is the example from the protobuf documentation). Split each byte, mask off the continuation bit, reassemble. Then confirm with a test that `ParseVarint([]byte{0xE5, 0x8E, 0x26})` returns exactly what you computed and `n == 3`.
4. **Add `AppendVarint32`.** Implement `AppendVarint32(dst []byte, v uint32) []byte` and `ParseVarint32(b []byte) (uint32, int, error)`. A `uint32` needs at most **5** bytes; what is the overflow rule for the 5th byte, and what does the error name have to be to make it distinct from `ErrVarintOverflow`? Then rewrite the 64-bit versions in terms of the 32-bit ones (hint: two halves).

## 6. Reflect

- Why do the getters read the *first* N bytes of a slice instead of requiring the slice to be exactly N long?
- `io.ErrUnexpectedEOF` and `ErrVarintOverflow` both mean "this varint did not decode". Why does a stream reader treat them completely differently?
- Suppose you wrote every field of an IPv4 header little-endian but parsed it back little-endian too. Your unit tests pass. Which real-world interaction breaks, and how would you notice?
- Why does the varint format store the least significant group first, and the hexdump store the most significant byte first? (They are both "how you would write the number"; the first is about incremental decoding, the second about readability.)
- `VarintLen(300) == 2` without building anything — why can a protocol need that number before it has the value?
- When is the compactness of a varint *not* worth its cost? Think of a fixed-size table of 10,000 offsets you need to index directly.
