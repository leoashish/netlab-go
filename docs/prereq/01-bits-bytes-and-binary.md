# Prereq 01 — Bits, bytes and binary

Everything on the wire is bytes. Not structs, not strings, not JSON — **bytes**. If you can predict the exact bytes a program emits, you understand the protocol. This is the foundation Chapter 01 (and half the course) builds on.

## Bits and bytes

A **bit** is one binary digit (0 or 1). A **byte** (or octet) is 8 bits. One byte holds 256 values: `0x00`–`0xff` (0–255). You will see hexadecimal (`0x3f`, `0xAC`) everywhere in network code because 2 hex digits = exactly 1 byte; binary (`00111111`) almost never, because it's noisy.

A **nibble** is 4 bits (one hex digit). You need it for IPv4's header-length field and for WebSocket's frame header — both pack two nibbles into one byte.

## Positional number systems

Each position is worth the base raised to a power:

```
decimal: 4 2 1 → 4*100 + 2*10 + 1 = 421
hex:     0x1A3 → 1*256 + 10*16 + 3 = 419
binary:  0b1011 → 1*8 + 0*4 + 1*2 + 1 = 11
```

Reverse direction: divide by the base repeatedly, collect remainders. `go vet` and `0x` prefixes are your friends; `strconv.ParseUint` does the work in practice, but you should be able to read `0b1100_0010` as `194` at a glance.

## Signed integers and two's complement

Network protocols almost never use Go ints; they use *width-defined* unsigned/signed fields. To negate a two's-complement number: **flip all bits, add 1**.

```
 5 = 0000 0101
~5 = 1111 1010
+1 = 1111 1011  = -5
```

Range for n bits: -2^(n-1) … 2^(n-1) - 1. 8-bit: -128…127. Sign extension (copying the sign bit into newly wider slots) is what Go does for you with int conversions — on the wire *you* do it by hand.

## Byte order (endianness) — the classic trap

The number `0x01020304` (hex bytes `01 02 03 04`) can be placed in memory two ways:

| Order | Bytes in memory | Used by |
|---|---|---|
| **Big-endian (BE)**, "network order" | `01 02 03 04` | IP, TCP, UDP, DNS, most internet protocols |
| **Little-endian (LE)** | `04 03 02 01` | x86/ARM, Apple silicon, most file formats |

Go's `encoding/binary` lets you pick (`binary.BigEndian`), and Chapter 01 makes you implement both orders yourself. Rule to remember: **all multi-byte integers on the network are big-endian** unless the spec says otherwise.

Memory trick: big-endian = "the big end comes first" (most significant byte first). "Network byte order" is always BE.

## Hex dumps, the universal debugging tool

When a connection misbehaves, `xxd` / Wireshark / `tcpdump -x` show you the bytes. A hexdump has three columns:

```
00000000  47 45 54 20 2f 20 48 54  54 50 2f 31 2e 31 0d 0a  |GET / HTTP/1.1..|
```

- offset in hex (`00000000`)
- 16 bytes per row, shown in hex (grouped 8 + 8 for readability)
- printable ASCII on the right (`.` for non-printable bytes like `0x0d` CR, `0x0a` LF)

Chapter 01's `HexDump` produces exactly this format, and you will read your own output to debug the HTTP chapter.

## Varints and zig-zag (protobuf-style)

Fixed 4- or 8-byte ints waste space for small values. A **varint** stores 7 bits per byte; the most significant bit is a *continuation flag*: `1` = more bytes follow, `0` = this was the last.

```
300 = 0b1_0101100  → 2 bytes: 0xAC 0x02
byte0: 1010 1100  (0xAC: MSB set → continue; low 7 bits 0101100)
byte1: 0000 0010  (0x02: MSB clear → stop; low 7 bits 0000010)
value = 0101100 | (0000010 << 7) = 300
```

Plain varint is terrible for negatives (they'd take 10 bytes), so signed values are mapped with **zig-zag**: `0→0, -1→1, 1→2, -2→3…` — the formula is `(n << 1) ^ (n >> 63)` for int64. Zig-zag folds the sign into the low bit so small-magnitude negatives stay small.

## Checksums

Before trusting any received bytes, verify they weren't corrupted in transit:

- **Internet checksum** (RFC 1071): sum all 16-bit big-endian words with wraparound carry, then take the one's complement. Used by IPv4 header, ICMP, UDP (optionally). Cheap, weak — catches accidents, not attackers, not all errors.
- **CRC-32** (IEEE 802.3, used by Ethernet, NTFS, ZIP): treat the payload as a big binary number and divide by a fixed polynomial; the remainder is the CRC. Much stronger burst-error detection than the internet checksum. Chapter 02 implements it bitwise from first principles.

CRC vs cryptographic hash: CRCs detect *accidental* corruption; they are trivially forgeable, so never use them for security. (TLS uses HMAC/AEAD — Chapter 12.)

## Delimiting messages on a byte stream

A TCP connection is just a byte stream — it has no message boundaries. If you write `{"a":1}{"b":2}` the reader can't tell where one JSON ends and the next begins. Two standard cures:

1. **Length-prefixing**: prefix each message with its size (`[4 bytes BE length][payload]`). Fast, binary-safe, needs a max-size cap to survive hostile lengths. Chapter 02 builds this as `FrameReader`/`FrameWriter`.
2. **Delimiters**: like HTTP's `\r\n\r\n` between header and body, or `\n` between lines. Human-friendly-ish, but you must escape/scan the payload.

## Practical checklist

- [ ] Convert hex ↔ decimal ↔ binary by hand for values < 256
- [ ] Explain why `0x01020304` becomes `04 03 02 01` in memory on your laptop
- [ ] Explain what a hexdump's offset column means
- [ ] Decode `0xAC 0x02` as a varint
- [ ] Say when you'd length-prefix vs delimit
