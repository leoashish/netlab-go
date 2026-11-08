package limit_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"netlab/internal/limit"
	"netlab/internal/testutil"
)

// Chapter 06 — rate limiting: token buckets, leaky buckets, window counters.
// Run:  go test ./internal/limit -run Ch06 -race -v

// advancingSleeper records every slept delay (via RecordingSleeper) and moves the fake
// clock forward by the same amount — which is exactly what a real sleep does to a real
// clock. Without it, a fake clock and Wait would never see time pass.
type advancingSleeper struct {
	rec   *testutil.RecordingSleeper
	clock *testutil.FakeClock
}

func (s advancingSleeper) Sleep(ctx context.Context, d time.Duration) error {
	if err := s.rec.Sleep(ctx, d); err != nil {
		return err
	}
	s.clock.Advance(d)
	return nil
}

func TestCh06_TokenBucket_StartsFull_AndDrainsExactlyCapacity(t *testing.T) {
	clock := testutil.NewFakeClock()
	b := limit.NewTokenBucket(3, 0.5, clock) // half a token per second: no refill inside this test

	for i := 1; i <= 3; i++ {
		if !b.Allow() {
			t.Fatalf("Allow #%d on a full bucket should succeed", i)
		}
	}
	if b.Allow() {
		t.Fatal("the fourth Allow must fail: a fresh bucket holds exactly its capacity")
	}
}

func TestCh06_TokenBucket_AllowN_PartialFailureConsumesNothing(t *testing.T) {
	clock := testutil.NewFakeClock()
	b := limit.NewTokenBucket(10, 0, clock)

	if !b.AllowN(4) {
		t.Fatal("first AllowN(4) should succeed")
	}
	if b.AllowN(7) {
		t.Fatal("AllowN(7) with only 6 tokens left must fail")
	}
	if !b.AllowN(6) {
		t.Fatal("the failed AllowN(7) must not have consumed anything: 6 tokens remain")
	}
	if b.Allow() {
		t.Fatal("the bucket is empty after spending 4 + 6")
	}
}

func TestCh06_TokenBucket_AllowN_ZeroIsFree_AndOversizedIsRefused(t *testing.T) {
	clock := testutil.NewFakeClock()
	b := limit.NewTokenBucket(2, 0, clock)

	if !b.AllowN(0) {
		t.Fatal("AllowN(0) is free and must succeed")
	}
	if !b.AllowN(-3) {
		t.Fatal("AllowN with a negative n is free and must succeed")
	}
	if b.AllowN(3) {
		t.Fatal("AllowN(3) can never be satisfied by a capacity-2 bucket")
	}
	if !b.AllowN(2) {
		t.Fatal("the refused AllowN(3) must not have consumed anything")
	}
	if b.Allow() {
		t.Fatal("AllowN(2) consumed the whole bucket")
	}
}

func TestCh06_TokenBucket_RefillsContinuously_WithTheClock(t *testing.T) {
	clock := testutil.NewFakeClock()
	b := limit.NewTokenBucket(10, 10, clock) // 10 tokens per second

	if !b.AllowN(10) {
		t.Fatal("drain the full bucket first")
	}
	if b.Allow() {
		t.Fatal("same instant: no time has passed, nothing was refilled")
	}

	clock.Advance(500 * time.Millisecond) // +5 tokens (fractional time deltas work)
	if !b.AllowN(5) {
		t.Fatal("500ms at 10 tokens/s should have refilled 5 tokens")
	}
	if b.Allow() {
		t.Fatal("only 5 tokens were refilled, not 6")
	}

	clock.Advance(time.Hour) // plenty of time — but the bucket caps at capacity
	if !b.AllowN(10) {
		t.Fatal("refill is capped at capacity: after an idle hour the bucket holds exactly 10, not more")
	}
	if b.Allow() {
		t.Fatal("all 10 refilled tokens were spent by AllowN(10)")
	}
}

func TestCh06_TokenBucket_Wait_SleepsExactlyTheDeficit(t *testing.T) {
	clock := testutil.NewFakeClock()
	rec := &testutil.RecordingSleeper{}
	b := limit.NewTokenBucketWithSleeper(10, 10, clock, advancingSleeper{rec: rec, clock: clock}.Sleep)

	if !b.AllowN(10) {
		t.Fatal("drain the bucket")
	}
	if err := b.Wait(context.Background(), 1); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	delays := rec.Delays()
	if len(delays) != 1 {
		t.Fatalf("sleeper called %d times (%v), want exactly 1", len(delays), delays)
	}
	if got := delays[0]; got < 99*time.Millisecond || got > 101*time.Millisecond {
		t.Fatalf("slept %v, want ~100ms (1 token missing at 10 tokens/s)", got)
	}
	if b.Allow() {
		t.Fatal("Wait spent the refilled token; the bucket must be empty again")
	}
}

func TestCh06_TokenBucket_Wait_OverCapacity_ReturnsErrExceedsCapacity(t *testing.T) {
	clock := testutil.NewFakeClock()
	rec := &testutil.RecordingSleeper{}
	b := limit.NewTokenBucketWithSleeper(2, 10, clock, rec.Sleep)

	err := b.Wait(context.Background(), 3)
	if !errors.Is(err, limit.ErrExceedsCapacity) {
		t.Fatalf("err = %v, want ErrExceedsCapacity", err)
	}
	if d := rec.Delays(); len(d) != 0 {
		t.Fatalf("over-capacity Wait must fail before sleeping; sleeper saw %v", d)
	}
}

func TestCh06_TokenBucket_Wait_ZeroRefill_ReturnsErrZeroRefill(t *testing.T) {
	clock := testutil.NewFakeClock()
	rec := &testutil.RecordingSleeper{}
	b := limit.NewTokenBucketWithSleeper(2, 0, clock, rec.Sleep)

	if !b.AllowN(2) {
		t.Fatal("drain the bucket")
	}
	err := b.Wait(context.Background(), 1)
	if !errors.Is(err, limit.ErrZeroRefill) {
		t.Fatalf("err = %v, want ErrZeroRefill", err)
	}
	if d := rec.Delays(); len(d) != 0 {
		t.Fatalf("zero refill must fail fast, not sleep; sleeper saw %v", d)
	}
}

func TestCh06_TokenBucket_Wait_ContextCancelled_ReturnsContextError(t *testing.T) {
	clock := testutil.NewFakeClock()
	rec := &testutil.RecordingSleeper{}
	ctx, cancel := context.WithCancel(context.Background())
	// The "sleep" cancels the context, exactly what a real timer does not do — this models
	// a client hanging up (or an operator hitting Ctrl-C) while we are waiting to proceed.
	sleeper := func(ctx context.Context, d time.Duration) error {
		cancel()
		return rec.Sleep(ctx, d) // sees the cancelled context, returns its error without recording
	}
	b := limit.NewTokenBucketWithSleeper(2, 1, clock, sleeper)

	if !b.AllowN(2) {
		t.Fatal("drain the bucket")
	}
	err := b.Wait(ctx, 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestCh06_TokenBucket_Wait_ImmediateWhenTokensAvailable(t *testing.T) {
	clock := testutil.NewFakeClock()
	rec := &testutil.RecordingSleeper{}
	b := limit.NewTokenBucketWithSleeper(10, 1, clock, rec.Sleep)

	if err := b.Wait(context.Background(), 5); err != nil {
		t.Fatalf("Wait with a full bucket: %v", err)
	}
	if d := rec.Delays(); len(d) != 0 {
		t.Fatalf("no sleep expected when tokens are available; sleeper saw %v", d)
	}
	if err := b.Wait(context.Background(), 0); err != nil {
		t.Fatalf("Wait(ctx, 0) is free: %v", err)
	}
}

func TestCh06_LeakyBucket_AdmitsBurstThenRejects_AndLeaksOverTime(t *testing.T) {
	clock := testutil.NewFakeClock()
	b := limit.NewLeakyBucket(3, 2, clock) // drains 2 units per second

	for i := 1; i <= 3; i++ {
		if !b.Allow() {
			t.Fatalf("Allow #%d: the burst must be admitted", i)
		}
	}
	if b.Allow() {
		t.Fatal("level is at capacity 3; the next request must be rejected")
	}

	clock.Advance(500 * time.Millisecond) // leaks exactly 1 unit
	if !b.Allow() {
		t.Fatal("one leaked unit buys exactly one admission")
	}
	if b.Allow() {
		t.Fatal("only one unit leaked, not two")
	}

	clock.Advance(time.Second) // level 3 → leaks 2 → level 1
	if !b.Allow() || !b.Allow() {
		t.Fatal("a full second of leaking buys two admissions")
	}
	if b.Allow() {
		t.Fatal("level is back at capacity; reject again")
	}
}

func TestCh06_FixedWindow_AllowsLimitThenResetsAtTheBoundary(t *testing.T) {
	clock := testutil.NewFakeClock()
	w := limit.NewFixedWindow(2, time.Minute, clock)

	if !w.Allow() || !w.Allow() {
		t.Fatal("the first two requests in the window must be admitted")
	}
	if w.Allow() {
		t.Fatal("the third request exceeds the limit of 2 for this window")
	}

	clock.Advance(time.Minute - time.Nanosecond) // 1ns before the boundary
	if w.Allow() {
		t.Fatal("one nanosecond early is still the old window")
	}

	clock.Advance(time.Nanosecond) // exactly at the boundary
	if !w.Allow() {
		t.Fatal("exactly at the boundary a fresh window opens and the count resets")
	}
	if !w.Allow() {
		t.Fatal("the new window has its own full limit")
	}
	if w.Allow() {
		t.Fatal("the new window enforces its own limit too")
	}
}

func TestCh06_FixedWindow_FirstCallStartsTheWindow(t *testing.T) {
	clock := testutil.NewFakeClock()
	w := limit.NewFixedWindow(2, time.Minute, clock)

	clock.Advance(10 * time.Minute) // long idle BEFORE the first request: no window may be burned
	if !w.Allow() || !w.Allow() {
		t.Fatal("the first call starts the window; the counter starts at zero")
	}
	if w.Allow() {
		t.Fatal("the first window still enforces its limit")
	}
}

func TestCh06_SlidingLog_AllowsExactlyLimitPerRollingWindow(t *testing.T) {
	clock := testutil.NewFakeClock()
	s := limit.NewSlidingLog(2, time.Minute, clock)

	if !s.Allow() || !s.Allow() {
		t.Fatal("the first two requests must be admitted")
	}
	if s.Allow() {
		t.Fatal("a third request inside the window must be refused")
	}

	clock.Advance(time.Minute) // exactly one full window: both hits expire
	if !s.Allow() || !s.Allow() {
		t.Fatal("after a full window the log is empty again")
	}
	if s.Allow() {
		t.Fatal("the new hits count towards the limit too")
	}
}

func TestCh06_SlidingLog_BoundaryIsExact(t *testing.T) {
	clock := testutil.NewFakeClock()
	s := limit.NewSlidingLog(2, time.Minute, clock)

	s.Allow() // hit at t
	clock.Advance(30 * time.Second)
	s.Allow()                       // hit at t+30s
	clock.Advance(30 * time.Second) // now = t+60s: the first hit is EXACTLY one window old

	if !s.Allow() {
		t.Fatal("a hit exactly `window` old must have expired")
	}
	if s.Allow() {
		t.Fatal("the second hit (30s old) still counts; only one slot freed")
	}
}

func TestCh06_TokenAndLeakyBucket_MeterTheSameScenarioIdentically(t *testing.T) {
	clockA, clockB := testutil.NewFakeClock(), testutil.NewFakeClock()
	tok := limit.NewTokenBucket(3, 1, clockA) // 1 token per second
	leak := limit.NewLeakyBucket(3, 1, clockB)

	for _, meter := range []struct {
		name  string
		allow func() bool
	}{
		{"token", tok.Allow},
		{"leaky", leak.Allow},
	} {
		for i := 1; i <= 3; i++ {
			if !meter.allow() {
				t.Fatalf("%s: burst request %d should be admitted", meter.name, i)
			}
		}
		if meter.allow() {
			t.Fatalf("%s: the burst of 3 is spent; the fourth request must be refused", meter.name)
		}
	}

	clockA.Advance(time.Second)
	clockB.Advance(time.Second) // one second of refill / leak for each
	if !tok.Allow() || !leak.Allow() {
		t.Fatal("one second at rate 1 buys exactly one admission in both meters")
	}
	if tok.Allow() || leak.Allow() {
		t.Fatal("only one unit was refilled/leaked; a second admission must be refused")
	}
}

func TestCh06_TokenBucket_ConcurrentAllowAdmitsExactlyCapacity(t *testing.T) {
	clock := testutil.NewFakeClock()
	b := limit.NewTokenBucket(25, 0, clock) // no refill: only the 25 starting tokens exist

	var admitted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if b.Allow() {
				admitted.Add(1)
			}
		}()
	}
	wg.Wait()

	if got := admitted.Load(); got != 25 {
		t.Fatalf("admitted %d requests, want exactly 25 (the check and the consume must be one critical section)", got)
	}
}

func TestCh06_ConstructorsClampNonsenseInputs(t *testing.T) {
	clock := testutil.NewFakeClock()

	b := limit.NewTokenBucket(0, -3, clock) // capacity 0 → 1, negative rate → 0
	if !b.Allow() {
		t.Fatal("a clamped capacity-1 bucket admits one request")
	}
	if b.Allow() {
		t.Fatal("a clamped rate of 0 never refills")
	}

	lk := limit.NewLeakyBucket(0, -3, clock) // capacity 0 → 1, negative leak → 0
	if !lk.Allow() {
		t.Fatal("a clamped capacity-1 leaky bucket admits one request")
	}
	if lk.Allow() {
		t.Fatal("a clamped leak of 0 never drains")
	}

	w := limit.NewFixedWindow(0, 0, clock) // limit 0 → 1, window 0 → fallback 1s
	if !w.Allow() {
		t.Fatal("a clamped limit-1 window admits one request")
	}
	if w.Allow() {
		t.Fatal("a clamped limit-1 window refuses the second")
	}
	clock.Advance(time.Second)
	if !w.Allow() {
		t.Fatal("the window fallback is 1s; after 1s it resets")
	}

	s := limit.NewSlidingLog(0, -time.Minute, clock) // limit 0 → 1, window ≤ 0 → fallback 1s
	if !s.Allow() {
		t.Fatal("a clamped limit-1 log admits one request")
	}
	if s.Allow() {
		t.Fatal("a clamped limit-1 log refuses the second")
	}
}
