# Chapter 04 — TCP sockets with Go: listeners, connections, deadlines

| | |
|---|---|
| **Build** | `EchoServer`, `Echo`, `ReadLineWithTimeout`, `CloseWrite` — `internal/tcp/tcp.go` |
| **Tests** | `internal/tcp/tcp_test.go` — 14 tests |
| **Run** | `go test ./internal/tcp -run Ch04 -race -v` |
| **Prerequisites** | [prereq 02 — Go essentials](../prereq/02-go-essentials.md), [prereq 03 — concurrency primitives](../prereq/03-concurrency-primitives.md), [prereq 04 — how networks work](../prereq/04-how-networks-work.md), [prereq 05 — networking vocabulary](../prereq/05-networking-vocabulary.md) |
| **Time** | ~90 min |

## 1. Why this matters

Everything above TCP — HTTP, gRPC, TLS, your database driver — is byte streams and deadlines wearing fancy clothes. Strip the HTTP away and a web server is: accept a connection, read bytes, decide, write bytes, close. Get that lifecycle wrong (a goroutine that outlives its connection, a `Read` that blocks forever because the peer vanished silently) and your service leaks memory one quiet client at a time.

The **echo server** is the "hello world" of network servers: the smallest program that exercises the *entire* lifecycle of a connection — bind, listen, accept, read, write, close — plus the one skill every production server needs and most tutorials skip: a shutdown that actually finishes.

You also need the vocabulary to read your own failures: **connection refused** (the kernel sent RST — nothing is listening; the error is instant) is a different failure from **connection timed out** (your packets simply vanished — you wait until your deadline fires). Different failures, different fixes (see prereq 04). This chapter makes you the person who can tell them apart from the error alone.

## 2. The lesson, step by step

### Step 1 — the socket lifecycle, and what Go wraps

A TCP server is six system calls in a trench coat. In Go you write exactly one of them out loud; the rest hide inside `net.Listen`:

| syscall | Go | What it does |
|---|---|---|
| `socket()` | inside `net.Listen` | allocate an endpoint (a file descriptor) |
| `bind()` | inside `net.Listen` | give the endpoint an address — `127.0.0.1:0` means "**I** pick the port" |
| `listen()` | inside `net.Listen` | mark the socket passive, set the accept queue length |
| `accept()` | `Listener.Accept()` | block until a client connects; returns one `Conn` per client |
| `read` / `write` | `Conn.Read` / `Conn.Write` | move bytes between user space and the kernel buffers |
| `close()` | `Conn.Close` / `Listener.Close` | release the fd; sends FIN |

One listener, many connections. Each `Accept` returns a **new descriptor** — the listener itself never carries payload:

```
net.Listen("tcp", "127.0.0.1:0")
        │
   ┌────▼─────┐  Accept()   ┌─────────────┐   ┌──────────────────┐
   │ listener │ ──────────▶ │ conn (fd 5) │──▶│ handler goroutine │
   │  (fd 3)  │             ├─────────────┤   ├──────────────────┤
   └──────────┘ ──────────▶ │ conn (fd 6) │──▶│ handler goroutine │
                             └─────────────┘   └──────────────────┘
        ▲ loop forever; each conn is independent
```

`127.0.0.1:0` in every test: the kernel assigns a free ephemeral port and no test ever collides with another. You read it back with `srv.Addr()`. Never hard-code `:8080`.

### Step 2 — file descriptors and the kernel buffer story

Every connection is a **file descriptor** — an index into the kernel's open-file table. For each socket the kernel owns two buffers (the `rmem`/`wmem` knobs you met in prereq 04):

```
 your process                 kernel (each host)                 network
 ─────────────           ─────────────────────────          ────────────────
 Conn.Write(buf) ─▶ ┌─────────────┐   segments    ┌─────────────┐ ─▶ Conn.Read
                    │ send buffer │ ─────────────▶│ recv buffer │
                    └─────────────┘               └─────────────┘
                    kernel clocks bytes out       peer ACKs / window update
```

Three consequences you will feel in real programs:

1. **`Write` returning nil does not mean the peer got the bytes.** It means the bytes are in *your* kernel buffer; the kernel delivers them at the peer's advertised pace (flow control, `rwnd`).
2. **Back-pressure is built in.** If the receiver is slow, its receive buffer fills, it advertises window 0, and your send buffer fills, and *then* your `Conn.Write` blocks (or your deadline expires). Unbounded buffering is not a thing TCP gives you for free — courtesy of the receiver window.
3. **Reads can be partial.** A `Read(buf)` may fill 1 byte or 32 KiB; never assume it returned a whole message — that is why `io.ReadFull` and explicit framing exist.

**Nagle's algorithm** — the kernel coalesces small writes into one segment to avoid flooding the network with tiny packets (a 40-byte ACK carrying 1 byte of payload). Great for bulk throughput; adds up to one RTT of delay for chatty request/response protocols. `Conn.SetNoDelay(true)` sets `TCP_NODELAY` and switches Nagle off — latency-sensitive protocols (games, terminals) want it; bulk transfers don't care.

### Step 3 — deadlines, not contexts

`net.Conn` **cannot take a `context.Context`**. The conn-level tool is the *deadline*, and it is an absolute time, not a duration:

```go
conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)) // any read after this moment fails
```

When a deadline fires, the I/O op returns an error for which `errors.Is(err, os.ErrDeadlineExceeded)` is true. Internally Go arms `SO_RCVTIMEO`-style timeouts via epoll/kqueue (prereq 04), so an expired deadline costs one syscall, not a timer goroutine per connection.

Rule from the prereq 05 checklist: **timeouts on every blocking call — connect, read, write.** `net.DialTimeout` is the connect timeout; `SetReadDeadline`/`SetWriteDeadline` cover the rest.

The mental model — use both, for different jobs:

| | `context.Context` | `SetReadDeadline` |
|---|---|---|
| scope | your goroutines, your program | one file descriptor |
| says | "the caller wants you to stop" | "the peer is too slow" |
| checked | by your `select` | by the kernel, at the next I/O |
| error | `context.Canceled` / `context.DeadlineExceeded` | wraps `os.ErrDeadlineExceeded` |

A silent peer and a slow peer are **identical** until a deadline fires. That's why every blocking call in this chapter sets one.

### Step 4 — the accept loop and its shutdown discipline

Every production Go server is this loop with hygiene:

```go
for {
	conn, err := listener.Accept()   // blocks until a client arrives — or the listener closes
	if err != nil {
		return                        // listener closed (or accept is broken): stop accepting
	}
	go handle(conn)                  // one goroutine per connection; track them!
}
```

Graceful shutdown is four steps, **in this order**:

1. **Stop accepting** — close the listener; the blocked `Accept` returns an error; the loop exits.
2. **Close active connections** — you can't wait for handlers that are blocked in `Read` on a peer that never speaks again. Track every conn in a map under a mutex; `Close` each one — their `Read`s unblock with `use of closed network connection`.
3. **Wait for handlers** — a `sync.WaitGroup`; each handler calls `Done` when it returns. Only now is the server done.
4. **Make it idempotent** — `sync.Once`; tests and shutdown paths call `Close` twice more often than you'd like. Close is safe to call any number of times and returns the same result.

What you must *not* do: just `os.Exit` and hope, or close the listener and leak the handlers (each holds an fd and a goroutine — the leak compounds silently).

**Refused vs timed out**, one more time because you will debug this at 2 a.m.: `connection refused` = RST received = the kernel actively rejected you (port closed, nothing listening) — instant. `i/o timeout` = no answer at all = firewall dropped your SYN or the host is down — you learn about it when your deadline fires. The first means "fix the address/port"; the second means "fix the network/firewall".

### Step 5 — half-close: send FIN, keep reading

TCP is **two independent byte streams** glued into one connection. `Close()` shuts both. `CloseWrite()` shuts only yours — it sends FIN, the peer's `Read` returns `io.EOF`, and the peer **may still write**, and you may still read every byte it writes.

```
 client                          server
   │───── CloseWrite() ─── FIN ────▶│
   │                                │ server's Read returns 0, io.EOF
   │◀────── "log complete" ─────────│   (server can keep talking!)
   │  client's Read ◀───────────────│
```

When does this matter? Any protocol where the client says "my request is finished" without knowing the response length — e.g. EOF-delimited request bodies, streaming uploads, tunnels. `CloseWrite` requires `*net.TCPConn` specifically: it's a TCP feature (a TLS session on top doesn't expose it, which is why truncated-upload-over-TLS needs an app-level "end of body" marker).

Half-close is also a debugging tool: `echo` a huge request into a server that reads until EOF and watch it complete without you closing your read side.

### Step 6 — why loopback tests are enough (and the race detector is not optional)

`127.0.0.1` never leaves the machine: the same kernel TCP stack, the same fds, the same buffers, the same deadline path — only the wire is virtual. Packet loss, MTU, NAT, congestion are exactly the things loopback *cannot* show you, and this chapter does not need them. What loopback tests **can** show, deterministically: wrong goroutine discipline, fd leaks, missed deadlines, EOF/half-close confusion — i.e. all the bugs *you* write. That's why the whole suite runs in about a second, why `-race` can watch every interleaving, and why the tests never hard-code a port.

And the echo server itself? It is the hello world of network servers — the smallest program that forces you to get the accept loop, the buffer sizing, the error propagation and the shutdown order right. Every production Go server you will ever read is this loop wearing more hygiene.

## 3. Your challenge

Implement in `internal/tcp/tcp.go`:

- **`NewEchoServer`** — `net.Listen("tcp", "127.0.0.1:0")` + `Addr()` returning the actual bound address; a goroutine running the accept loop; every accepted conn tracked in a map + `WaitGroup`.
- **`(*EchoServer).Close`** — stops accepting (listener closed), closes the listener and all tracked conns (blocked `Read`s unblock), waits for handlers, idempotent via `sync.Once`.
- **`Echo`** — one-shot round trip: `net.DialTimeout` (2s) → `SetWriteDeadline` (2s) → write the message → `SetReadDeadline` (2s) → `io.ReadFull` exactly `len(message)` bytes → return as string. Works for binary (NULs, `0x00..0xff`), works for `""`.
- **`ReadLineWithTimeout`** — `SetReadDeadline(now + d)`; read byte-at-a-time until `\n` (never swallow bytes belonging to the next line); strip one trailing `\r` (CRLF tolerance); line longer than `1<<16` bytes → error; on deadline the error matches `errors.Is(err, os.ErrDeadlineExceeded)`; on peer close without newline, `errors.Is(err, io.EOF)`.
- **`CloseWrite`** — type-assert to `*net.TCPConn` (anything else → error), call `CloseWrite()`.

## 4. Hints

<details><summary>Hint 1 — the shape of Close</summary>

`sync.Once` wraps the whole body; the result is stored on the struct so every later call returns it. Inside: set `closing = true` and close the listener under the mutex; then close every conn in the map (ignore their errors); unlock; then `wg.Wait()`. The accept loop and the handlers share the mutex for the map, so a conn can never be "accepted but never tracked" — if the loop sees `closing`, it closes the conn itself and returns.
</details>

<details><summary>Hint 2 — handler loop</summary>

```go
buf := make([]byte, 32*1024)
for {
	n, readErr := conn.Read(buf)
	if n > 0 {
		if _, werr := conn.Write(buf[:n]); werr != nil { return }
	}
	if readErr != nil { return }   // io.EOF (peer FIN) or "use of closed network connection" (Close)
}
```

`defer` both the map removal and `conn.Close()`. Don't `log` anything — there is no logger, and silence is fine.
</details>

<details><summary>Hint 3 — Echo and io.ReadFull</summary>

`Read` is allowed to return fewer bytes than asked; `io.ReadFull` loops until it has all N or an error. Set the deadlines *before* the I/O, not after. `Write` only returns a short count with a non-nil error, so `if _, err := conn.Write(...)` is sufficient. The empty message needs no special case: `io.ReadFull` with a zero-byte slice returns immediately.
</details>

<details><summary>Hint 4 — ReadLineWithTimeout, byte at a time</summary>

Read one byte into a scratch `[1]byte`; on `\n` return the accumulated slice with a trailing `\r` trimmed. Count bytes and bail out at `1<<16+1`. Wrap every error with `%w` (`fmt.Errorf("read line: %w", err)`) — otherwise `errors.Is(err, os.ErrDeadlineExceeded)` and `errors.Is(err, io.EOF)` will never match. Byte-at-a-time is deliberate: a `bufio.Reader` may over-read into the *next* line and your caller can never get those bytes back.
</details>

<details><summary>Hint 5 — CloseWrite</summary>

`conn.(*net.TCPConn)` — and if the assertion fails, return `fmt.Errorf("CloseWrite needs a *net.TCPConn, got %T", conn)`, do NOT panic. The test hands you a `net.Pipe()` and expects an error, not a crash.
</details>

## 5. Stretch challenges

1. **Broadcast chat hub.** One listener, N clients; every line from any client is fanned out to *all* connected clients. The trap: a slow client's send blocks the broadcast loop for everyone. Fix it with a bounded per-client outbound channel drained by that client's writer goroutine — and decide what the hub does when a client's buffer is full (drop the line? disconnect the client? block? — each is defensible; write down your choice).
2. **Per-connection idle timeout.** Re-arm `SetReadDeadline(time.Now().Add(idle))` at the top of every read iteration; on deadline, close just that conn (the handler exits; everyone else is unaffected). Now combine with a *global* server deadline in `Close`: which one wins for a conn, and can you distinguish the two in the error?
3. **Limit max conns with a semaphore.** A `chan struct{}` of capacity `maxConns`; acquire *before* spawning the handler goroutine (or inside it — which is better for the accept loop's latency?), release after. When the semaphore is full: close the fresh conn immediately and tell the client "at capacity" — or block the accept loop? Discuss the back-pressure trade-off against prereq 05's vocabulary.

## 6. Reflect

- Why `127.0.0.1:0` instead of `:8080` — what breaks in a suite with fixed ports on a shared machine or in CI with parallel packages?
- After `Close` returns, what is guaranteed about the handler goroutines — and why is that guarantee worth the `WaitGroup`?
- A handler is blocked in `Read`; `Close` runs. What error does the handler see, and why is that error *not* `io.EOF`? (EOF means "the peer sent FIN"; closing your own fd is not a FIN.)
- Your service has a `context.Context` for shutdown. Why can you not just cancel it and skip deadlines on the conns?
- `errors.Is(err, os.ErrDeadlineExceeded)` — why is `errors.Is` the right tool and `err == os.ErrDeadlineExceeded` wrong?
- `CloseWrite` sends FIN but you can still read. Where would you use this in a real system — and why does TLS (Chapter 12) not give you this exact primitive?
- `connection refused` vs `i/o timeout`: which one tells you the server process crashed, and which one tells you a firewall is dropping packets? Which one would a health check prefer, and why?
- Why is the loopback interface "enough" for this chapter — and which class of bug would it *never* catch?
