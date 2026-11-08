# Prereq 03 — Concurrency primitives you'll actually use

Network programming in Go is concurrent programming wearing a different hat: every connection handoff, every accept loop, every "do N things at once" is goroutines + channels + a context. This chapter is the complete toolbox; nothing here is exotic.

## Goroutines

`go f()` starts a function on a new goroutine — cheap (few KB stack, growable), multiplexed onto OS threads by the Go runtime. The runtime's scheduler is preemptive-ish (since 1.14, asynchronous preemption), but you should never rely on a tight loop yielding; use channel ops or explicit `runtime.Gosched()` if you truly need to yield.

A goroutine that blocks forever on I/O pins whatever it holds (memory, locks). That's a **goroutine leak**. In this course, `testutil.NoLeaks` fails any test that leaks one — it's the #1 bug class in Go network servers.

## Channels

```go
ch := make(chan T)          // unbuffered: send blocks until a receiver is ready (back-pressure for free)
ch := make(chan T, 128)     // buffered: up to 128 in flight, then blocked
close(ch)                   // sender-only, exactly once; receivers observe ok==false
```

- `v, ok := <-ch` — ok=false means closed.
- `for v := range ch` — drains until closed.
- Only the **sender** closes; closing twice panics; send-on-closed panics.
- Multiple producers → one closer goroutine with `sync.WaitGroup`.

## select

`select` chooses among channel ops ready *right now*; `default:` makes it non-blocking. With several ready cases it picks pseudo-randomly (no priority!). The canonical cancellation shape:

```go
select {
case msg := <-in:
    handle(msg)
case <-ctx.Done():
    return ctx.Err()
}
```

## context.Context

Three constructors: `context.Background()`, `WithCancel`, `WithTimeout`/`WithDeadline`. Rules:

- Pass ctx as the **first parameter**, named `ctx`.
- Never store it in a struct; never pass nil.
- `ctx.Err()` returns `context.Canceled` or `context.DeadlineExceeded`.
- Cancellation propagates down the call tree; anything blocking must watch `Done()`.

## sync

- `sync.Mutex` — guard shared state (stats counters, maps). Keep critical sections tiny; never hold across I/O or channel sends.
- `sync.WaitGroup` — wait for N goroutines to finish; `Add` before `go`, `Done` via `defer`.
- `sync.Once` — lazy singleton init (used sparingly).
- `sync/atomic` — `atomic.Int64` etc. for counters that don't need a mutex (LB in-flight counts, pool stats). Cheaper, subtler; race detector still validates them.

## Data races and the race detector

A data race = two goroutines access the same memory, at least one is a write, no synchronization. It does **not** reliably crash; it corrupts silently. Always run `go test -race`. If it reports a race, the two stack traces show the two racing accesses — read both frames.

## Deadlocks vs livelocks

- **Deadlock**: everything waits on something that never happens (channel never closed, mutex never released, conn never written). Symptom: hang. The watchdog dumps stacks.
- **Livelock**: goroutines spin without progress (retry storms, spinning on a channel). Symptom: CPU burn, no work done. Backoff+jitter (Chapter 07) is the cure.

## The accept-loop pattern you will write in Chapter 04

```go
for {
    conn, err := listener.Accept()
    if err != nil {
        if ctx.Err() != nil { return nil } // shutting down
        continue // transient error: log and keep going
    }
    go handle(ctx, conn)  // bounded by a semaphore if needed
}
```

Every production Go server is this loop with extra hygiene: limits, deadlines, graceful drain.

## Channels are not queues

If you want a bounded work queue with drops, use a buffered channel + select/default (or drop-oldest logic). If you want FIFO fairness, channels don't promise it between multiple blocked senders. For anything fancier, write it explicitly rather than abusing channels.
