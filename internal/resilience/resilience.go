// Package resilience implements the client-side survival kit of every RPC client:
// retries with exponential backoff and jitter, a permanent-vs-transient error
// distinction, and a circuit breaker that stops hammering a backend that is down.
//
// See docs/chapters/ch07-client-resilience.md for the guided lesson.
package resilience

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"time"

	"netlab/internal/todo"
)

// Clock abstracts "now" so the circuit breaker can be tested without sleeping:
// production passes RealClock, tests pass testutil.NewFakeClock().
type Clock interface {
	Now() time.Time
}

// RealClock is the Clock you use in production.
type RealClock struct{}

// Now returns the current local time.
func (RealClock) Now() time.Time {
	// TODO Ch07: return time.Now().
	return time.Time{}
}

// Permanent wraps err to mark it as non-retryable: by the time the caller sees it,
// repeating the exact same request cannot possibly succeed (a 4xx, an authentication
// failure, a malformed payload, "user does not exist").
//
// The result unwraps to err, so errors.Is/errors.As still find the original error
// through it. Permanent(nil) returns nil, and Permanent(Permanent(err)) returns the
// same wrapper (marking twice is harmless).
func Permanent(err error) error {
	// TODO Ch07: return nil for nil; wrap otherwise in a small type with Error() + Unwrap().
	return todo.NotImplemented("Ch07: Permanent")
}

// IsPermanent reports whether err, or anything it wraps, was marked by Permanent.
// It must see through fmt.Errorf("...: %w", ...) wrapping (use errors.As).
func IsPermanent(err error) bool {
	// TODO Ch07.
	return false
}

// RetryPolicy describes how RetryPolicy.Do retries a failing operation.
//
// The zero value is not usable (one attempt, zero delays): start from
// NewRetryPolicy and adjust the fields you care about.
type RetryPolicy struct {
	// MaxAttempts is the total number of attempts, including the first.
	// Values below 1 are treated as 1 (a single attempt, no retries).
	MaxAttempts int

	// BaseDelay is the delay before the SECOND attempt. Each further retry doubles it:
	// BaseDelay * 2^(attempt-1), capped by MaxDelay.
	BaseDelay time.Duration

	// MaxDelay caps the exponential growth. MaxDelay <= 0 means "no cap".
	MaxDelay time.Duration

	// Jitter is in [0, 1] and shrinks every delay by a random fraction:
	//
	//	delay = delay * (1 - Jitter*rand.Float64())
	//
	// Jitter 0 disables randomness; Jitter 1 ("full jitter") spreads a delay of d over
	// [0, d]. Values outside [0, 1] are clamped. Jitter never INCREASES a delay.
	Jitter float64

	// Rand is the source of jitter randomness. A nil Rand means "use a fresh,
	// deterministically seeded source" (rand.New(rand.NewSource(1))), documented so
	// examples and tests replay identically. Tests inject testutil.Rand(seed).
	//
	// Production should inject a genuinely random source — using the same seed in every
	// process would keep every client synchronized, which is exactly what jitter exists
	// to prevent. A *rand.Rand is not safe for concurrent use; Do is not either.
	Rand *rand.Rand

	// Sleep waits for d and returns nil, or returns ctx.Err() early if ctx is done first.
	// A nil Sleep means "use a real timer". Tests inject
	// (*testutil.RecordingSleeper).Sleep so no test ever really sleeps.
	Sleep func(ctx context.Context, d time.Duration) error
}

// NewRetryPolicy returns a policy with production-sane defaults:
//
//	MaxAttempts 5, BaseDelay 100ms, MaxDelay 2s, Jitter 0.2, nil Rand and Sleep.
func NewRetryPolicy() RetryPolicy {
	// TODO Ch07: return RetryPolicy{MaxAttempts: 5, BaseDelay: 100*time.Millisecond, ...}.
	return RetryPolicy{}
}

// Do calls fn(attempt) with attempt starting at 1, and keeps trying according to r:
//
//   - fn returns nil            → Do returns nil.
//   - IsPermanent(err) is true  → Do returns that error immediately: no sleep, no retry.
//   - any other error           → Do sleeps, then retries: the sleep before attempt i+1 is
//     BaseDelay * 2^(i-1), capped at MaxDelay, then shrunk by Jitter.
//   - MaxAttempts exhausted     → Do returns the LAST error (never a summary or a join).
//   - ctx is done before an attempt or during a sleep → Do returns ctx.Err().
//     A retry must never outlive the deadline it was called under.
//
// There is no sleep after the final attempt, and no sleep before the first.
func (r RetryPolicy) Do(ctx context.Context, fn func(attempt int) error) error {
	// TODO Ch07: clamp MaxAttempts; pick rng (default rand.New(rand.NewSource(1)));
	// pick sleep (default: a real timer that selects on ctx.Done());
	// loop: check ctx, call fn, handle nil/permanent, sleep, retry.
	return todo.NotImplemented("Ch07: RetryPolicy.Do")
}

// CircuitState is the breaker's state. See the lesson for the state machine:
//
//	closed → (failureThreshold consecutive failures) → open
//	open   → (openDuration elapsed)                  → half-open (one trial call)
//	half-open → success → closed | failure → open (the cooldown restarts)
type CircuitState int

const (
	CircuitClosed   CircuitState = iota // normal: every call runs
	CircuitOpen                         // failing: calls fail fast with ErrCircuitOpen
	CircuitHalfOpen                     // one trial call is in flight
)

// String returns "closed", "open" or "half-open".
func (s CircuitState) String() string {
	// TODO Ch07.
	return ""
}

// ErrCircuitOpen is what a breaker returns while it is open (and to everyone except the
// single trial call while it is half-open). Do never calls fn in that case.
var ErrCircuitOpen = errors.New("circuit breaker is open")

// CircuitBreaker stops calling a backend that keeps failing: after failureThreshold
// CONSECUTIVE failures it "opens" and rejects calls for openDuration, then lets exactly
// one trial call through to test the water.
//
// A zero-value CircuitBreaker is not usable; construct one with NewCircuitBreaker.
// The breaker must be safe for concurrent use: several goroutines call Do at once.
type CircuitBreaker struct {
	mu                  sync.Mutex
	state               CircuitState
	consecutiveFailures int
	openedAt            time.Time
	failureThreshold    int
	openDuration        time.Duration
	clock               Clock
	trialInFlight       bool
}

// NewCircuitBreaker returns a closed breaker that opens after failureThreshold
// consecutive failures and stays open for openDuration.
//
// failureThreshold < 1 is treated as 1. openDuration <= 0 becomes the documented
// default of 30s. A nil clock means RealClock{} (handy in tests that don't move time).
func NewCircuitBreaker(failureThreshold int, openDuration time.Duration, clock Clock) *CircuitBreaker {
	// TODO Ch07: clamp thresholds, pick the clock, return a closed breaker.
	return &CircuitBreaker{}
}

// Do runs fn — unless the breaker is open, in which case fn is NOT called and Do returns
// ErrCircuitOpen immediately. The three states:
//
//   - closed:    fn runs; an error counts as a consecutive failure and at
//     failureThreshold the breaker opens (openedAt = clock.Now()); any success
//     resets the counter to zero.
//   - open:      if openDuration has NOT elapsed since openedAt → ErrCircuitOpen.
//     If it has → the breaker moves to half-open and THIS call becomes the trial.
//   - half-open: exactly one trial may run; every other concurrent call gets
//     ErrCircuitOpen. A successful trial closes the breaker and resets the
//     counter; a failed trial reopens it and restarts the cooldown.
//
// fn runs OUTSIDE the lock: it is the slow part, and other callers must be able to
// observe the state and fail fast while it runs.
func (c *CircuitBreaker) Do(fn func() error) error {
	// TODO Ch07: lock → inspect/transition state → unlock → fn → lock → record outcome.
	return todo.NotImplemented("Ch07: CircuitBreaker.Do")
}

// State returns the current state (closed, open or half-open).
//
// Transitions happen in Do — including open→half-open once the cooldown has elapsed.
// Merely reading the state does not move the breaker.
func (c *CircuitBreaker) State() CircuitState {
	// TODO Ch07.
	return c.state
}

// Reset forces the breaker back to closed and clears the failure counter and cooldown,
// as if it had never failed. Used by tests and by operations ("I fixed the backend").
func (c *CircuitBreaker) Reset() {
	// TODO Ch07.
}
