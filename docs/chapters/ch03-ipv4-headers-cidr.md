# Chapter 03 — Reading packets: IPv4 headers, CIDR math and longest-prefix routing

| | |
|---|---|
| **Build** | `ParseIPv4Header`, `IPv4Header.Marshal`, `IPv4HeaderChecksum`, `IPv4ToUint32`/`Uint32ToIPv4`, the CIDR helpers, and `RouteTable` — `internal/packet/packet.go` |
| **Tests** | `internal/packet/packet_test.go` — 24 tests |
| **Run** | `go test ./internal/packet -run Ch03 -race -v` |
| **Prerequisites** | [prereq 01 — Bits, bytes and binary](../prereq/01-bits-bytes-and-binary.md), [prereq 04 — How networks actually work](../prereq/04-how-networks-work.md), Chapters 01–02 (byte order, checksums) |
| **Time** | ~90 min |

## 1. Why this matters for network programming

Everything you have written so far talks to a **socket**, and the kernel makes the packets invisible. But the moment something goes wrong — a firewall drops you, a proxy rewrites your source address, a VPN splits your traffic, a load balancer sends you to the wrong backend — the answer is in the bytes *under* the socket API. Tools like `tcpdump`, Wireshark, and `xxd` show those bytes raw; this chapter teaches you to read them.

Three ideas carry most of the weight:

1. **The IPv4 header is 20 bytes of tightly packed fields** — some of them nibbles (4 bits), some 16-bit counts. Parsing from a byte slice with offsets is the same skill every binary protocol needs (DNS, TCP, WebSocket, QUIC).
2. **CIDR is bit arithmetic, not string arithmetic.** "Is `10.1.2.3` inside `10.1.0.0/16`?" is an AND with a mask, and the answer decides whether a packet stays on the local wire or goes to the **default gateway**. Every VPC console, firewall rule, and Kubernetes network policy is this same math.
3. **Routers pick the most specific route** — longest-prefix match. The "default route" your laptop has is literally `0.0.0.0/0`: the prefix that matches every address but with the fewest bits, used only when nothing more specific matches.

By the end you will have implemented `ParseIPv4Header` over a real hex dump, the header checksum, the whole CIDR toolbox, and a longest-prefix-match routing table — the actual decision a router makes for every packet. Chapter 02 gave you checksums and byte order from first principles; Chapter 17 will use these parsers to dissect a whole `.pcap` capture.

## 2. The lesson, step by step

### Step 1 — anatomy of the IPv4 header

`b` starts at the first byte of the IPv4 header (whatever parsed the Ethernet frame before you has already done its job). The header looks like this — 20 bytes, four bytes per row:

```
        0                   1                   2                   3
        0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
       +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
byte 0 |Version|  IHL  |  DSCP / ECN   |          Total Length         |  bytes 0-3
       +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
byte 4 |         Identification        |Flags|      Fragment Offset    |  bytes 4-7
       +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
byte 8 |  Time to Live |    Protocol   |         Header Checksum       |  bytes 8-11
       +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
byte 12|                       Source Address                          |  bytes 12-15
       +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
byte 16|                    Destination Address                        |  bytes 16-19
       +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

| Offset | Size | Field | Meaning |
|---|---|---|---|
| 0 | 4 bits | **Version** | 4 for IPv4. Anything else is not yours to parse. |
| 0 | 4 bits | **IHL** | Header length in 32-bit *words*. Minimum 5 (= 20 bytes); bigger means options. |
| 1 | 8 bits | DSCP / ECN | Quality-of-service bits. Ignored in this course. |
| 2–3 | 16 bits | **Total Length** | The whole packet: header + payload. The unit is bytes. |
| 4–5 | 16 bits | **Identification** | Fragmentation id. We copy it through but do not reassemble. |
| 6–7 | 16 bits | Flags + Fragment Offset | `0x4000` means "don't fragment" (DF). Fragmentation is out of scope. |
| 8 | 8 bits | **TTL** | Every router decrements it; at 0 the packet is dropped and you get an ICMP Time Exceeded. That is what `traceroute` abuses. |
| 9 | 8 bits | **Protocol** | Payload type: 1 ICMP, 6 TCP, 17 UDP (the constants in the package). |
| 10–11 | 16 bits | **Header Checksum** | One's complement sum of the header. Verified on parse — see Step 4. |
| 12–15 | 32 bits | **Source Address** | Who sent it. |
| 16–19 | 32 bits | **Destination Address** | Who should get it. |

The IHL field is the clever part: the header length is counted in 32-bit words, so `IHL = 5` means 20 bytes. An IPv4 header is *always* a multiple of 4 bytes, because each option is padded to a 4-byte boundary. Our `Marshal` always writes IHL 5 (no options), but `ParseIPv4Header` must accept headers up to 15 words (60 bytes) and return the real length.

### Step 2 — big-endian, everywhere, always

Every multi-byte field is **big-endian**: the most significant byte comes first. The 16-bit Total Length `00 3c` is `0x003c` = 60, not `0x3c00` = 15360. The source address `c0 a8 01 64` is `0xC0A80164` = 192.168.1.100, in that byte order.

That is why the header diagram's leftmost byte is the most significant: **on the wire, network order = big-endian**. In Go you get it for free if you refuse to hand-roll the shifts:

```go
total := binary.BigEndian.Uint16(b[2:4])   // 0x003c
src := binary.BigEndian.Uint32(b[12:16])   // 0xc0a80164
```

An address carried as `uint32` keeps this convention, so `IPv4ToUint32("192.168.1.100") == 0xC0A80164` and the first octet is always `v >> 24`. Hold one representation (uint32, big-endian) everywhere and conversions are trivial: `Uint32ToIPv4` is just four shifts and a `fmt.Sprintf`.

### Step 3 — three lengths that are easy to confuse

A parser must never trust a length field blindly; it must **bound-check it against reality**. There are three different lengths in play:

- `len(b)` — how many bytes the buffer actually holds. Your ground truth.
- `TotalLength` — how many bytes the *packet* claims to be (header + payload). Valid range: `IHL*4 <= TotalLength <= len(b)`. Smaller than the header is nonsense; larger than the buffer means you got a truncated read (or a liar).
- `headerLen = IHL*4` — where the header ends and the payload begins. Your function returns it so the caller can slice `b[headerLen:]`.

```
|<----------- TotalLength ----------->|
+----------------+-------------------+
| IPv4 header    | payload           |
| IHL*4 bytes    |                   |
+----------------+-------------------+
|<-- len(b): the bytes you actually got -->|      (len(b) >= TotalLength required)
```

`ParseIPv4Header` validates in this order: `len(b) >= 20`, version == 4, IHL >= 5, `len(b) >= IHL*4`, `TotalLength >= IHL*4`, `TotalLength <= len(b)`, and finally the checksum. Stop at the first broken invariant; every error wraps `ErrInvalidIPv4Header` so the caller can match it with `errors.Is`.

### Step 4 — the header checksum, and why corruption is caught at the header

The checksum is the **Internet checksum** you met in Chapter 02 (and prereq 01): treat the bytes as 16-bit big-endian words, add them with carry wraparound, take the one's complement. The catch: the checksum field itself sits inside the data being summed, so it must be **zeroed** while computing. `Marshal` builds the header with the field still zero, computes, then pokes the result in. `ParseIPv4Header` copies the header, zeroes bytes 10–11, recomputes, and compares.

Worked example, from the golden header in the tests (`45 00 00 3c 1c 46 00 00 40 06 66 8f c0 a8 01 64 5d b8 d8 22`), with the checksum field zeroed:

```
4500 + 003c                     = 453c
453c + 1c46                     = 6182
6182 + 0000                     = 6182
6182 + 4006                     = a188
a188 + 0000                     = a188
a188 + c0a8 = 16230  ── carry ──▶ 6230 + 1 = 6231
6231 + 0164                     = 6395
6395 + 5db8                     = c14d
c14d + d822 = 1996f  ── carry ──▶ 996f + 1 = 9970
complement: ffff - 9970         = 668f   ← written into bytes 10–11
```

Two properties matter:

- **The checksum covers the header only — never the payload.** A corrupted *header* (wrong address, wrong length) would otherwise send your packet to the wrong host or make you read garbage. Each layer protects itself: the transport checksum inside TCP/UDP/ICMP protects the payload, and the Ethernet CRC-32 (Chapter 02) protects the frame. One bad byte in the payload is not the IP header's job.
- **It is weak on purpose.** It catches accidental bit flips cheaply, in software, at every hop and every host. It does **not** defend against a malicious sender — the sender can just recompute it. Integrity against attackers comes from TLS (Chapter 12), not from a 2-byte sum.

The tests include `TestCh03_Parse_RejectsCorruptChecksum`: flip one bit anywhere in the header and the parse must fail. Corruption in transit is a fact of life; dropping bad packets silently is the correct, boring behavior.

### Step 5 — parse a real-ish packet by hand

Here is a 60-byte packet: a 20-byte IPv4 header followed by 40 bytes of TCP payload (not shown). Read it with the table from Step 1:

```
45 00 00 3c 1c 46 00 00 40 06 66 8f c0 a8 01 64 5d b8 d8 22
```

| Bytes | Hex | Field | Value |
|---|---|---|---|
| 0 | `45` | Version / IHL | `4` / `5` → header is 5×4 = **20 bytes** |
| 1 | `00` | DSCP/ECN | 0 — ignored |
| 2–3 | `00 3c` | Total Length | **60** → 40 bytes of payload follow the header |
| 4–5 | `1c 46` | Identification | 7238 |
| 6–7 | `00 00` | Flags / Fragment Offset | Not fragmented |
| 8 | `40` | TTL | **64** — a fresh Linux default |
| 9 | `06` | Protocol | **6 = TCP** |
| 10–11 | `66 8f` | Header Checksum | matches the hand computation above |
| 12–15 | `c0 a8 01 64` | Source | **192.168.1.100** |
| 16–19 | `5d b8 d8 22` | Destination | **93.184.216.34** |

Validation: version 4 ✓, IHL 5 ✓, buffer holds ≥ 20 bytes ✓, `60 >= 20` ✓, `60 <= len(b)` — only true if the caller really handed you 60 bytes ✓, checksum ✓. If the packet were truncated to 45 bytes in the buffer, the parse must fail even though every field above still "looks" fine. That is the bound check from Step 3 doing its job.

### Step 6 — CIDR: a mask is a bit filter

A subnet is written `192.168.1.0/24`: an address plus a prefix length. The prefix length cuts the 32-bit address into **network part** (leftmost `n` bits) and **host part** (the rest). The mask has 1s where the network part is:

```
prefix:    /24
mask:      CIDRToMask(24) = 255.255.255.0 = 11111111 11111111 11111111 00000000

192.168.1.77   11000000 10101000 00000001 01001101
255.255.255.0  11111111 11111111 11111111 00000000
AND            11000000 10101000 00000001 00000000  = 192.168.1.0  ← NetworkAddress
```

The AND **keeps the network bits and erases the host bits** — that is all a mask does. Two hosts are on the same subnet when their ANDs are equal (`InSameSubnet`); the wildcard address (`^mask`) fills the host bits with 1s to get the broadcast address. Hosts of a /24: 256 addresses, minus the network and broadcast addresses = `HostCount(24) = 254`. Two special cases the tests pin:

- `/31` → **2** hosts: a point-to-point link where the two endpoints are the whole subnet (RFC 3021).
- `/32` → **1** host: a single host route; the broadcast address is the address itself.

`MaskToCIDR` goes backwards and must reject non-masks: `0xF0F0F0F0` has 1s, then 0s, then 1s again — no prefix length produces it. Valid masks are exactly a run of 1s followed by a run of 0s, and `math/bits.OnesCount32` tells you where the run ends.

### Step 7 — how a router decides: longest-prefix match

A host asks one question: *is the destination on my subnet?* If `InSameSubnet(dst, myIP, myPrefix)` it sends directly on the local link; otherwise it hands the packet to its **default gateway**. That gateway (and every router after it) holds a routing table. Here is a tiny one:

```
destination   prefix   next hop        matches...
0.0.0.0       /0       192.168.1.1     everything (the default route)
10.1.0.0      /16      10.1.0.254      10.1.x.x
10.1.2.0      /24      10.1.2.254      10.1.2.x
```

A packet to `10.1.2.77` matches **all three** rows. Longest-prefix match says: the row with the most bits wins, because it describes the tightest-fitting network. So:

- `10.1.2.77` → `/24` → `10.1.2.254` (most specific).
- `10.1.9.9` → matches `/16` and `/0` → `/16` → `10.1.0.254`.
- `8.8.8.8` → matches only `/0` → the default gateway.

That is exactly what your laptop does when you use Wi-Fi: check `netstat -rn` (macOS) or `ip route` (Linux) and find the `default` line — it is `0.0.0.0/0`, the catch-all with the fewest possible bits. Real tables have thousands of these rows (BGP feeds the internet's core routers with ~1M prefixes); "pick the longest match" is simply a more specific `if`.

Our `RouteTable` resolves ties — two routes with the *same* prefix length that both match — deterministically: **the route added earliest wins.** Real routers spread equal-cost routes over several next hops (ECMP) or use policy; what matters for us is that the answer never depends on map iteration order.

### Step 8 — what you implement

In `internal/packet/packet.go`:

- `ParseIPv4Header(b)` — full validation (Step 3 order), checksum verification (Step 4), returns the header and `headerLen`.
- `IPv4Header.Marshal()` — 20 bytes, version 4, IHL 5, zeros elsewhere, checksum filled in.
- `IPv4HeaderChecksum(hdr)` — the RFC 1071 sum over a header whose checksum field is already zeroed.
- `IPv4ToUint32` / `Uint32ToIPv4` — dotted-quad ⇄ big-endian `uint32`. Exactly 4 parts, each 0–255, parsed with `strconv.Atoi`: leading zeros are decimal, so `"1.2.3.04"` is 1.2.3.4 (there is no octal here).
- `CIDRToMask`, `MaskToCIDR`, `ParseCIDR`, `NetworkAddress`, `BroadcastAddress`, `HostCount`, `InSameSubnet` — the CIDR toolbox.
- `RouteTable.Add` / `RouteTable.Lookup` — longest-prefix match with the earliest-added tie-break.

The tests pin the exact semantics above, including the chosen edge cases (`/31` → 2 hosts, `"04"` accepted, equal-prefix stability), so read them when a decision feels ambiguous.

## 3. Your challenge

Implement every function in `internal/packet/packet.go` (the doc comments restate the contracts). Run:

```sh
go test ./internal/packet -run Ch03 -race -v
```

All 24 tests must pass. The package must stay standard-library only — no `net`, no help from `net.ParseIP`. Every parse error must be an `ErrInvalidIPv4Header` wrapping so that `errors.Is` works; every validator (`CIDRToMask`, `MaskToCIDR`, `ParseCIDR`, `Add`) must reject bad input with a real error instead of guessing.

## 4. Hints

<details><summary>Hint 1 — the parse skeleton</summary>

```go
if len(b) < 20 { return IPv4Header{}, 0, fmt.Errorf("%w: ...", ErrInvalidIPv4Header, ...) }
version := b[0] >> 4
ihl := int(b[0] & 0x0f)
if version != 4 { ... }
if ihl < 5 { ... }
headerLen = ihl * 4
if len(b) < headerLen { ... }
total := int(binary.BigEndian.Uint16(b[2:4]))
if total < headerLen { ... }
if total > len(b) { ... }
if !checksumOK { ... }
```

Read `TTL` as `b[8]` and `Protocol` as `b[9]`; the addresses start at 12 and 16. Remember to return the header **and** `headerLen`.
</details>

<details><summary>Hint 2 — the checksum recipe</summary>

```go
var sum uint32
for len(b) >= 2 {
    sum += uint32(binary.BigEndian.Uint16(b[:2]))
    b = b[2:]
}
if len(b) == 1 { sum += uint32(b[0]) << 8 }   // odd trailing byte: pad on the right
for sum>>16 != 0 { sum = sum&0xffff + sum>>16 } // fold carries
return ^uint16(sum)                              // one's complement
```

For verification you need a copy of the header with bytes 10–11 zeroed; never zero the caller's bytes. For `Marshal`, build the header with the field still zero, compute, then `binary.BigEndian.PutUint16(b[10:12], c)`.
</details>

<details><summary>Hint 3 — mask math and longest-prefix lookup</summary>

```go
// CIDRToMask: bits==0 → 0, else ^uint32(0) << (32-bits)
// MaskToCIDR: ones := bits.OnesCount32(mask); valid iff mask == (^uint32(0) << (32-ones)) (for ones>0)
// Lookup: iterate ALL routes, keep the best match, replace only when Bits is STRICTLY greater
for i := range t.routes {
    m := maskForBits(t.routes[i].Bits)
    if ip&m != t.routes[i].Dest&m { continue }
    if best < 0 || t.routes[i].Bits > t.routes[best].Bits { best = i } // >, not >=: earliest wins ties
}
```

Mask both sides of the comparison: a `Dest` written with host bits set (e.g. `10.1.2.99/24`) still matches its subnet.
</details>

<details><summary>Hint 4 — traps the tests are watching for</summary>

- A pure `Marshal`→`Parse` round trip needs `TotalLength == 20`, because you only marshal the header. For a packet with a payload, set `TotalLength = 20 + len(payload)` and append the payload yourself.
- `Parse` must **fail** on the corrupt-checksum test — verification is not optional. Wrap errors with `%w` and `ErrInvalidIPv4Header`.
- `"1.2.3.04"` is valid (value 4). `"1.2.3."`, `" 1.2.3.4"`, `"1.2.3.4 "`, and `"1.2.3.4.5"` are not. Use `strconv.Atoi` and propagate its error.
- `HostCount` special-cases `/31` (2) and `/32` (1) before the `2^(32-bits) - 2` formula.
- `BroadcastAddress(ip, 32) == ip`; `NetworkAddress(ip, 0) == 0`.
- Route ties: first added wins — use `>` when comparing prefix lengths, and iterate the routes in insertion order.
</details>

## 5. Stretch challenges

1. **ICMP echo parse helper.** An ICMP Echo Request (type 8) / Reply (type 0) message starts right after the IPv4 header: `type(1) code(1) checksum(2) identifier(2) sequence(2)` then data. Write `ParseICMPEcho(b []byte) (id, seq uint16, err error)` that validates type/code and **verifies the ICMP checksum with the same 16-bit sum** (unlike the IP header, the ICMP checksum covers the whole message including the data). This is what `ping` builds — you are one step from implementing it.
2. **IPv6 version detection.** The version lives in the *top nibble of the first byte* for both protocols, on purpose: a router can dispatch on one nibble. Write `PacketVersion(b []byte) int` that returns 4 or 6 (and 0 for an empty buffer), and explain in a comment why the IPv6 header could not be parsed with this chapter's parser: the addresses grow to 16 bytes each and the header is a fixed 40 bytes with a totally different field layout (`Traffic Class`, `Flow Label`, `Payload Length`, `Hop Limit`, `Next Header`).
3. **A trie route table.** The linear `Lookup` is fine for ten routes; the internet runs on ~1M prefixes. Build a binary radix trie (Patricia trie): each route is a bit-path from the most significant bit of `Dest`; `Lookup` walks `min(32, ...)` steps and remembers the last node that had a route. Make it return the same answers as the slice version, including the earliest-added tie-break, and benchmark both with `go test -bench`.
4. **Header options.** Extend `Marshal` to emit one 4-byte option when the caller sets a flag: IHL 6, the option bytes, and `TotalLength` unchanged semantics. Then make `Parse` return the option bytes too, and confirm the checksum still verifies (it covers the options — that is why the field covers `IHL*4` bytes, not just 20).

## 6. Reflect

- The header checksum cannot detect corruption in the payload. Which layer does, and what would the internet look like if the IP header checksum covered the payload too?
- Why must a parser treat `TotalLength`, `IHL`, and `Info.Length` style fields as *hostile input*? Name a real vulnerability class that comes from trusting a length field (hint: it starts with "buffer").
- `MaskToCIDR(0xFFFFFF00)` is 24, but `MaskToCIDR(0xFFFFFFFE)` is an error. Why is `...FE` not "almost a /31"? What invariant do masks have that this check enforces?
- A /0 default route matches every address. Why does the *fewest bits* route end up being the fallback rather than the winner? State the rule in one sentence.
- Our tie-break is "earliest added wins". Real routers can use ECMP instead: same prefix, several next hops, chosen by hash. What does ECMP buy, and what does it break compared to a deterministic tie-break?
- Your phone's `/32` loopback route, your `/31` point-to-point VPN links, and your `/24` home network all use the same `Lookup` function. What other "exotic" prefix lengths show up in a real `ip route` output, and what are they for?
