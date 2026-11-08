# Prereq 04 — How networks actually work (the 10-minute map)

You are not going to install a VPN or solder an RJ45. You are going to build software that talks over networks, and you will understand **every layer** it crosses — from "what a packet is" to "what Go's net package hands you". This is the map.

## The TCP/IP model (OSI's practical cousin)

| Layer | PDU name | Lives where | Examples |
|---|---|---|---|
| Link | frame | your NIC + switch | Ethernet, Wi-Fi, MAC addresses, VLANs |
| Internet | packet | routers decide, hosts decapsulate | IPv4, IPv6, ICMP, routing (OSPF, BGP) |
| Transport | segment (TCP) / datagram (UDP) | host kernel (mostly) | TCP, UDP, ports, retransmission, ordering |
| Application | message | **your code** | HTTP, DNS, TLS, WebSocket, SSH, gRPC |

OSI's 7 layers split "Application" into Application/Presentation/Session — chemically similar, practically identical for our purposes. Everything in this course happens in the top two layers **plus** the packet/parse chapters that open up the lower two.

Encapsulation: each layer wraps the payload of the one above in its own header. On the wire you see:

```
[ Ethernet header | IPv4 header | TCP header | HTTP bytes ][ FCS ]
```

Chapter 03 lets you parse the middle two headers by hand; Chapter 17 lets you dissect a whole capture.

## IP addresses and ports

- An **IPv4 address** is 32 bits (`192.168.1.10`); an **IPv6 address** is 128 bits (`2001:db8::1`). Written humans-first as dotted-quad, on the wire as big-endian bytes (Chapter 03).
- A **subnet** is (network address, prefix length) — `192.168.1.0/24`. Hosts AND their IP with the mask; if result == network address, it's local; otherwise the packet goes to the **default gateway**. Chapter 03 implements the mask math and a longest-prefix-match routing table, which is literally what routers do.
- A **port** is a 16-bit number identifying a socket on a host. 1–1023 are privileged (well-known: 80 HTTP, 443 HTTPS, 22 SSH, 53 DNS); 49152+ are ephemeral (what your laptop uses as source ports).
- An IP address + port + protocol (TCP/UDP) is a **socket** — the endpoint of a connection.

### NAT and why your laptop has a private IP

IPv4 ran out of addresses. Home routers do **NAT** (network address translation): many internal addresses (e.g. 192.168.x.x) share one public IP; the router rewrites source addresses and ports on outbound packets and remembers the mapping. Consequences: unsolicited inbound connections don't work (no mapping), port forwarding is manual, and peer-to-peer is hard (hence STUN/TURN — out of scope, but now you know why they exist).

## The kernel is in the middle

Your Go program never touches the wire. It calls `socket()`, `bind()`, `listen()`, `accept()`, `connect()`, `read()`, `write()` — **system calls** into the kernel. The kernel:

- owns the NIC queues and interrupt handling,
- implements the whole TCP state machine (handshake, retransmit, flow control, congestion control, teardown),
- buffers data (send buffer / receive buffer per socket — `sysctl net.core.rmem_max` and friends),
- delivers packets to the right socket by (src IP, src port, dst IP, dst port) demux.

`net.Listen("tcp", ":8080")` is a thin Go wrapper over `socket`+`bind`+`listen`+`accept4`. `conn.Read` is a `read(2)`; `conn.SetReadDeadline` is `SO_RCVTIMEO` interpreted by epoll/kqueue. Packets arriving faster than you read sit in the kernel buffer; if it fills, the kernel drops them and the peer (TCP) eventually shrinks its sending rate.

## TCP — the reliable byte stream

TCP gives you: **ordered, reliable, flow-controlled, congestion-controlled** delivery of a byte stream. Under the hood it is:

1. **Sequence numbers** — every byte is numbered; receiver detects gaps.
2. **ACKs** — receiver tells sender "I have everything up to byte N".
3. **Retransmission** — lost segments are re-sent (duplicate ACKs trigger fast retransmit; timeouts trigger RTO).
4. **Checksums** — every segment has a checksum (Chapter 02 geometry); corrupt segments are discarded silently.
5. **Flow control (rwnd)** — receiver advertises how much it can accept; sender must not overwhelm it.
6. **Congestion control (cwnd)** — sender slows down when the *network* drops/delays packets (slow start, AIMD, modern BBR).
7. **Connection lifecycle** — SYN, SYN-ACK, ACK (handshake) ... FIN, FIN-ACK (graceful close), RST (hard abort / rejected connection).

Chapter 05 re-implements a toy version of items 1–4 over an in-memory lossy link so the magic dissolves.

### UDP — the honest one

UDP does **nothing**: no handshake, no ordering, no retransmit, no flow control — just (src port, dst port, length, checksum, payload). That's why it's perfect for DNS queries (one round trip, fail fast), real-time media, and QUIC, which builds its own reliability on top (HTTP/3). If you need TCP's guarantees, you build them (Chapter 05) or you use TCP.

## DNS — names before packets

Every connection starts as a name lookup: `example.com` → IP. Usually: browser cache → OS cache → `/etc/hosts` → the configured **recursive resolver** (your ISP's/8.8.8.8/1.1.1.1). If the resolver doesn't know, it starts at the **root** servers (`.`), which refer it to the `.com` TLD servers, which refer it to example.com's authoritative nameservers. Every referral is a DNS message with flags/questions/answers (Chapter 10 parses those bytes by hand — it's one of the cleanest binary protocols to implement).

TTL (seconds) is on every answer; caches honor it. DNS is usually UDP port 53; responses >512 bytes historically fall back to TCP. "It's always DNS."

## TLS — encryption on top, not instead of TCP

TLS wraps a TCP connection with a handshake that negotiates ciphers and exchanges/verifies certificates, then encrypts everything after that. In your program: `tls.Dial`, `tls.Listen`, `tls.Config{Certificates, ClientCAs, ClientAuth}`. Chapter 12 generates its own CA and certificates so the whole trust chain is not a black box: you will sign, verify, reject expired/mismatched certs, and do mutual TLS (client also presents a cert).

## A full round trip, end to end

`curl https://example.com/`:

1. `curl` resolves `example.com` via DNS (UDP/53) → `93.184.216.34` (or AAAA/HTTPS records).
2. TCP handshake to `93.184.216.34:443` (SYN → SYN-ACK → ACK) — one RTT.
3. TLS handshake (ClientHello → ServerHello+Certificate → key exchange → Finished) — one more RTT (or zero with session resumption).
4. `GET / HTTP/1.1\r\nHost: example.com\r\n...` encrypted inside TLS records inside TCP segments inside IP packets inside Ethernet frames.
5. Server parses the request, writes a response (`HTTP/1.1 200 OK\r\n...`), same layers in reverse.
6. Client verifies cert chain against the system's root store, parses the response, done.

Every arrow in that list is a chapter in this course (DNS=10, TCP=4–5, TLS=12, HTTP=8–9).

## What you should be able to answer after reading

- Why is network byte order big-endian? (Chapter 01/02)
- What does a subnet mask actually do? (Chapter 03)
- Where do retransmissions happen — your code or the kernel? (kernel; Chapter 05 builds your own)
- Why does TCP exist if UDP is simpler? (reliability under loss; Chapter 05/06)
- Why "connection refused" vs "connection timed out"? (RST received vs no answer at all — different failures, different fixes)
- What is an RTT and why do all latency budgets count in RTTs? (see Prereq 05)
