# Chapter 07 — Client-side resilience: retries, backoff with jitter, and a circuit breaker

| | |
|---|---|
| **Build** | `Permanent`, `IsPermanent`, `NewRetryPolicy`, `(RetryPolicy).Do`, `NewCircuitBreaker`, `(CircuitBreaker).Do/State/Reset`, `(CircuitState).String` — `internal/resilience/resilience.go` |
| **Given** | `testutil.RecordingSleeper`, `testutil.NewFakeClock` (`internal/testutil/`, do not edit) |
| **Tests** | `internal/resilience/resilience_test.go` — 17 tests |
| **Run** | `go test ./internal/resilience -run Ch07 -race -v` |
| **Prerequisites** | [prereq 02 — Go essentials](../prereq/02-go-essentials.md) (interfaces, `errors.As`), [prereq 03 — concurrency primitives](../prereq/03-concurrency-primitives.md) (`sync.WaitGroup`, atomics, `context`), [prereq 05 — networking vocabulary](../prereq/05-networking-vocabulary.md), [Chapter 04 — TCP sockets](ch04-tcp-sockets.md) (deadlines, timeouts) |
| **Time** | ~75 min |

## 1. Why this matters

At any given moment some fraction of a fleet is broken: a deployment is rolling, a GC pause lasts 400 ms, a rack died, a packet vanished. **Failure is the common case, not the exceptional case.** A client that treats every error as fatal is fragile. A client that retries blindly is a weapon aimed at its own infrastructure.

This chapter builds the three standard mechanisms, in the order they were invented:

| Mechanism | What it protects | What it costs |
|---|---|---|
| **Retry** | transient failures (a lost packet, a moment of unavailability) | extra attempts add load; total load is multiplied if the failure is widespread |
| **Exponential backoff** | the recovering backend (give it time between attempts) | time; the caller waits longer each round |
| **Jitter** | synchronisation between clients (all retry at the same instant) | none, mechanically — just randomness |
| **Circuit breaker** | a backend that is clearly down (fail fast instead of pile up) | short periods of "unavailable" even when the backend recovered seconds ago |
| **Deadline (`context`)** | the caller: don't do work nobody will wait for | you may give up on a request that would have succeeded just after the deadline |

### Failure is the common case

You have seen this from the server side in [Chapter 04](ch04-tcp-sockets.md): connections arrive, the peer vanishes, deadlines fire. From the *client* side, the same failures look like this:

- `connection refused` — nothing is listening yet, or the port is wrong. **Fast error**; retrying is pointless unless the service is still booting.
- `connection reset by peer` — the server accepted then crashed. Transient, retry.
- `deadline exceeded` — the server didn't respond in time. Maybe it was overloaded (don't add load!), maybe the packet vanished (retry is fine).
- `503 Service Unavailable / 429 Too Many Requests` — the server is explicitly telling you to slow down.
- `401 Unauthorized / 404 Not Found / 400 Bad Request` — **retrying cannot change the outcome**.

### The three mechanisms, in one paragraph each

**Retry with exponential backoff.** Try the request; on failure wait `BaseDelay`, then `2 * BaseDelay`, `4 * BaseDelay`, and so on, capped by `MaxDelay`. Doubling is a bet: most transient failures come back within tens or hundreds of milliseconds, so the first retries are cheap. If the backend is genuinely down, doubling quickly reaches seconds — you stop hammering it without having to know it's down.

**Jitter.** 1000 clients all fail at the same moment (a pod dies, a DNS record changes), and all sleep exactly `BaseDelay`. At `t + BaseDelay`, 1000 requests arrive simultaneously; at `t + 2*BaseDelay`, another 1000, and so on. The backend doesn't "recover and see a few requests" — it gets the same synchronized storm every round. **Jitter desynchronises the herd**: each delay is multiplied by a random factor in `[1 - Jitter, 1]`. With `Jitter = 1` ("full jitter") a delay of `d` becomes a uniform draw from `[0, d]`.

**Circuit breaker.** Retries don't help when the backend is *down*: they just make every client burn `MaxAttempts × BaseDelay` of latency and a TCP connection before failing anyway. The breaker watches consecutive failures; at a threshold it "opens" and rejects every call instantly with `ErrCircuitOpen` without touching the network; after a cooldown it lets exactly one trial call through — half-open — and if that succeeds the breaker closes again.

### The math you should be able to do in your head

- Expected attempts per request ≈ `1 / q`, where `q` is the probability one attempt succeeds.

| Per-attempt success rate q | Expected attempts | Load multiplier |
|---|---|---|
| 0.99 | 1.01 | ~1.0× |
| 0.9 | 1.11 | ~1.1× |
| 0.5 | 2 | 2× |
| 0.1 | 10 | 10× |

A 10× amplification on an already-saturated server is how a partial outage becomes a total one.
- **Layered amplification:** three layers each doing 3 retries → up to `3³ = 27` attempts at the bottom for one user request.
- **Retry storm vs jitter:** without jitter, 1000 clients that fail together retry together; the queue is 1000 deep at every round. With full jitter their delays spread uniformly, so the load decays smoothly — the server sees a thinning queue instead of synchronised pulses.
- **A success resets the backoff and the breaker's counter** on the spot: anything that "worked" ends the suspicion.

## 2. The lesson, step by step

### Step 1 — the dichotomy: permanent vs transient

The retry loop must know the difference between "try again" and "give up now", and the caller is the only one who knows the protocol well enough to decide. So the API is a wrapper, not a guess:

```go
if resp.StatusCode >= 500 {
    return fmt.Errorf("server %d", resp.StatusCode)      // transient — retry may help
}
if resp.StatusCode == 429 {
    return fmt.Errorf("rate limited (%d)", resp.StatusCode)
}
return resilience.Permanent(fmt.Errorf("won't fix: %d", resp.StatusCode)) // 4xx — stop
```

`Permanent` stores a marker the retry loop can find with `errors.As`, so it sees through `fmt.Errorf("request failed: %w", ...)` wrapping:

```go
err := resilience.Permanent(errors.New("invalid api key"))
resilience.IsPermanent(fmt.Errorf("call: %w", err)) // true
```

Note the marker is part of the error chain, not a parallel channel: `errors.Is` and `errors.As` keep working, and nobody needs to thread a boolean through `Do`.

### Step 2 — one retry attempt, then the loop

`RetryPolicy.Do` calls `fn(attempt)` with `attempt` starting at 1. When `fn` fails with a **transient** error, Do sleeps, then calls `fn` again. The sleep before attempt `i+1` is

```
backoff(i) = min(BaseDelay * 2^(i-1), MaxDelay)
```

worked out for the defaults (`BaseDelay 100ms, MaxDelay 2s`):

| After attempt | sleep before next |
|---|---|
| 1 | 100 ms |
| 2 | 200 ms |
| 3 | 400 ms |
| 4 | 800 ms |
| 5 | last attempt — no sleep, `Do` returns the last error |

A full timeline trace of the default policy against a backend that is still starting:

```
 t=  0ms  attempt 1 ──▶ 503
 t=  5ms  sleep 100ms × jitter
 t=100ms  attempt 2 ──▶ 503
 t=105ms  sleep 200ms × jitter
 t=305ms  attempt 3 ──▶ 503
 t=310ms  sleep 400ms × jitter
 t=710ms  attempt 4 ──▶ 200 OK   ⇒ Do returns nil (the caller never sees the failures)
```

The caller sees either `nil` or the last error — never `[]error`. If all attempts fail, your logs should print the last one and, optionally, the attempt count.

Two contracts that surprise people:

- **No sleep before attempt 1, no sleep after attempt N.** Sleeping after the final attempt is pure waste: the caller has already decided to fail.
- **`MaxAttempts` includes the first try** because users think in "attempts", not "retries", and off-by-one is the retry bug everyone writes.

### Step 3 — jitter, or: the thundering herd

Apply after the exponential (and still inside the cap):

```
jittered = backoff × (1 - Jitter × rand.Float64())
```

- `Jitter = 0` → no randomness (fully deterministic; tests use this).
- `Gossip rule`: jitter **never makes a delay longer**, only shorter — hence the subtraction.
- `Jitter = 1` ("full jitter") → delay is a uniform draw from `[0, backoff]`; the herd is fully smeared.
- `Jitter = 0.2` (the default) → delay stays in `[0.8 × backoff, backoff]`, still desynchronised but bounded — the server sees a stretched but predictable load.

```
1000 clients, backend dies at 12:00:00.000, BaseDelay 100ms, no jitter:
 12:00:00.100  1000 reqs  12:00:00.200 ... (every 100 ms, synchronised pulses)

with full jitter:
 12:00:00.004 … 12:00:00.096 … (spread uniformly — no pulses)
```

Why keep part of the delay instead of sleeping 0? A retry that always fires immediately counts against the backend twice as fast. Also, decorrelation only exists if the *source* differs per process — the zero-value policy's documented `nil Rand` (a fixed `rand.NewSource(1)`) is there **only** so examples are reproducible. A real service should inject a properly seeded source, otherwise every process still wakes up in the same order.

### Step 4 — never retry past the deadline

Chapter 04 taught deadlines on sockets. They are the boss of this loop:

```go
if err := ctx.Err(); err != nil {
    return err        // before every attempt
}
...
if err := sleep(ctx, delay); err != nil {
    return err        // sleep returns ctx.Err() the moment the deadline blows
}
```

So a client that cancels (user navigated away, RPC deadline blown, parent gave up) stops *immediately*, even mid-sleep:

```
deadline = 250 ms
 t=  40ms  attempt 1 fails
 t=  40ms  sleep 100ms × jitter
 t= 140ms  attempt 2 fails
 t= 140ms  sleep 200ms — the deadline at 250 ms cuts it short
 t= 250ms  ctx done ⇒ Do returns context.DeadlineExceeded
```

Notice whose job the deadline is: `Do` never knows the deadline itself; it only watches `ctx`. The production `Sleep` is a `select` between `time.After(d)` and `ctx.Done()` — the timer is cancelled the moment the context is. The tests inject `testutil.RecordingSleeper`, which does the same check and otherwise records the delay instead of sleeping.

### Step 5 — the circuit breaker: closed, open, half-open

Fail fast instead of piling up. Three states:

```
                     failureThreshold consecutive failures
  ┌──────────┐ ───────────────────────────────▶ ┌──────────┐
  │  closed  │                                  │   open   │
  │ normal   │ ◀────────────────────────────┐   │ reject   │
  └──────────┘                              │   │ every    │
       ▲                                    │   │ call     │
       │                                     │   │ instantly│
  success in half-open                      │   └──────────┘
       │                                    │        │
  ┌────┴─────┐                              │        │ openDuration elapsed
  │ half-open│◀─────────────────────────────┘        ▼
  │ ONE trial│        next call at ≥ openedAt+openDuration
  │ allowed  │        moves the breaker to half-open and becomes the trial
  └──────────┘
        │
   trial fails ⇒ straight back to open (+ cooldown restarts)
```

Design decisions that matter:

- **Consecutive failures, not a rate.** Simple, predictable, and one success ends the streak. (A rolling 30-second window of the failure ratio is the stretch challenge.)
- **The trial is exclusive.** Exactly one caller may test the water. Everyone else gets `ErrCircuitOpen` immediately — otherwise the "one trial" is a stampede.
- **The cooldown restarts on trial failure**, not on the wall clock: a backend that is still broken gets its full recovery time.
- **Reading the clock is not a state transition.** The breaker moves open → half-open inside `Do`, at the moment it decides to accept a call; `State()` only reports what is stored.
- **`Reset()`** returns the breaker to closed with counters cleared — the operations escape hatch ("we just deployed the fix") and the fixture in tests.

Trace, threshold 3, cooldown 30s:

```
 t=  0s  closed      call fails  (consecutive 1/3)
 t=  0.5s closed     call fails  (2/3)
 t=  0.8s closed     call succeeds ⇒ counter reset
 t=  1.1s closed     call fails  (1/3)
 t=  4.2s closed     call fails  (2/3)
 t=  6.9s OPEN       call fails  (3/3, openedAt = 6.9s)
 t=  7.0s open       Do ⇒ ErrCircuitOpen (fn NOT called)
 t= 37.0s open       same
 t= 37.0s (≥ 36.9s)  Do ⇒ transition to HALF-OPEN — this call IS the trial
 t= 37.2s half-open  any other caller ⇒ ErrCircuitOpen
 t= 37.2s closed     trial returned nil — breaker closes, counter 0
```

### Step 6 — the breaker in code: lock discipline

The fn you call is the slow part (a network round-trip). Holding the mutex for it would serialize all callers — precisely the pile-up the breaker exists to prevent. So:

```go
c.mu.Lock()
// inspect / transition the state, then UNLOCK
c.mu.Unlock()

err := fn()              // the slow part, outside the lock

c.mu.Lock()
// record the outcome: increment (and maybe open) on failure, reset on success
c.mu.Unlock()
```

While the trial runs, `state` is already `half-open`; every other caller sees it and fails fast. Concurrent calls in the closed state run fn in parallel — normal.

### Step 7 — where all this sits in a real client

The order in a middleware stack, outside → inside:

```
circuit breaker  ──▶  retry policy ──▶  one HTTP attempt ──▶ the backend
```

The breaker outside, retries inside. A rejected trial should NOT be retried by the retry loop (it's not an error from the network, it's a decision); a retried request should NOT count as 3 failures at the breaker. Wiring:

```go
breaker := resilience.NewCircuitBreaker(5, 30*time.Second, resilience.RealClock{})
policy  := resilience.NewRetryPolicy()
err := breaker.Do(func() error {
    return policy.Do(ctx, func(attempt int) error {
        return callOnce(ctx, url)              // one HTTP call, per-attempt ctx deadline
    })
})
if errors.Is(err, resilience.ErrCircuitOpen) {
    return serveStaleOrFallback(ctx)           // degraded, but instant: a cache hit beats a 30s hang
}
```

Rules of use, in order of how often they're ignored:

1. **Only retry idempotent requests** unless you have idempotency keys (`GET`, `PUT`, `DELETE`, or a `POST` with a unique `Idempotency-Key`).
2. **Per-attempt timeout, overall deadline.** Retrying a request that takes 30s of CPU on a 2s deadline is malpractice; put a deadline *inside* the retry as well.
3. **Half-open is not "retry permission".** In half-open the breaker itself is testing the water; combining both multiplies traffic to a recovering backend by `2 ×` exactly when it can least absorb it.
4. **gRPC does this under the hood**: its retry policy is this machinery with a config file (`maxAttempts`, backoff, jitter), and the breaker is what proxies add around it. You now know what that config compiles down to.

## 3. Your challenge

Implement the API in `internal/resilience/resilience.go` (the doc comments restate the contract):

- `Permanent` / `IsPermanent` — the marker, seen through `%w` wrapping.
- `NewRetryPolicy` — defaults: `MaxAttempts 5, BaseDelay 100ms, MaxDelay 2s, Jitter 0.2`.`Do` — clamp `MaxAttempts` (`< 1 → 1`), default `Rand` to `rand.New(rand.NewSource(1))` (documented), default `Sleep` to a real timer that honours ctx; backoff `BaseDelay * 2^(attempt-1)`, `MaxDelay <= 0` means no cap, jitter formula as documented, no sleep after the final attempt, `ctx.Err()` returns the moment ctx is done, last error returned when attempts run out.
- `NewCircuitBreaker` — `failureThreshold < 1 → 1`, `openDuration <= 0 → 30s`, nil clock → `RealClock{}`; `Do`/`State`/`Reset`; `CircuitState.String` → `"closed"` / `"open"` / `"half-open"`.

The tests start **red** (they fail with `not implemented yet`); finish them one by one. 17 tests: 9 retry, 7 breaker, 1 clock.

What the tests inject so you never *really* wait:

- **`testutil.RecordingSleeper`** — `Sleep` returns immediately and records delays. Your `Do` must call it through the `Sleep` field; nil must fall back to a real `time.Timer`-based sleep (not exercised by tests, but part of the contract).
- **`testutil.NewFakeClock()`** — `Advance(d)` moves breaker time by hand; no sleeping is involved anywhere.
- **`testutil.Rand(seed)`** — deterministic jitter.

`-race` runs the concurrent tests (a concurrent burst on an open breaker; a trial goroutine competing with a probe call in half-open). Reading this paragraph is fine; reading the production probe code you are meant to write is not.

## 4. Hints

<details><summary>Hint 1 — the backoff arithmetic</summary>

Double from `BaseDelay` in a loop instead of computing powers (`BaseDelay * 1<<(attempt-1)` overflows int64 for large attempts):

```go
d := base
for i := 1; i < failedAttempt && (max <= 0 || d < max); i++ {
    if d > time.Duration(1<<62) { d = 1 << 62; break }
    d *= 2
}
if max > 0 && d > max { d = max }
```
Only after the cap, apply jitter: `d = time.Duration(float64(d) * (1 - jitter*rng.Float64()))` with jitter clamped to `[0, 1]`. With `Jitter == 0`, don't consume a random number at all — `testutil.Rand` tests would notice.
</details>
<details><summary>Hint 2 — the Permanent marker</summary>

A small struct `permanentError{err error}` implementing `Error()` (e.g. `"permanent: " + err.Error()`) and `Unwrap() error { return err }` makes `errors.As` the finder. `IsPermanent(nil)` must return false (`errors.As` panics on nil target — copy to a local first). `Permanent(nil)` returns nil, and `Permanent(Permanent(err))` returns the same wrapper.
</details>
<details><summary>Hint 3 — the breaker: three separate regions</summary>

```go
c.mu.Lock()
// (a) if open: `now.Sub(openedAt) >= openDuration` → become half-open and let THIS call be the trial;
//             otherwise return ErrCircuitOpen without calling fn.
// (b) if half-open: another trial is in flight → ErrCircuitOpen.
// (c) if closed: just run.
c.mu.Unlock()

err := fn()               // (d) NOTHING else may touch the breaker here — that's the whole point

c.mu.Lock()
// (e) on failure: consecutiveFailures++; open if half-open OR counter >= threshold; openedAt = clock.Now()
//     on success: state = closed; consecutiveFailures = 0; trialInFlight = false
c.mu.Unlock()
```
Every return path must unlock before `fn` runs. If you "just keep the lock", the test with a blocked trial (`Release` after an assertion) deadlocks and the watchdog prints all goroutines.
</details>
<details><summary>Hint 4 — "my tests run for seconds / hang"</summary>

You are probably using the real sleep (`Sleep` left nil) or the real timer is racing with cancellation. Check: every test injects `RecordingSleeper`; the only sleeps in production code path go through the `Sleep` field; and after a `cancel()` inside `fn`, your loop must see `ctx.Err()` — either before the next attempt or inside the next `Sleep`. `RecordingSleeper.Sleep` checks `ctx.Err()` first and returns it: it will also record nothing, so the expected delay slices are exact.
</details>
<details><summary>Hint 5 — the concurrent half-open test</summary>

The test spawns the trial goroutine, waits for `started` (closed inside the trial fn), then issues a second `Do` and expects `ErrCircuitOpen` — while `b.State()` must read `half-open`. This only works if the state is already `half-open` at the moment the trial fn is entered (i.e. the transition happens when the trial *starts*, not when it *ends*), and if `State()` doesn't force a transition. Set `trialInFlight = true` in the same locked section that flips the state, and reset it (together with the counter) when the trial concludes.
</details>

## 5. Stretch challenges

1. **Honour `Retry-After`.** Parse it (seconds or HTTP-date) on `429` and `503` and multiply your backoff by it for that one attempt — clamped to a documented max. Extend `RetryPolicy` with a probe `Delay func(err error) (time.Duration, bool)` checked before the computed one. Compare the wall-clock time-to-200 on a fake server that sends `Retry-After: 2`.
2. **Success-rate window breaker.** Replace "consecutive failures" with a ring of the last N (e.g. 20) outcomes; open when the failure ratio exceeds 50% with at least 10 calls in the window. Compare behaviour on a flaky backend (bursty isolated failures vs a slow ramp-down): consecutive counting opens later but never resets on one lucky call. Why might this be worse for very low traffic?
3. **Hedged requests.** Send the request; if no reply within p95 (or a fixed 150 ms), send a second one in parallel; return the first result, cancel the loser. Cut tail latency on an 99th-percentile-skewed service — count bandwidth cost. Explicitly answerable only for idempotent requests; what changes if one of the branches is `fn` with side effects?
4. **Retry budget.** A token bucket per client (e.g. `maxRetries = 10 %` of total calls, refill rate proportional) so that retries are starved before links are saturated. This is what distinguishes "retry helps" from "retry is a self-DoS": during a real outage the budget empties and the client fails fast instead of multiplying load.
5. **Measure the storm.** A scratch `main` (not the test binary) with an in-memory "backend" that dies for 5 seconds: count total requests seen with and without jitter, with and without the breaker. Plot requests vs time-in-seconds-after-recovery. Which of the two mechanisms actually empties the queue?

## 6. Reflect

- Why is a retried `400` worse than a retried `503` — for you, for the backend, for the user?
- What property does jitter have that pure exponential backoff doesn't, and why does it only work if the RNG differs per process?
- Why is the number of concurrent calls in half-open exactly one?
- Your colleague "fixes" a failing service by setting `MaxAttempts: 10`. Describe the incident that follows.
- The breaker "protects the server" — and the client. Name one failure mode where the breaker itself causes collateral damage (partial outage looking total; flapping; gray failure).
- If `fn` returns `ErrCircuitOpen` while the breaker is open and `Do` (retry) had not even hit the network — did that attempt fail "permanently"? What do the stretch budgets and the hedge mechanism change about your answer?
- Where would you put the per-attempt deadline vs the overall deadline in the stack, and what happens if you mix them up on a slow-but-successful endpoint?
