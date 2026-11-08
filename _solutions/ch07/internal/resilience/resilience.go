// Package resilience implements the client-side survival kit of every RPC client:
// retries with exponential backoff and jitter, a permanent-vs-transient error
// distinction, and a circuit breaker that stops hammering a backend that is down.
//
// See docs/chapters/ch07-client-resilience.md for the guided lesson.
//
// Reference implementation — chapter 07 solution.
package resilience

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"
)

// Clock abstracts "now" so the circuit breaker can be tested without sleeping.
type Clock interface {
	Now() time.Time
}

// RealClock is the Clock you use in production.
type RealClock struct{}

// Now returns the current local time.
func (RealClock) Now() time.Time { return time.Now() }

// permanentError marks an error as non-retryable. It unwraps to the original error,
// so errors.Is/errors.As keep working through it.
type permanentError struct{ err error }

func (e *permanentError) Error() string { return "permanent: " + e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// Permanent wraps err to mark it as non-retryable: repeating the exact same request
// cannot succeed (a 4xx, an authentication failure, a malformed payload).
// Permanent(nil) returns nil and marking an already-permanent error is a no-op.
func Permanent(err error) error {
	if err == nil || IsPermanent(err) {
		return err
	}
	return &permanentError{err: err}
}

// IsPermanent reports whether err, or anything it wraps, was marked by Permanent.
func IsPermanent(err error) bool {
	var pe *permanentError
	return errors.As(err, &pe)
}

// RetryPolicy describes how RetryPolicy.Do retries a failing operation.
// The zero value is not usable: start from NewRetryPolicy and adjust.
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
	// delay = delay * (1 - Jitter*rand.Float64()). Values outside [0, 1] are clamped.
	Jitter float64

	// Rand is the source of jitter randomness. nil means "use a fresh, deterministically
	// seeded source" (rand.New(rand.NewSource(1))), documented so examples and tests
	// replay identically. Production should inject a genuinely random source.
	// Not safe for concurrent use; Do is not either.
	Rand *rand.Rand

	// Sleep waits for d and returns nil, or returns ctx.Err() if ctx is done first.
	// nil means "use a real timer". Tests inject (*testutil.RecordingSleeper).Sleep.
	Sleep func(ctx context.Context, d time.Duration) error
}

// NewRetryPolicy returns a policy with production-sane defaults:
// MaxAttempts 5, BaseDelay 100ms, MaxDelay 2s, Jitter 0.2, nil Rand and Sleep.
func NewRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts: 5,
		BaseDelay:   100 * time.Millisecond,
		MaxDelay:    2 * time.Second,
		Jitter:      0.2,
	}
}

// Do calls fn(attempt) with attempt starting at 1, and keeps trying according to r:
//
//   - fn returns nil           → nil;
//   - IsPermanent(err)         → that error immediately (no sleep, no retry);
//   - any other error          → sleep BaseDelay*2^(attempt-1) (capped by MaxDelay,
//     shrunk by Jitter) and retry;
//   - after MaxAttempts        → the LAST error;
//   - ctx done before an attempt or during a sleep → ctx.Err().
//
// No sleep before the first attempt and none after the last.
func (r RetryPolicy) Do(ctx context.Context, fn func(attempt int) error) error {
	attempts := r.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}
	rng := r.Rand
	if rng == nil {
		rng = rand.New(rand.NewSource(1))
	}
	sleep := r.Sleep
	if sleep == nil {
		sleep = realSleep
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := fn(attempt)
		if err == nil {
			return nil
		}
		if IsPermanent(err) {
			return err
		}
		lastErr = err
		if attempt == attempts {
			break
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		delay := backoffDelay(r.BaseDelay, r.MaxDelay, attempt, r.Jitter, rng)
		if err := sleep(ctx, delay); err != nil {
			return err
		}
	}
	return lastErr
}

// realSleep is the production Sleep: it honours ctx.Done() and stops the timer.
func realSleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// backoffDelay computes the sleep after failedAttempt failed: base doubled
// (failedAttempt-1) times, capped at max (when max > 0), then shrunk by jitter.
func backoffDelay(base, max time.Duration, failedAttempt int, jitter float64, rng *rand.Rand) time.Duration {
	if base < 0 {
		base = 0
	}
	d := base
	for i := 1; i < failedAttempt && (max <= 0 || d < max); i++ {
		if d > time.Duration(1)<<62 {
			d = time.Duration(1) << 62 // ~146 years: stay far away from int64 overflow
			break
		}
		d *= 2
	}
	if max > 0 && d > max {
		d = max
	}
	if jitter > 0 {
		if jitter > 1 {
			jitter = 1
		}
		d = time.Duration(float64(d) * (1 - jitter*rng.Float64()))
	}
	return d
}

// CircuitState is the breaker's state. The state machine is:
//
//	closed → (failureThreshold consecutive failures) → open
//	open   → (openDuration elapsed, noticed by the next Do) → half-open (one trial)
//	half-open → success → closed | failure → open (the cooldown restarts)
type CircuitState int

const (
	CircuitClosed   CircuitState = iota // normal: every call runs
	CircuitOpen                         // failing: calls fail fast with ErrCircuitOpen
	CircuitHalfOpen                     // one trial call is in flight
)

// String returns "closed", "open" or "half-open".
func (s CircuitState) String() string {
	switch s {
	case CircuitClosed:
		return "closed"
	case CircuitOpen:
		return "open"
	case CircuitHalfOpen:
		return "half-open"
	default:
		return fmt.Sprintf("CircuitState(%d)", int(s))
	}
}

// ErrCircuitOpen is what a breaker returns while it is open (and to everyone except the
// single trial call while it is half-open). Do never calls fn in that case.
var ErrCircuitOpen = errors.New("circuit breaker is open")

// CircuitBreaker stops calling a backend that keeps failing: after failureThreshold
// consecutive failures it opens and rejects calls for openDuration, then lets exactly
// one trial call through to test the water. Safe for concurrent use.
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
// consecutive failures and stays open for openDuration. failureThreshold < 1 is
// treated as 1; openDuration <= 0 becomes 30s; a nil clock means RealClock{}.
func NewCircuitBreaker(failureThreshold int, openDuration time.Duration, clock Clock) *CircuitBreaker {
	if failureThreshold < 1 {
		failureThreshold = 1
	}
	if openDuration <= 0 {
		openDuration = 30 * time.Second
	}
	if clock == nil {
		clock = RealClock{}
	}
	return &CircuitBreaker{
		failureThreshold: failureThreshold,
		openDuration:     openDuration,
		clock:            clock,
	}
}

// Do runs fn unless the breaker is open, in which case fn is NOT called and Do
// returns ErrCircuitOpen. fn runs outside the lock: the slow part must not block
// callers that are only checking the state.
func (c *CircuitBreaker) Do(fn func() error) error {
	c.mu.Lock()
	switch {
	case c.state == CircuitOpen:
		if c.clock.Now().Sub(c.openedAt) >= c.openDuration {
			c.state = CircuitHalfOpen // this call becomes the single trial
			c.trialInFlight = true
		} else {
			c.mu.Unlock()
			return ErrCircuitOpen
		}
	case c.state == CircuitHalfOpen:
		c.mu.Unlock() // a trial is already in flight
		return ErrCircuitOpen
	}
	c.mu.Unlock()

	err := fn()

	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.consecutiveFailures++
		if c.state == CircuitHalfOpen || c.consecutiveFailures >= c.failureThreshold {
			c.state = CircuitOpen
			c.openedAt = c.clock.Now()
		}
		c.trialInFlight = false
		return err
	}
	c.state = CircuitClosed
	c.consecutiveFailures = 0
	c.trialInFlight = false
	return nil
}

// State returns the stored state. Transitions happen in Do — including open→half-open
// when the cooldown has elapsed. Merely reading the state does not move the breaker.
func (c *CircuitBreaker) State() CircuitState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Reset forces the breaker back to closed and clears the failure counter, cooldown
// timer and any trial flag, as if it had never failed.
func (c *CircuitBreaker) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state = CircuitClosed
	c.consecutiveFailures = 0
	c.openedAt = time.Time{}
	c.trialInFlight = false
}
