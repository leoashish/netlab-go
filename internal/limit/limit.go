// Package limit implements the four classic rate limiters from first principles, on the
// standard library alone: the token bucket and the leaky bucket (rate-based, with a
// burst allowance), and the fixed window and the sliding log (count-based).
//
// See docs/chapters/ch06-rate-limiting.md for the guided lesson.
package limit

import (
	"context"
	"errors"
	"sync"
	"time"

	"netlab/internal/todo"
)

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

// TokenBucket is a rate limiter that starts FULL. Tokens refill continuously at
// refillPerSec up to capacity (float accounting, so fractional tokens accumulate
// correctly between requests). Each allowed request spends one token (AllowN spends n).
//
// Constructor clamps nonsense inputs, by design:
//   - capacity < 1        → 1
//   - refillPerSec < 0    → 0   (NaN is clamped to 0 as well; +Inf works: it pins the
//     bucket at capacity, which is all it can ever hold)
//   - clock == nil        → RealClock{}
//
// A clock that moves BACKWARDS adds no tokens (refill is computed from elapsed time;
// negative elapsed is ignored). AllowN with n <= 0 is free and consumes nothing.
// AllowN with n > capacity can never succeed and consumes nothing.
// All methods are safe for concurrent use.
type TokenBucket struct {
	mu           sync.Mutex
	capacity     float64
	refillPerSec float64
	tokens       float64
	last         time.Time
	clock        Clock
	sleeper      func(ctx context.Context, d time.Duration) error
}

// NewTokenBucket returns a full bucket. sleeper is nil, so Wait sleeps for real
// (time.NewTimer) while honouring ctx. Tests that want determinism pass a sleeper via
// NewTokenBucketWithSleeper instead — a fake clock does NOT drive real timers.
func NewTokenBucket(capacity int, refillPerSec float64, clock Clock) *TokenBucket {
	return &TokenBucket{} // TODO Ch06: clamp inputs, start the bucket FULL (tokens = capacity)
}

// NewTokenBucketWithSleeper is NewTokenBucket with the waiting behaviour of Wait injected.
// The sleeper must block for (approximately) d and return ctx.Err() if ctx is cancelled
// first — testutil.RecordingSleeper.Sleep matches this signature. Tests pair it with a
// fake clock and advance the clock inside the sleeper, so Wait's re-check sees the tokens
// that "elapsed" during the sleep. With a sleeper that does NOT advance the clock, Wait
// honestly re-checks and would keep waiting; it gives up after 100 attempts.
func NewTokenBucketWithSleeper(capacity int, refillPerSec float64, clock Clock, sleeper func(context.Context, time.Duration) error) *TokenBucket {
	return &TokenBucket{} // TODO Ch06: same clamping as NewTokenBucket, plus store sleeper
}

// Allow spends 1 token, or reports false if none is available.
func (b *TokenBucket) Allow() bool {
	return false // TODO Ch06: refill from elapsed time, then spend 1 token if available
}

// AllowN spends n tokens atomically: either all n are spent, or none are.
// n <= 0 is free (returns true). n greater than the capacity can never succeed (returns false).
func (b *TokenBucket) AllowN(n int) bool {
	return false // TODO Ch06: refill, check tokens >= n, spend or refuse — under one lock
}

// Wait blocks until n tokens can be spent (it spends them), or ctx is done.
//
//   - n > capacity        → ErrExceedsCapacity, checked first, before any sleeping
//   - n <= 0              → nil, nothing to wait for
//   - refill rate is 0    → ErrZeroRefill (waiting can never help)
//   - otherwise            → sleeps deficit/refillPerSec, then re-checks; loops at most
//     100 attempts (a safety bound no real clock or honest sleeper ever reaches)
//   - ctx cancelled       → ctx.Err()
//
// The refill is computed from elapsed time on the injected Clock; with a nil sleeper the
// sleep is a real timer selecting on ctx.Done().
func (b *TokenBucket) Wait(ctx context.Context, n int) error {
	return todo.NotImplemented("Ch06: Wait")
}

// LeakyBucket is the metering twin of TokenBucket: every admitted request raises the
// bucket's level by one, and the level leaks continuously at leakPerSec. A request is
// admitted (and the level raised) iff level+1 <= capacity — i.e. a burst of capacity
// passes, then admissions settle at the leak rate. The bucket starts EMPTY (level 0):
// it has no stored credit, only the rate.
//
// (This is the policing form of the leaky bucket — dropping excess. The shaping form
// — a queue drained at leakPerSec, delaying excess instead of dropping — is a stretch
// challenge.)
//
// Constructor clamps nonsense inputs, by design:
//   - capacity < 1     → 1
//   - leakPerSec < 0   → 0   (NaN → 0 as well)
//   - clock == nil     → RealClock{}
//
// A clock that moves BACKWARDS leaks nothing (negative elapsed is ignored).
// All methods are safe for concurrent use.
type LeakyBucket struct {
	mu         sync.Mutex
	capacity   float64
	leakPerSec float64
	level      float64
	last       time.Time
	clock      Clock
}

// NewLeakyBucket returns an empty bucket that leaks leakPerSec units per second.
func NewLeakyBucket(capacity int, leakPerSec float64, clock Clock) *LeakyBucket {
	return &LeakyBucket{} // TODO Ch06: clamp inputs; bucket starts empty (level = 0)
}

// Allow admits (and counts) one request iff the level after this request stays within
// capacity. The level is leaked according to the elapsed time before the check.
func (b *LeakyBucket) Allow() bool {
	return false // TODO Ch06: leak by elapsed time (floor at 0), then admit iff level+1 <= capacity
}

// FixedWindow is a count-based limiter: at most `limit` requests are admitted per fixed
// window of `window`. The first Allow starts the first window (windows are anchored to
// that first call, not to construction, and aligned thereafter — advancing the clock by
// whole windows skips empty windows in O(1)). At the exact boundary the counter resets.
//
// The known weakness is the 2x burst at window edges: limit requests at the end of one
// window plus limit at the start of the next — see the lesson. For exact rolling
// accounting use SlidingLog; for O(1) memory with acceptable approximation, FixedWindow
// is the classic gateway choice.
//
// Constructor clamps nonsense inputs, by design:
//   - limit < 1       → 1
//   - window <= 0     → time.Second
//   - clock == nil    → RealClock{}
//
// A clock that moves BACKWARDS does not reset windows (negative elapsed is ignored).
// All methods are safe for concurrent use.
type FixedWindow struct {
	mu          sync.Mutex
	limit       int
	window      time.Duration
	count       int
	windowStart time.Time // zero value until the first Allow: the first call starts the window
	clock       Clock
}

// NewFixedWindow returns an empty fixed-window counter.
func NewFixedWindow(limit int, window time.Duration, clock Clock) *FixedWindow {
	return &FixedWindow{} // TODO Ch06: clamp limit to >= 1 and window to > 0
}

// Allow admits iff fewer than `limit` requests were admitted in the current window.
func (f *FixedWindow) Allow() bool {
	return false // TODO Ch06: start/skip whole aligned windows as needed, then count or refuse
}

// SlidingLog is an exact rolling-window limiter: it remembers the timestamp of every
// admitted request and drops those older than the window. A hit exactly `window` old
// HAS expired (age == window counts as expired). A request is admitted iff fewer than
// `limit` hits remain in the window.
//
// Cost: O(limit) memory per limiter and O(limit) work per Allow — use it when the
// precision matters (low-volume, high-value endpoints) and prefer FixedWindow or a
// token bucket when volume is high.
//
// Constructor clamps nonsense inputs, by design:
//   - limit < 1      → 1
//   - window <= 0    → time.Second
//   - clock == nil   → RealClock{}
//
// All methods are safe for concurrent use.
type SlidingLog struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	hits   []time.Time
	clock  Clock
}

// NewSlidingLog returns an empty sliding log.
func NewSlidingLog(limit int, window time.Duration, clock Clock) *SlidingLog {
	return &SlidingLog{} // TODO Ch06: clamp limit to >= 1 and window to > 0
}

// Allow drops the hits older than the window and admits iff fewer than `limit` remain.
func (s *SlidingLog) Allow() bool {
	return false // TODO Ch06: cutoff = now - window; drop hits not after cutoff; admit iff len(hits) < limit
}
