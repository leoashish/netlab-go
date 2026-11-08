# Prereq 02 — Go essentials for network programming

The tour of the Go stdlib that this course leans on hardest. If any of this is unfamiliar, stop and read it before the chapters; the chapters assume you know the 20% that does 80% of the work.

## Modules

This repo is `module netlab` (see `go.mod`). Imports look like `netlab/internal/wire`. `internal/` packages may only be imported from inside the module — good hygiene for course code. Standard library only; `go.sum` stays empty.

## Errors

- Sentinel errors: `var ErrFrameTooLarge = errors.New("...")`; callers check with `errors.Is(err, frame.ErrFrameTooLarge)`.
- Wrapping: `fmt.Errorf("parse header: %w", err)` keeps the chain.
- Never return a nil error wrapped in a non-nil value; never compare errors with `==` except against your own sentinels (use `errors.Is`).

## Interfaces you will meet constantly

- `io.Reader` — `Read(p []byte) (n int, err error)`: fill `p`, return how many bytes were read. `err == io.EOF` means "stream finished cleanly".
- `io.Writer` — `Write(p []byte) (n int, err error)`: write `p`; short writes return `io.ErrShortWrite` (or an error).
- `io.ReadWriter` — both. `net.Conn` implements it, plus deadlines.
- Rule: a `Read` may return fewer bytes than you asked for; use `io.ReadFull` when you need exactly N.

## Slices and views into bytes

`[]byte` is a header (pointer, len, cap) — passing it around copies nothing. But beware sub-slices sharing backing storage: keep ownership notes clear. In packet-parsing code we often return sub-slices of the input buffer; the doc for each function says whether it copies.

`append(dst, ...)` may reallocate; `binary.BigEndian.PutUint32(buf, v)` needs a 4-byte slice with cap — chapters often use `dst := make([]byte, 0, 4)` or fixed `[4]byte`.

## time and timeouts

Network code lives and dies by deadlines. The two patterns used all over this course:

```go
conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
select {
case <-ctx.Done():
    return ctx.Err()
case res := <-ch:
    ...
}
```

`context.Context` is how Go says "cancel this". Anything that blocks on I/O should either have a deadline on the conn, or watch `ctx.Done()` — ideally both.

## Testing discipline used in this course

- Every package has a `main_test.go` running `testutil.Main`, a **watchdog** that dumps all goroutine stacks after 90s if the tests hang (a deadlock is the default failure mode of network code).
- Tests are **red first**: they fail on the stubs; the failure messages point at the chapter to implement.
- `-race` is the expected way to run them: `go test ./internal/tcp -race -v`.
- Loopback only (`127.0.0.1:0` — the OS picks a free port), never the real internet.
- `testutil.FakeClock` exists for rate-limiter/circuit-breaker chapters; time-dependent logic takes an injected clock so tests are deterministic.

## Generics, quickly

Only two uses in this course: typed helpers like `func Drain[T any](...)`, and `slices`/`maps` package functions (`slices.Equal`, `slices.Sort`, `slices.Contains` — Go 1.21+). That's it.

## What you do NOT need to know yet

- Generics metaprogramming, reflection (we avoid it; x509 and JSON use it internally but you'll never write it)
- `unsafe` (never used; wire-format code in this course is bounds-checked by construction)
- `sync/atomic` — used only in the load balancer and pool chapters; introduced there with a two-line refresher
