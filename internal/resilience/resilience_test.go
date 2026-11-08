package resilience_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"netlab/internal/resilience"
	"netlab/internal/testutil"
)

// Chapter 07 — client-side resilience: retries with backoff + jitter, and a circuit breaker.
// Run:  go test ./internal/resilience -run Ch07 -race -v
//
// Determinism rules for this package:
//   - every sleep goes through testutil.RecordingSleeper: no test ever waits on a timer;
//   - breaker time goes through testutil.NewFakeClock(): the test moves time by hand;
//   - randomness is injected from testutil.Rand(seed): same seed, same delays, every run.

// policy builds a jitter-free policy wired to a recording sleeper.
func policy(sleeper *testutil.RecordingSleeper, maxAttempts int, base, max time.Duration) resilience.RetryPolicy {
	return resilience.RetryPolicy{
		MaxAttempts: maxAttempts,
		BaseDelay:   base,
		MaxDelay:    max,
		Jitter:      0,
		Sleep:       sleeper.Sleep,
	}
}

func transient(attempt int) error {
	return fmt.Errorf("attempt %d: connection reset by peer", attempt)
}

// ---- retries -------------------------------------------------------------------------

func TestCh07_Retry_SucceedsOnThirdAttempt(t *testing.T) {
	testutil.NoLeaks(t)
	sleeper := &testutil.RecordingSleeper{}
	p := policy(sleeper, 5, 100*time.Millisecond, 2*time.Second)

	var attempts []int
	err := p.Do(context.Background(), func(attempt int) error {
		attempts = append(attempts, attempt)
		if attempt < 3 {
			return transient(attempt)
		}
		return nil
	})

	if err != nil {
		t.Fatalf("Do = %v, want nil after the third attempt", err)
	}
	if want := []int{1, 2, 3}; !slices.Equal(attempts, want) {
		t.Fatalf("fn called with attempts %v, want %v", attempts, want)
	}
	if want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond}; !slices.Equal(sleeper.Delays(), want) {
		t.Fatalf("recorded sleeps %v, want %v (one sleep between attempts, none after success)", sleeper.Delays(), want)
	}
}

func TestCh07_Retry_PermanentErrorIsNotRetried(t *testing.T) {
	testutil.NoLeaks(t)
	sleeper := &testutil.RecordingSleeper{}
	p := policy(sleeper, 5, 10*time.Millisecond, time.Second)

	sentinel := errors.New("400 Bad Request")
	if got := resilience.Permanent(nil); got != nil {
		t.Fatalf("Permanent(nil) = %v, want nil", got)
	}
	if !resilience.IsPermanent(resilience.Permanent(sentinel)) {
		t.Fatal("IsPermanent(Permanent(err)) = false, want true")
	}
	if resilience.IsPermanent(sentinel) {
		t.Fatal("IsPermanent(plain error) = true, want false")
	}
	if !resilience.IsPermanent(fmt.Errorf("login failed: %w", resilience.Permanent(sentinel))) {
		t.Fatal("IsPermanent must see through fmt.Errorf %w wrapping (errors.As)")
	}

	calls := 0
	err := p.Do(context.Background(), func(attempt int) error {
		calls++
		return resilience.Permanent(fmt.Errorf("do not retry: %w", sentinel))
	})

	if calls != 1 {
		t.Fatalf("fn called %d times, want exactly 1: a permanent error is never retried", calls)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("Do = %v, want an error wrapping the original cause %v", err, sentinel)
	}
	if delays := sleeper.Delays(); len(delays) != 0 {
		t.Fatalf("slept %v before giving up on a permanent error, want no sleep", delays)
	}
}

func TestCh07_Retry_ExhaustsAttempts_ReturnsTheLastError(t *testing.T) {
	testutil.NoLeaks(t)
	sleeper := &testutil.RecordingSleeper{}
	p := policy(sleeper, 3, 10*time.Millisecond, time.Second)

	errs := []error{errors.New("failure 1"), errors.New("failure 2"), errors.New("failure 3")}
	calls := 0
	err := p.Do(context.Background(), func(attempt int) error {
		calls++
		return errs[attempt-1]
	})

	if calls != 3 {
		t.Fatalf("fn called %d times, want exactly 3 (MaxAttempts = 3)", calls)
	}
	if !errors.Is(err, errs[2]) {
		t.Fatalf("Do = %v, want the LAST error %v", err, errs[2])
	}
	if errors.Is(err, errs[0]) {
		t.Fatalf("Do = %v, still wraps the FIRST error: Do must report the last failure", err)
	}
	if want := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond}; !slices.Equal(sleeper.Delays(), want) {
		t.Fatalf("recorded sleeps %v, want %v (no sleep after the final attempt)", sleeper.Delays(), want)
	}
}

func TestCh07_Retry_MaxAttemptsBelowOne_TriesOnce(t *testing.T) {
	testutil.NoLeaks(t)
	for _, maxAttempts := range []int{0, -7} {
		sleeper := &testutil.RecordingSleeper{}
		p := policy(sleeper, maxAttempts, 10*time.Millisecond, time.Second)
		sentinel := fmt.Errorf("still failing (MaxAttempts=%d)", maxAttempts)

		calls := 0
		err := p.Do(context.Background(), func(attempt int) error {
			calls++
			if attempt != 1 {
				t.Fatalf("MaxAttempts=%d: attempt = %d, want 1", maxAttempts, attempt)
			}
			return sentinel
		})

		if calls != 1 {
			t.Fatalf("MaxAttempts=%d: fn called %d times, want exactly 1", maxAttempts, calls)
		}
		if !errors.Is(err, sentinel) {
			t.Fatalf("MaxAttempts=%d: Do = %v, want %v", maxAttempts, err, sentinel)
		}
		if delays := sleeper.Delays(); len(delays) != 0 {
			t.Fatalf("MaxAttempts=%d: slept %v, want no retries", maxAttempts, delays)
		}
	}
}

func TestCh07_Retry_ContextCancelledBeforeFirstAttempt(t *testing.T) {
	testutil.NoLeaks(t)
	sleeper := &testutil.RecordingSleeper{}
	p := policy(sleeper, 5, 10*time.Millisecond, time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	calls := 0
	err := p.Do(ctx, func(attempt int) error {
		calls++
		return nil
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Do = %v, want context.Canceled", err)
	}
	if calls != 0 {
		t.Fatalf("fn called %d times with an already-cancelled context, want 0", calls)
	}
	if delays := sleeper.Delays(); len(delays) != 0 {
		t.Fatalf("slept %v, want no sleep when the context is already done", delays)
	}
}

func TestCh07_Retry_ContextCancelledDuringDo(t *testing.T) {
	testutil.NoLeaks(t)
	sleeper := &testutil.RecordingSleeper{}
	p := policy(sleeper, 5, 10*time.Millisecond, time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := p.Do(ctx, func(attempt int) error {
		calls++
		cancel() // the caller gives up while we are in the middle of the retry loop
		return transient(attempt)
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Do = %v, want context.Canceled: never retry past the deadline", err)
	}
	if calls != 1 {
		t.Fatalf("fn called %d times, want 1: no second attempt after cancellation", calls)
	}
}

func TestCh07_Retry_Jitter_NeverExceedsTheBackoff(t *testing.T) {
	testutil.NoLeaks(t)
	const base = 500 * time.Millisecond
	sleeper := &testutil.RecordingSleeper{}
	p := resilience.RetryPolicy{
		MaxAttempts: 2, // exactly one sleep per Do, so 50 Do calls record 50 delays
		BaseDelay:   base,
		MaxDelay:    time.Minute,
		Jitter:      1, // full jitter
		Rand:        testutil.Rand(7),
		Sleep:       sleeper.Sleep,
	}

	ctx := context.Background()
	for i := 0; i < 50; i++ {
		err := p.Do(ctx, func(attempt int) error { return transient(attempt) })
		testutil.ExpectError(t, err)
	}

	delays := sleeper.Delays()
	if len(delays) != 50 {
		t.Fatalf("recorded %d delays, want 50 (one per Do)", len(delays))
	}
	shrunk := false
	for i, d := range delays {
		if d < 0 || d > base {
			t.Fatalf("delay %d = %v, want 0 <= delay <= %v: jitter only SHRINKS a delay", i, d, base)
		}
		if d != base {
			shrunk = true
		}
	}
	if !shrunk {
		t.Fatal("50 full-jitter draws produced BaseDelay every time: the random source is not being used")
	}
}

func TestCh07_Retry_BackoffIsCappedByMaxDelay(t *testing.T) {
	testutil.NoLeaks(t)
	sleeper := &testutil.RecordingSleeper{}
	p := policy(sleeper, 5, 100*time.Millisecond, 250*time.Millisecond)

	calls := 0
	err := p.Do(context.Background(), func(attempt int) error {
		calls++
		return transient(attempt)
	})
	testutil.ExpectError(t, err)

	if calls != 5 {
		t.Fatalf("fn called %d times, want 5", calls)
	}
	want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 250 * time.Millisecond, 250 * time.Millisecond}
	if got := sleeper.Delays(); !slices.Equal(got, want) {
		t.Fatalf("recorded sleeps %v, want %v (double, then stay at the cap)", got, want)
	}
}

func TestCh07_Retry_NewRetryPolicyDefaults(t *testing.T) {
	testutil.NoLeaks(t)
	p := resilience.NewRetryPolicy()
	if p.MaxAttempts != 5 {
		t.Fatalf("NewRetryPolicy().MaxAttempts = %d, want 5", p.MaxAttempts)
	}
	if p.BaseDelay != 100*time.Millisecond {
		t.Fatalf("NewRetryPolicy().BaseDelay = %v, want 100ms", p.BaseDelay)
	}
	if p.MaxDelay != 2*time.Second {
		t.Fatalf("NewRetryPolicy().MaxDelay = %v, want 2s", p.MaxDelay)
	}
	if p.Jitter != 0.2 {
		t.Fatalf("NewRetryPolicy().Jitter = %v, want 0.2", p.Jitter)
	}
	if p.Rand != nil {
		t.Fatalf("NewRetryPolicy().Rand = %v, want nil (the deterministic default source)", p.Rand)
	}
	if p.Sleep != nil {
		t.Fatal("NewRetryPolicy().Sleep != nil, want nil (a real timer in production)")
	}

	// The defaults must work as-is: nil Rand means the documented deterministic source,
	// so the one recorded delay is a jittered 100ms in [80ms, 100ms].
	sleeper := &testutil.RecordingSleeper{}
	p.Sleep = sleeper.Sleep
	calls := 0
	err := p.Do(context.Background(), func(attempt int) error {
		calls++
		if calls == 1 {
			return errors.New("cold start")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Do with defaults = %v, want nil", err)
	}
	if calls != 2 {
		t.Fatalf("fn called %d times, want 2", calls)
	}
	delays := sleeper.Delays()
	if len(delays) != 1 {
		t.Fatalf("recorded %d delays, want 1", len(delays))
	}
	if d := delays[0]; d < 80*time.Millisecond || d > 100*time.Millisecond {
		t.Fatalf("default jitter produced %v, want a delay in [80ms, 100ms] (20%% jitter on 100ms)", d)
	}
}

func TestCh07_RealClock_NowIsCurrentTime(t *testing.T) {
	testutil.NoLeaks(t)
	before := time.Now()
	now := resilience.RealClock{}.Now()
	after := time.Now()
	if now.Before(before) || now.After(after) {
		t.Fatalf("RealClock.Now() = %v, want a time within [%v, %v]", now, before, after)
	}

	// A nil clock must fall back to real time instead of panicking.
	b := resilience.NewCircuitBreaker(1, time.Hour, nil)
	if err := b.Do(func() error { return errors.New("down") }); err == nil {
		t.Fatal("Do = nil, want the function's error")
	}
	if got := b.State(); got != resilience.CircuitOpen {
		t.Fatalf("State() = %v, want open", got)
	}
	if err := b.Do(func() error { return nil }); !errors.Is(err, resilience.ErrCircuitOpen) {
		t.Fatalf("Do = %v, want ErrCircuitOpen", err)
	}
}

// ---- circuit breaker -----------------------------------------------------------------

func TestCh07_Breaker_OpensAfterConsecutiveFailures_ThenRejectsWithoutCallingFn(t *testing.T) {
	testutil.NoLeaks(t)
	clock := testutil.NewFakeClock()
	b := resilience.NewCircuitBreaker(3, time.Minute, clock)

	calls := 0
	ok := func() error { calls++; return nil }
	fail := func() error { calls++; return errors.New("backend down") }

	for i := 0; i < 2; i++ {
		if err := b.Do(fail); err == nil {
			t.Fatalf("failure %d: Do = nil, want the failure", i+1)
		}
	}
	if got := b.State(); got != resilience.CircuitClosed {
		t.Fatalf("after 2 failures of threshold 3: State() = %v, want closed", got)
	}

	// A success must reset the consecutive-failure run...
	if err := b.Do(ok); err != nil {
		t.Fatalf("Do = %v, want nil", err)
	}
	for i := 0; i < 2; i++ {
		if err := b.Do(fail); err == nil {
			t.Fatalf("failure %d after the reset: Do = nil", i+1)
		}
	}
	if got := b.State(); got != resilience.CircuitClosed {
		t.Fatalf("State() = %v after a success reset, want closed (the run must start over)", got)
	}

	// ...so only the third CONSECUTIVE failure opens the breaker.
	if err := b.Do(fail); err == nil {
		t.Fatal("third consecutive failure: Do = nil")
	}
	if got := b.State(); got != resilience.CircuitOpen {
		t.Fatalf("State() = %v after threshold failures, want open", got)
	}

	// While open, Do returns ErrCircuitOpen WITHOUT calling fn.
	before := calls
	err := b.Do(ok)
	if !errors.Is(err, resilience.ErrCircuitOpen) {
		t.Fatalf("Do while open = %v, want ErrCircuitOpen", err)
	}
	if calls != before {
		t.Fatalf("fn was called %d time(s) while the breaker was open, want 0", calls-before)
	}
}

func TestCh07_Breaker_AfterCooldown_AllowsOneTrial_ClosesOnSuccess(t *testing.T) {
	testutil.NoLeaks(t)
	clock := testutil.NewFakeClock()
	b := resilience.NewCircuitBreaker(2, 30*time.Second, clock)
	fail := func() error { return errors.New("backend down") }

	for i := 0; i < 2; i++ {
		if err := b.Do(fail); err == nil {
			t.Fatalf("failure %d: Do = nil", i+1)
		}
	}
	if got := b.State(); got != resilience.CircuitOpen {
		t.Fatalf("State() = %v, want open", got)
	}

	// Still inside the cooldown: rejected, fn not called.
	clock.Advance(29 * time.Second)
	if err := b.Do(fail); !errors.Is(err, resilience.ErrCircuitOpen) {
		t.Fatalf("Do inside the cooldown = %v, want ErrCircuitOpen", err)
	}

	// Cooldown over: the next call becomes the half-open trial and is allowed to run.
	clock.Advance(time.Second)
	started := make(chan struct{})
	release := make(chan struct{})
	trial := make(chan error, 1)
	go func() {
		trial <- b.Do(func() error {
			close(started)
			<-release
			return nil
		})
	}()
	testutil.WaitClosed(t, started, testutil.Timeout)

	if got := b.State(); got != resilience.CircuitHalfOpen {
		t.Fatalf("State() while the trial is in flight = %v, want half-open", got)
	}

	// A second caller while the trial is in flight must NOT get a second trial.
	probeCalls := 0
	if err := b.Do(func() error { probeCalls++; return nil }); !errors.Is(err, resilience.ErrCircuitOpen) {
		t.Fatalf("second call during the trial = %v, want ErrCircuitOpen", err)
	}
	if probeCalls != 0 {
		t.Fatalf("the probe function ran %d time(s) during the trial, want 0", probeCalls)
	}

	close(release)
	if err := testutil.Receive(t, trial, testutil.Timeout); err != nil {
		t.Fatalf("trial Do = %v, want nil", err)
	}
	if got := b.State(); got != resilience.CircuitClosed {
		t.Fatalf("State() after a successful trial = %v, want closed", got)
	}
	if err := b.Do(func() error { return nil }); err != nil {
		t.Fatalf("Do after closing = %v, want nil", err)
	}
}

func TestCh07_Breaker_TrialFailure_ReopensAndRestartsTheCooldown(t *testing.T) {
	testutil.NoLeaks(t)
	clock := testutil.NewFakeClock()
	b := resilience.NewCircuitBreaker(1, 10*time.Second, clock)
	fail := func() error { return errors.New("backend still down") }

	if err := b.Do(fail); err == nil {
		t.Fatal("Do = nil, want the failure")
	}
	if got := b.State(); got != resilience.CircuitOpen {
		t.Fatalf("State() = %v, want open", got)
	}

	clock.Advance(10 * time.Second)
	err := b.Do(fail) // becomes the trial and fails
	if err == nil {
		t.Fatal("failing trial: Do = nil")
	}
	if errors.Is(err, resilience.ErrCircuitOpen) {
		t.Fatal("the trial must actually run fn, not return ErrCircuitOpen")
	}
	if got := b.State(); got != resilience.CircuitOpen {
		t.Fatalf("State() after a failed trial = %v, want open again", got)
	}

	// The cooldown restarted when the trial failed: 9s is not enough...
	clock.Advance(9 * time.Second)
	if err := b.Do(fail); !errors.Is(err, resilience.ErrCircuitOpen) {
		t.Fatalf("Do 9s after the failed trial = %v, want ErrCircuitOpen", err)
	}
	// ...10s is.
	clock.Advance(time.Second)
	if err := b.Do(func() error { return nil }); err != nil {
		t.Fatalf("successful trial: Do = %v, want nil", err)
	}
	if got := b.State(); got != resilience.CircuitClosed {
		t.Fatalf("State() = %v, want closed", got)
	}
}

func TestCh07_Breaker_ConcurrentCallsWhileOpen_AllRejected(t *testing.T) {
	testutil.NoLeaks(t)
	clock := testutil.NewFakeClock()
	b := resilience.NewCircuitBreaker(1, time.Minute, clock)

	if err := b.Do(func() error { return errors.New("down") }); err == nil {
		t.Fatal("Do = nil, want the failure")
	}
	if got := b.State(); got != resilience.CircuitOpen {
		t.Fatalf("State() = %v, want open", got)
	}

	var calls atomic.Int64
	const goroutines = 16
	errs := make([]error, goroutines)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = b.Do(func() error { calls.Add(1); return nil })
		}(i)
	}
	wg.Wait()

	if n := calls.Load(); n != 0 {
		t.Fatalf("fn was called %d time(s) while the breaker was open, want 0", n)
	}
	for i, err := range errs {
		if !errors.Is(err, resilience.ErrCircuitOpen) {
			t.Fatalf("goroutine %d: Do = %v, want ErrCircuitOpen", i, err)
		}
	}
	if got := b.State(); got != resilience.CircuitOpen {
		t.Fatalf("State() = %v after rejected calls, want open", got)
	}
}

func TestCh07_Breaker_Reset_ClosesAndClearsCounters(t *testing.T) {
	testutil.NoLeaks(t)
	clock := testutil.NewFakeClock()
	b := resilience.NewCircuitBreaker(2, time.Minute, clock)
	fail := func() error { return errors.New("backend down") }

	for i := 0; i < 2; i++ {
		if err := b.Do(fail); err == nil {
			t.Fatalf("failure %d: Do = nil", i+1)
		}
	}
	if got := b.State(); got != resilience.CircuitOpen {
		t.Fatalf("State() = %v, want open", got)
	}
	if err := b.Do(fail); !errors.Is(err, resilience.ErrCircuitOpen) {
		t.Fatalf("Do while open = %v, want ErrCircuitOpen", err)
	}

	b.Reset()
	if got := b.State(); got != resilience.CircuitClosed {
		t.Fatalf("State() after Reset = %v, want closed", got)
	}

	// No cooldown wait is needed, and the failure counter starts from zero: one failure
	// (threshold is 2) must leave the breaker closed.
	calls := 0
	if err := b.Do(func() error { calls++; return nil }); err != nil {
		t.Fatalf("Do immediately after Reset = %v, want nil", err)
	}
	if calls != 1 {
		t.Fatalf("fn called %d time(s), want 1", calls)
	}
	if err := b.Do(fail); err == nil {
		t.Fatal("Do = nil, want the failure")
	}
	if got := b.State(); got != resilience.CircuitClosed {
		t.Fatalf("State() = %v after one post-Reset failure, want closed (counters were not cleared?)", got)
	}
	if err := b.Do(fail); err == nil {
		t.Fatal("Do = nil, want the failure")
	}
	if got := b.State(); got != resilience.CircuitOpen {
		t.Fatalf("State() = %v after two post-Reset failures, want open", got)
	}
}

func TestCh07_Breaker_StateString(t *testing.T) {
	testutil.NoLeaks(t)
	cases := []struct {
		state resilience.CircuitState
		want  string
	}{
		{resilience.CircuitClosed, "closed"},
		{resilience.CircuitOpen, "open"},
		{resilience.CircuitHalfOpen, "half-open"},
	}
	for _, tc := range cases {
		if got := tc.state.String(); got != tc.want {
			t.Fatalf("CircuitState(%d).String() = %q, want %q", int(tc.state), got, tc.want)
		}
	}

	// String must agree with a live breaker as it moves through the states.
	clock := testutil.NewFakeClock()
	b := resilience.NewCircuitBreaker(1, 10*time.Second, clock)
	if got := b.State().String(); got != "closed" {
		t.Fatalf("new breaker: State() = %q, want %q", got, "closed")
	}
	_ = b.Do(func() error { return errors.New("down") })
	if got := b.State().String(); got != "open" {
		t.Fatalf("after a failure: State() = %q, want %q", got, "open")
	}
	clock.Advance(10 * time.Second)
	if got := b.State(); got != resilience.CircuitOpen {
		t.Fatalf("State() = %v before the trial call, want open (half-open is entered by Do, not by reading the clock)", got)
	}
}

func TestCh07_Breaker_ZeroArgumentsUseDocumentedDefaults(t *testing.T) {
	testutil.NoLeaks(t)
	clock := testutil.NewFakeClock()
	b := resilience.NewCircuitBreaker(0, 0, clock) // threshold < 1 → 1; openDuration <= 0 → 30s

	if err := b.Do(func() error { return errors.New("down") }); err == nil {
		t.Fatal("Do = nil, want the failure")
	}
	if got := b.State(); got != resilience.CircuitOpen {
		t.Fatalf("State() = %v with failureThreshold 0, want open after ONE failure", got)
	}

	clock.Advance(29 * time.Second)
	calls := 0
	if err := b.Do(func() error { calls++; return nil }); !errors.Is(err, resilience.ErrCircuitOpen) {
		t.Fatalf("Do 29s into the default cooldown = %v, want ErrCircuitOpen (default openDuration is 30s)", err)
	}
	if calls != 0 {
		t.Fatalf("fn called %d time(s) during the default cooldown, want 0", calls)
	}

	clock.Advance(time.Second) // 30s total: the trial is allowed
	if err := b.Do(func() error { calls++; return nil }); err != nil {
		t.Fatalf("Do after the default 30s cooldown = %v, want nil", err)
	}
	if calls != 1 {
		t.Fatalf("fn called %d time(s), want 1", calls)
	}
	if got := b.State(); got != resilience.CircuitClosed {
		t.Fatalf("State() = %v after a successful trial, want closed", got)
	}
}
