// Package limit implements the four classic rate limiters from first principles, on the
// standard library alone: the token bucket and the leaky bucket (rate-based, with a
// burst allowance), and the fixed window and the sliding log (count-based).
//
// Reference implementation — chapter 06 solution.
package limit

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"
)

// maxWaitAttempts bounds Wait's sleep/re-check loop. Real clocks and honest sleepers
// always succeed on the first or second attempt; the bound only stops a Wait whose
// clock never advances from spinning forever.
const maxWaitAttempts = 100

// Clock abstracts time so tests can inject a fake (see testutil.NewFakeClock).
// Production callers pass RealClock{} (or nothing, via nil — see the constructors).
type Clock interface{ Now() time.Time }

// RealClock is the production Clock: it reads the wall clock.
type RealClock struct{}

// Now returns the current wall-clock time.
func (RealClock) Now() time.Time { return time.Now() }

// ErrExceedsCapacity is returned by Wait when n is larger than the bucket's total capacity:
// no amount of refilling will ever produce that many tokens.
var ErrExceedsCapacity = errors.New("request exceeds bucket capacity")

// ErrZeroRefill is returned by Wait when the bucket is short on tokens and its refill rate
// is zero: waiting can never help, so instead of blocking forever Wait fails fast.
var ErrZeroRefill = errors.New("zero refill rate and not enough tokens available")

// clockOrReal replaces a nil clock with the wall clock, so callers can pass nil.
func clockOrReal(c Clock) Clock {
	if c == nil {
		return RealClock{}
	}
	return c
}

// clampCapacity turns a non-positive capacity into 1.
func clampCapacity(capacity int) float64 {
	if capacity < 1 {
		return 1
	}
	return float64(capacity)
}

// clampRate turns negative and NaN rates into 0. (+Inf is left alone: it simply pins the
// bucket at capacity, which is all it can ever hold anyway.)
func clampRate(rate float64) float64 {
	if rate < 0 || math.IsNaN(rate) {
		return 0
	}
	return rate
}

// TokenBucket is a rate limiter that starts FULL. Tokens refill continuously at
// refillPerSec up to capacity (float accounting, so fractional tokens accumulate
// correctly between requests). Each allowed request spends one token (AllowN spends n).
type TokenBucket struct {
	mu           sync.Mutex
	capacity     float64
	refillPerSec float64
	tokens       float64
	last         time.Time
	clock        Clock
	sleeper      func(ctx context.Context, d time.Duration) error
}

// NewTokenBucket returns a full bucket with no injected sleeper: Wait sleeps for real
// (time.NewTimer) while honouring ctx. Tests that want determinism pass a sleeper via
// NewTokenBucketWithSleeper instead — a fake clock does NOT drive real timers.
func NewTokenBucket(capacity int, refillPerSec float64, clock Clock) *TokenBucket {
	return NewTokenBucketWithSleeper(capacity, refillPerSec, clock, nil)
}

// NewTokenBucketWithSleeper is NewTokenBucket with the waiting behaviour of Wait injected.
// The sleeper must block for (approximately) d and return ctx.Err() if ctx is cancelled
// first — testutil.RecordingSleeper.Sleep matches this signature. Tests pair it with a
// fake clock and advance the clock inside the sleeper, so Wait's re-check sees the tokens
// that "elapsed" during the sleep.
func NewTokenBucketWithSleeper(capacity int, refillPerSec float64, clock Clock, sleeper func(context.Context, time.Duration) error) *TokenBucket {
	c := clockOrReal(clock)
	cap := clampCapacity(capacity)
	return &TokenBucket{
		capacity:     cap,
		refillPerSec: clampRate(refillPerSec),
		tokens:       cap, // starts FULL: the whole capacity is the initial burst
		last:         c.Now(),
		clock:        c,
		sleeper:      sleeper,
	}
}

// refillLocked adds the tokens accrued since b.last, capped at capacity, and moves
// b.last to now. A clock that moved backwards (or did not move) adds nothing.
func (b *TokenBucket) refillLocked(now time.Time) {
	elapsed := now.Sub(b.last)
	if elapsed <= 0 {
		return
	}
	if b.refillPerSec > 0 {
		b.tokens += elapsed.Seconds() * b.refillPerSec
		if b.tokens > b.capacity {
			b.tokens = b.capacity
		}
	}
	b.last = now
}

// Allow spends 1 token, or reports false if none is available.
func (b *TokenBucket) Allow() bool { return b.AllowN(1) }

// AllowN spends n tokens atomically: either all n are spent, or none are.
// n <= 0 is free. n greater than the capacity can never succeed.
func (b *TokenBucket) AllowN(n int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refillLocked(b.clock.Now())

	if n <= 0 {
		return true
	}
	need := float64(n)
	if need > b.capacity || b.tokens < need {
		return false // refuse without consuming anything
	}
	b.tokens -= need
	return true
}

// Wait blocks until n tokens can be spent (it spends them), or ctx is done.
// See the package lesson for the full contract; the short version:
// n > capacity → ErrExceedsCapacity (before any sleeping); refill == 0 and tokens
// missing → ErrZeroRefill; otherwise sleep deficit/refillPerSec and re-check.
func (b *TokenBucket) Wait(ctx context.Context, n int) error {
	if float64(n) > b.capacity { // checked first: no sleep, an impossible request fails now
		return ErrExceedsCapacity
	}
	if n <= 0 {
		return nil
	}

	for attempt := 0; attempt < maxWaitAttempts; attempt++ {
		b.mu.Lock()
		b.refillLocked(b.clock.Now())
		if b.tokens >= float64(n) {
			b.tokens -= float64(n)
			b.mu.Unlock()
			return nil
		}
		deficit := float64(n) - b.tokens
		rate := b.refillPerSec
		b.mu.Unlock()

		if rate == 0 {
			return ErrZeroRefill // waiting can never produce a token
		}
		delay := time.Duration(deficit / rate * float64(time.Second))
		if delay <= 0 { // round-off: never busy-loop on a zero-length sleep
			delay = time.Nanosecond
		}
		if err := b.sleep(ctx, delay); err != nil {
			return err // ctx.Err(), from the real timer or the injected sleeper
		}
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	return errors.New("limit: Wait exhausted its attempts without enough tokens (is the clock advancing?)")
}

// sleep waits for d. With an injected sleeper it delegates; otherwise it is a real timer
// racing ctx.Done().
func (b *TokenBucket) sleep(ctx context.Context, d time.Duration) error {
	if b.sleeper != nil {
		return b.sleeper(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// LeakyBucket is the metering twin of TokenBucket: every admitted request raises the
// bucket's level by one, and the level leaks continuously at leakPerSec. A request is
// admitted (and the level raised) iff level+1 <= capacity — i.e. a burst of capacity
// passes, then admissions settle at the leak rate. The bucket starts EMPTY.
type LeakyBucket struct {
	mu         sync.Mutex
	capacity   float64
	leakPerSec float64
	level      float64
	last       time.Time
	clock      Clock
}

// NewLeakyBucket returns an empty bucket that leaks leakPerSec units per second.
// capacity < 1 → 1; leakPerSec < 0 (or NaN) → 0; clock == nil → RealClock{}.
func NewLeakyBucket(capacity int, leakPerSec float64, clock Clock) *LeakyBucket {
	c := clockOrReal(clock)
	return &LeakyBucket{
		capacity:   clampCapacity(capacity),
		leakPerSec: clampRate(leakPerSec),
		level:      0, // starts empty: no stored credit, only the burst bound
		last:       c.Now(),
		clock:      c,
	}
}

// leakLocked drains the bucket according to the elapsed time. The level never goes
// below zero, and a clock that moved backwards drains nothing.
func (b *LeakyBucket) leakLocked(now time.Time) {
	elapsed := now.Sub(b.last)
	if elapsed <= 0 {
		return
	}
	b.level -= elapsed.Seconds() * b.leakPerSec
	if b.level < 0 {
		b.level = 0
	}
	b.last = now
}

// Allow admits (and counts) one request iff the level after this request stays within
// capacity. The level is leaked according to the elapsed time before the check.
func (b *LeakyBucket) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.leakLocked(b.clock.Now())
	if b.level+1 <= b.capacity {
		b.level++
		return true
	}
	return false
}

// FixedWindow is a count-based limiter: at most `limit` requests are admitted per fixed
// window of `window`. The first Allow starts the first window; boundaries are aligned
// to it forever after (a long jump skips empty windows in O(1)), and a counter at the
// exact boundary belongs to the new window.
type FixedWindow struct {
	mu          sync.Mutex
	limit       int
	window      time.Duration
	count       int
	windowStart time.Time // zero value until the first Allow: the first call starts the window
	clock       Clock
}

// NewFixedWindow returns an empty fixed-window counter.
// limit < 1 → 1; window <= 0 → time.Second; clock == nil → RealClock{}.
func NewFixedWindow(limit int, window time.Duration, clock Clock) *FixedWindow {
	if limit < 1 {
		limit = 1
	}
	if window <= 0 {
		window = time.Second
	}
	return &FixedWindow{limit: limit, window: window, clock: clockOrReal(clock)}
}

// Allow admits iff fewer than `limit` requests were admitted in the current window.
func (f *FixedWindow) Allow() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.clock.Now()

	if f.windowStart.IsZero() {
		f.windowStart = now // the first call starts the first window
	} else if elapsed := now.Sub(f.windowStart); elapsed >= f.window {
		// Skip every whole window that has passed, so boundaries stay aligned to the
		// first window instead of sliding forward on every request.
		whole := elapsed / f.window
		f.windowStart = f.windowStart.Add(whole * f.window)
		f.count = 0
	}

	if f.count >= f.limit {
		return false
	}
	f.count++
	return true
}

// SlidingLog is an exact rolling-window limiter: it remembers the timestamp of every
// admitted request and drops those older than the window. A hit exactly `window` old
// has expired (age == window counts as expired). Cost: O(limit) memory per limiter.
type SlidingLog struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	hits   []time.Time
	clock  Clock
}

// NewSlidingLog returns an empty sliding log.
// limit < 1 → 1; window <= 0 → time.Second; clock == nil → RealClock{}.
func NewSlidingLog(limit int, window time.Duration, clock Clock) *SlidingLog {
	if limit < 1 {
		limit = 1
	}
	if window <= 0 {
		window = time.Second
	}
	return &SlidingLog{limit: limit, window: window, clock: clockOrReal(clock)}
}

// Allow drops the hits older than the window and admits iff fewer than `limit` remain.
func (s *SlidingLog) Allow() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()

	// A hit at exactly now-window is `window` old and therefore expired:
	// `h.After(cutoff)` is false for h == cutoff.
	cutoff := now.Add(-s.window)
	kept := s.hits[:0]
	for _, h := range s.hits {
		if h.After(cutoff) {
			kept = append(kept, h)
		}
	}
	s.hits = kept

	if len(s.hits) >= s.limit {
		return false
	}
	s.hits = append(s.hits, now)
	return true
}
