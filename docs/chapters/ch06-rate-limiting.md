# Chapter 06 — Rate limiting: token buckets, leaky buckets, and window counters

| | |
|---|---|
| **Build** | `Clock`, `NewTokenBucket`, `NewTokenBucketWithSleeper`, `TokenBucket.Allow/AllowN/Wait`, `NewLeakyBucket`, `NewFixedWindow`, `NewSlidingLog` — `internal/limit/limit.go` |
| **Tests** | `internal/limit/limit_test.go` — 17 tests |
| **Run** | `go test ./internal/limit -run Ch06 -race -v` |
| **Prerequisites** | [prereq 02 — Go essentials](../prereq/02-go-essentials.md), [prereq 03 — concurrency primitives](../prereq/03-concurrency-primitives.md), [prereq 05 — back-pressure](../prereq/05-networking-vocabulary.md) |
| **Time** | ~75 min |

## 1. Why this matters for network programming

Rate limiting is the network's answer to *too much, too fast*. It does three jobs that show up in every incident postmortem:

1. **Protect the origin.** A backend that serves 100 req/s gracefully does something ugly at 10,000 req/s — the accept queue overflows, the thread pool starves, GC death-spirals, and now *nothing* works. The limiter is the shock absorber in front.
2. **Fairness.** One tenant — or one leaked API key, or one misbehaving mobile app — must not starve everyone else. Limits are **per key** (per IP, per API key, per tenant): a row of turnstiles, not one.
3. **Survive retry storms.** When a service hiccups, every client retries at once. Retries *multiply* load precisely when the system is weakest — a brief blip becomes an outage. Client-side limiting of retries, and shedding server-side, breaks the feedback loop.

Where it lives — and this is the shape of our capstone:

```
clients ──▶ [ reverse proxy / API gateway ] ──▶ origin servers
             │  per-key limiter                  │
             │  allow → forward                   │
             │  deny  → 429 Too Many Requests     │
             │           Retry-After: 1           │
             └─ egress limiters too: never DDoS your own dependency
```

Connections to the rest of the course:

- **prereq 05's back-pressure**: when a producer outruns a consumer, something must *absorb, drop, or delay*. The limiter is the explicit L7 version of that decision — typically *drop* (429), sometimes *delay* (`Wait`).
- **Chapter 13 (pools)**: a connection pool bounds *concurrency* (how many at once); a rate limiter bounds *rate* (how many per second). Different knobs, same goal: keep the downstream inside its happy zone. A pool makes you wait; a limiter usually says "no".
- **Chapter 15 (LB)**: a load balancer spreads requests across backends, but if the *total* exceeds their capacity, spreading is not enough — only limiting (or queueing) prevents collapse.

## 2. The lesson, step by step

### Step 1 — the two rate meters, one intuition

**Token bucket** — *spend credit*. The bucket holds up to `capacity` tokens, **starts full**, each allowed request spends one token, and tokens **refill continuously** at `refillPerSec`. Refill and spend race; whichever wins, the balance never goes below 0 or above `capacity`.

**Leaky bucket** — *relieve pressure*. The bucket starts **empty**; each admitted request raises the level by 1; the level **leaks continuously** at `leakPerSec`; a request is admitted iff `level+1 <= capacity`.

```
     token bucket                 leaky bucket (policing)
   ┌─────────────┐                    ▲ level
   │ ●●●●○○○     │ + refill/s        ┌─┼──────── capacity  → reject above this line
   └──────┬──────┘                   │ │
          ▼ -1 per request           │ │
        forward                      └─┴──────── leak -1/s, floor 0
```

The two are mathematical twins: both admit a **burst of up to `capacity`** and then settle at the **rate**. The mental models differ — tokens are *saved credit* ("I was idle, so I may burst"); the leaky level is *outstanding pressure* ("I am behind, so I must slow down"). The classic leaky bucket that *smooths* a stream (a queue drained at `leakPerSec`, delaying excess instead of dropping it) is the shaping variant — see the stretch challenges.

### Step 2 — trace a bucket, second by second

`capacity = 6`, `refillPerSec = 2`, starting full. Requests arrive at the listed instants:

| time | refill since last | tokens before | requests | outcome | tokens after |
|---|---|---|---|---|---|
| 0.0 s | — | 6.0 | 8 × Allow | 6 allowed, 2 denied | 0.0 |
| 0.5 s | +1.0 | 1.0 | 1 × Allow | allowed | 0.0 |
| 1.0 s | +1.0 | 1.0 | 2 × Allow | 1 allowed, 1 denied | 0.0 |
| 2.0 s | +2.0 | 2.0 | 3 × Allow | 2 allowed, 1 denied | 0.0 |
| 6.0 s | +8.0 → **capped at 6** | 6.0 | 6 × Allow | all allowed | 0.0 |
| 6.5 s | +1.0 | 1.0 | 1 × Allow | allowed | 0.0 |

Three things to internalize:

- **Refill is proportional to elapsed time**, not a timer: `tokens += elapsed × rate`, then `min(tokens, capacity)`. An idle hour does not bank 7,200 tokens — the cap is the burst size.
- **Fractional tokens are real**: `0.3 s × 2/s = 0.6` tokens. This is why the accounting is `float64`; rounding to integers would silently change the rate at low rates.
- **The bucket starts full.** A fresh limiter lets the first `capacity` requests through instantly — that is a *feature* (legitimate cold-start bursts), and a test will check it.

### Step 3 — time is injected: `Clock`, and the sleeper seam

Every limiter is a pure function of `(state, now)`. So `now` comes from an interface:

```go
type Clock interface{ Now() time.Time }   // RealClock{} in production, FakeClock in tests
```

Tests use `testutil.NewFakeClock()` and its `Advance` — deterministic, zero real sleeping (same discipline as Chapter 05's memlink). `nil` means `RealClock{}`, so production code can ignore the parameter.

`Wait` needs one more seam. It must *sleep*, and here is the trap: **`time.After` does not consult our fake clock**. A test that calls `Wait` on a fake clock would either wait for real (slow, and the clock still has not moved) or deadlock. So the sleeping itself is injectable:

```go
b := limit.NewTokenBucketWithSleeper(capacity, rate, clock, sleeper)
// production:  NewTokenBucket(...)   → sleeper == nil → real timer racing ctx.Done()
// tests:       RecordingSleeper.Sleep records the delay; the test advances the fake
//              clock inside the sleeper, so Wait's re-check sees the elapsed tokens.
```

This is the same trick as Chapter 05's deterministic loss: *inject the part that is really about time, keep the logic pure.*

### Step 4 — `AllowN`: check and consume are ONE critical section

```go
b.mu.Lock()
defer b.mu.Unlock()
b.refillLocked(b.clock.Now())
if float64(n) > b.capacity || b.tokens < float64(n) { return false }  // consumes nothing
b.tokens -= float64(n)
return true
```

Atomicity is the whole game: if two goroutines both read `tokens == 1` and each decides "there is one for me", the bucket over-spends. One mutex, one decision. The concurrency test throws 50 goroutines at a capacity-25 bucket and demands **exactly** 25 admissions — try it with the lock moved or the check and consume split apart and watch it fail under `-race`/`-count=100`.

`n <= 0` is free (`true`); `n > capacity` can never be satisfied (`false`) — fail now rather than pretend. All clamps from the stub doc apply: `capacity < 1 → 1`, negative (or NaN) rates `→ 0`.

### Step 5 — `Wait`: the delay that makes the request honest

Blocking is the "delay" arm of absorb/drop/delay. `Wait(ctx, n)` spends `n` tokens like `AllowN`, but if they are not there yet, it computes exactly how long they will take and sleeps that much:

```
deficit = n - tokens          (e.g. 1 token missing)
delay   = deficit / rate      (e.g. 1 / 10/s = 100 ms)
```

The order of checks is part of the contract:

| situation | result |
|---|---|
| `n > capacity` | `ErrExceedsCapacity` — **checked first, before any sleep**; waiting can never help |
| `n <= 0` | `nil`, immediately |
| tokens available now | spend, `nil`, no sleep |
| short, but `refillPerSec == 0` | `ErrZeroRefill` — waiting can never help either, so fail fast instead of blocking forever |
| short, rate > 0 | sleep `deficit/rate`, **re-check** (another goroutine may have taken them), loop at most 100 attempts |
| `ctx` cancelled during the sleep | `ctx.Err()` (`context.Canceled` / `DeadlineExceeded`) |

Two honest notes:

- The re-check loop exists because time may pass *differently* than you expected (another waiter drained the tokens). 100 attempts is a safety bound no real clock reaches; a `Wait` whose clock never advances gives up with a clear error instead of spinning.
- `ErrExceedsCapacity` vs `ErrZeroRefill` is a small lesson in API honesty: "impossible" and "impossible *for now*" deserve different errors, not a silent hang.

### Step 6 — fixed windows and the 2× edge problem

The cheapest count-based limiter: keep `(windowStart, count)`; reset at the boundary; allow while `count < limit`.

```
limit = 100 requests / minute

window 1: [00:00 ─────────────── 01:00)     window 2: [01:00 ─────────────── 02:00)
                        ▲ 100 requests at 00:59:59.9   ▲ 100 requests at 01:00:00.1
                        └────────── 200 requests admitted in 0.2 s ──────────┘
```

Both bursts are "legal" — each sits inside its own window — yet the backend just got **2× the limit**. That is the fixed window's known flaw: burstiness at the edges. It is still everywhere (rate = `limit` per window keyed by `windowStart.Unix()/windowSecs` in Redis) because it is O(1) memory, trivially shareable, and *good enough* for coarse quotas.

Two implementation details in this chapter:

- **The first call starts the window** (not construction): an idle limiter does not burn a window before its first request.
- **Boundaries are aligned and exact.** A request at exactly `window` after the start belongs to the new window; we skip whole elapsed windows in O(1) (`elapsed / window`) instead of resetting to `now` (which would slide and distort the limit).

### Step 7 — sliding logs: exact, at a price

If 2× edges are unacceptable, remember *every* admitted hit and judge against a rolling cutoff:

```
now = 10:00:30, window = 60s
hits: [10:00:05, 10:00:20, 09:59:50]           cutoff = 09:59:30
      keep 10:00:05, 10:00:20  (after cutoff)
      drop 09:59:50            (older than window → expired)
allow iff len(kept hits) < limit
```

Boundary precision matters: a hit exactly `window` old **has expired** (age == window), which is `h.After(now.Add(-window))` — strict `After`, not `Before`. A test advances the clock by exactly the window and expects the oldest hit to be gone.

The price: **O(limit) memory and O(limit) work per request**, per key. Fine for a login endpoint (limit 5/min, and abuse costs money); wrong for a CDN edge doing 500k req/s. That is the memory-vs-precision trade the fixed window bought its way out of.

### Step 8 — choosing one

| | burst behavior | memory per key | edge behavior | typical use |
|---|---|---|---|---|
| **Token bucket** | admits capacity burst, then rate | O(1) | none — resets continuously | general purpose: APIs, egress, retries |
| **Leaky bucket (metering)** | same burst bound, drained by rate | O(1) | none | when "outstanding pressure" is the natural frame |
| **Leaky bucket (shaping)** | excess is *delayed*, not dropped | O(queue) | none | constant-rate egress (video, radio) |
| **Fixed window** | up to 2× limit at the seam | O(1) | **2× burst at boundaries** | cheap quotas, coarse fairness |
| **Sliding log** | exact, never more than limit in any window | O(limit) | none — exact | low-volume, high-value endpoints |

Production stacks often combine them: a token bucket for burst control, a fixed window as a coarse quota, and a per-connection cap for concurrency.

## 3. Your challenge

Implement everything in `internal/limit/limit.go`. The tests start red (they fail with `not implemented yet`); the doc comments in the stub restate each contract precisely. Checklist:

- `RealClock` — one line, but every limiter needs it.
- `TokenBucket` — starts **full**; continuous float refill capped at capacity; `Allow`/`AllowN` atomic; `Wait` with `ErrExceedsCapacity`, `ErrZeroRefill`, `ctx.Err()`, and the sleeper seam (`NewTokenBucketWithSleeper`).
- `LeakyBucket` — starts **empty**; leaks by elapsed time with a floor at 0.
- `FixedWindow` — first call starts the window; aligned, exact boundaries.
- `SlidingLog` — strict `After` cutoff; a hit exactly `window` old is expired.
- Clamps everywhere: `capacity < 1 → 1`, `limit < 1 → 1`, negative rates → 0, non-positive windows → 1 s, nil clock → `RealClock{}`.
- All four types safe for concurrent use (one mutex each).

Run `go test ./internal/limit -run Ch06 -race -v` — `-race` is mandatory, this chapter is about shared state. 17 tests; no goroutines are spawned by the package, so there is no `NoLeaks` test here.

## 4. Hints

<details><summary>Hint 1 — the refill line, and why `last` is a field</summary>

```go
func (b *TokenBucket) refillLocked(now time.Time) {
    elapsed := now.Sub(b.last)
    if elapsed <= 0 { return }              // backwards or frozen clock: no tokens, keep last
    if b.refillPerSec > 0 {
        b.tokens += elapsed.Seconds() * b.refillPerSec
        if b.tokens > b.capacity { b.tokens = b.capacity }
    }
    b.last = now                            // advance the watermark even when rate == 0
}
```
`elapsed.Seconds()` is a `float64`; multiply by the rate and you have fractional tokens for free. Call `refillLocked(b.clock.Now())` as the first line of every `TokenBucket` operation, while holding the lock.
</details>

<details><summary>Hint 2 — AllowN: refuse without spending</summary>

Refill, then decide, then spend — all under the same lock. The classic bug is consuming *before* checking (`tokens -= n` first, then `tokens >= 0`), which would make a failed `AllowN(7)` cost you 7 tokens. A failed call must leave the bucket exactly as it was. `n > capacity` → `false`; `n <= 0` → `true`; otherwise compare `b.tokens` with `float64(n)`.
</details>

<details><summary>Hint 3 — Wait: check order and the sleeper</summary>

Order matters: (1) `n > capacity` → `ErrExceedsCapacity` *before any sleep*, (2) `n <= 0` → `nil`, (3) loop: refill, if enough → spend and return; if `refillPerSec == 0` → `ErrZeroRefill`; else `delay := time.Duration(deficit / rate * float64(time.Second))`, guard `delay <= 0` (round-off) by using `time.Nanosecond`, then sleep. The sleep is `b.sleeper(ctx, delay)` if set, else a real `time.NewTimer` racing `ctx.Done()`. A cancelled context makes `RecordingSleeper.Sleep` return `ctx.Err()` without recording — the test relies on that.
</details>

<details><summary>Hint 4 — sliding log: strict After, reuse the slice</summary>

```go
cutoff := now.Add(-s.window)
kept := s.hits[:0]
for _, h := range s.hits {
    if h.After(cutoff) { kept = append(kept, h) }   // h == cutoff is expired
}
s.hits = kept
if len(s.hits) >= s.limit { return false }
s.hits = append(s.hits, now)
return true
```
`kept := s.hits[:0]` filters in place — no allocation per request. If you used `!h.Before(cutoff)` you would keep hits that are exactly `window` old, and the boundary test fails by one nanosecond.
</details>

<details><summary>Hint 5 — "my fixed window shows 2× the limit, is that a bug?"</summary>

No — that is the algorithm (Step 6). Your job is only the aligned bookkeeping: first call sets `windowStart`; when `now.Sub(windowStart) >= window`, advance `windowStart` by whole windows (`elapsed / window`) and zero the count; then admit iff `count < limit`. Do **not** reset `windowStart = now` — that slides the window and makes the effective limit depend on arrival patterns. And do not forget the clamps from the stub doc; the `ConstructorsClampNonsenseInputs` test pokes every one of them.
</details>

## 5. Stretch challenges

1. **Per-tenant weighted buckets.** `type Limiter struct { mu sync.Mutex; buckets map[string]*TokenBucket }` with per-tier capacity/rate (free: 10 burst / 1 per s; pro: 100 / 20). What do you evict when the map grows unboundedly? (Hint: last-seen timestamp + LRU — the same problem as a connection pool.)
2. **HTTP middleware.** `func (b *TokenBucket) Middleware(next http.Handler) http.Handler` — on `!Allow()` write `429 Too Many Requests` with a `Retry-After: 1` header and do not call `next`; otherwise forward. Mount it in front of the capstone's reverse proxy.
3. **Prometheus-style results.** Add `AllowWithResult(n int) (allowed bool, remaining float64, retryAfter time.Duration)`: `remaining` is what a dashboard graphs; `retryAfter` is the deficit/rate formula from `Wait` and fills the `Retry-After` header on the 429 path. Why do dashboards prefer "remaining tokens" over "drops"?
4. **Sliding-window counter.** The middle ground: keep two counters (previous and current window) and judge `prev × (1 − elapsed/window) + curr`. O(1) memory, removes the worst of the 2× edge burst. Compare its error band with the fixed window's.
5. **Shaping leaky bucket.** Implement the *queueing* variant: admitted requests go into a buffered channel; a ticker at `leakPerSec` drains it to the output, so bursts come out smoothed. What happens when the queue is full — and why does a shaper add latency by design? Connect it to Chapter 01's unbuffered back-pressure.
6. **Client-side retry limiter.** Wrap a retry loop (or the capstone's HTTP client) so that retries are themselves rate-limited by a token bucket. This is the cheapest cure for retry storms; make the case with numbers: 10,000 clients × 3 retries with and without a limiter.

## 6. Reflect

- Why must the check *and* the consume be one critical section? Construct the interleaving that over-spends a 1-token bucket when they are split.
- Token bucket starts full, leaky starts empty — for the same capacity and rate, both admit a `capacity` burst. So what actually distinguishes "credit" (token) from "pressure" (leaky) thinking? When does the distinction change a design decision?
- The fixed window admits 2× at the seam. Why do so many real gateways still use it? What would you have to store to fix it, and who pays for that storage?
- Sliding log is exact but O(limit) per key. Name an endpoint where that is obviously right, and one where it is obviously wrong.
- `Wait` returns `ErrZeroRefill` instead of blocking forever when the refill rate is 0. Why is an error better than a silent hang here — and who would ever configure a zero-refill bucket on purpose? (Hint: `Wait` with a *huge* capacity and rate 0 is "one-shot permission".)
- A load balancer (Ch15) spreads load; a pool (Ch13) bounds concurrency; a limiter bounds rate. Which one keeps a blip from becoming a retry-storm outage, and which two only shift where the fire is?
- Where exactly would you mount limiters in our capstone: edge per-IP before auth, per-API-key after auth, egress per-backend? What key would you use at each hop, and what limit would you put on the *health checker* so it never triggers its own storm?
