# Prereq 05 — The networking vocabulary that matters

Words that sound interchangeable but aren't. If you internalize this table, every architecture review and every incident postmortem will make twice as much sense.

## Latency, bandwidth, throughput — pick two different words for two different things

| Term | Meaning | Analogy |
|---|---|---|
| **Bandwidth** | Capacity of the link, bits/second (e.g. 1 Gbps) | width of the pipe |
| **Throughput** | Bits/second actually delivered, end to end | water actually flowing |
| **Latency** | Time for one message to make the trip (ms) | length of the pipe |
| **RTT** | Round-trip time: send + back (latency × 2, roughly) | jog out and back |
| **Jitter** | Variation in latency between packets | wobbly pipe length |
| **Packet loss** | Fraction of packets dropped, % | leaks in the pipe |

Consequences you will feel in code:

- Latency (not bandwidth) dominates short transfers: a 1 KB HTTP response costs ~1 RTT no matter how fat the link. That's why connection reuse (keep-alive, pools — Chapter 13) exists.
- Loss kills TCP throughput hard: at 1% loss, classic Reno-style congestion control drops to ~15% of link capacity. Modern algorithms (BBR) cope better, but "just add bandwidth" doesn't fix a lossy link.
- Bandwidth-delay product (BDP): bandwidth × RTT = bytes "in flight" at full rate. On a 1 Gbps link with 100 ms RTT the BDP is 12.5 MB — if neither side buffers that much, TCP can never fill the pipe. This is why buffers matter.

## Head-of-line blocking

TCP delivers bytes **in order**. If packet 3 is lost but 4 and 5 arrived, the kernel holds 4 and 5 hostage until 3 is retransmitted. Everyone waits. HTTP/2's multiplexing over one TCP connection suffers from this — HTTP/3 (QUIC over UDP) fixes it by moving ordering to the application. You can't fix HOL blocking by writing better application code over TCP; you fix it by changing transports or sharding connections.

## Back-pressure

When a producer outruns a consumer, something must absorb, drop, or delay. Unbounded buffering → OOM. TCP's receive window is back-pressure. Channel-based pipelines (dataflow-go) are back-pressure. A slow origin behind a fast proxy → the proxy must block, shed load, or cache (our capstone does all three).

## Idempotency and retry safety

Retrying a `GET` is safe; retrying a `POST /charge` double-charges. Any RPC framework worth its salt distinguishes **idempotent** operations (retry freely) from non-idempotent ones (require idempotency keys or explicit user confirmation). Chapter 07's retry policy is honest about this: exponential backoff + jitter, cap on attempts, and permanent vs transient classification.

## Keep-alive, pipelining, multiplexing

- **Keep-alive**: reuse one TCP (and TLS) handshake for many HTTP requests. Default in HTTP/1.1.
- **Pipelining**: send request 2 before response 1 arrives. Legal in HTTP/1.1, brittle in practice (intermediaries broke it), dropped from HTTP/2 in favor of real multiplexing.
- **Multiplexing** (HTTP/2): many streams over one connection, frames interleaved. See head-of-line blocking above.

## Load balancers, reverse proxies, CDNs

- **Reverse proxy**: terminates client connections, forwards to backends, can cache, compress, auth, rate-limit. (nginx, Envoy, our capstone.)
- **Load balancer**: distributes across backends; L4 works on IP/ports (dumb, fast), L7 understands HTTP (smart, can route by URL/host/headers).
- **Health checks**: active probing (LB pokes `/healthz`) or passive (count 5xx and eject). Chapter 15 implements round-robin + least-conns + health marking.
- **CDN**: a reverse proxy you don't operate, placed near users, caching aggressively (Cache-Control, ETag).

## Proxies and tunnels

A **forward proxy** sits in front of *clients* (corporate egress); a **reverse proxy** sits in front of *servers* (everything above). A **TCP tunnel** (Chapter 14) forwards raw bytes: `localhost:8080 → remote:80` — the basis of SSH port forwarding, ngrok, bastion hosts. It knows nothing about HTTP; it just splices two streams together and counts bytes.

## TLS in one paragraph

Public-key crypto lets two strangers agree on a symmetric key without sharing a secret in advance (Diffie-Hellman), and lets the server prove its identity via a **certificate** signed by a CA chain the client trusts. After the handshake, AES-GCM or ChaCha20 does the bulk encryption at line rate. Certificate = (subject name, public key, issuer, expiry, signature). Verification failure = handshake aborts = browser shows the red screen. Chapter 12 makes you be the CA.

## The checklist for any networked system

1. **Timeouts** on every blocking call (connect, read, write) — no exceptions.
2. **Retries** only with backoff+jitter, only for transient/idempotent ops.
3. **Limits**: max body size, max header size, max concurrent conns — defend the memory before the CPU.
4. **Observability**: you cannot fix what you cannot count (RTT histograms, Chapter 16).
5. **Graceful shutdown**: stop accepting, drain in-flight, exit. `context.Context` glued to the accept loop (Chapter 04).
6. **Idempotent writes** wherever possible, or dedupe keys at the edge.

RTT is the unit of network time. Bandwidth is the unit of network space. Everyone who forgets the difference re-derives it the hard way, once per outage.
