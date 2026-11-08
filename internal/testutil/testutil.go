// Package testutil holds helpers shared by the tests: bounded waits, goroutine-leak checks, temp files, a fake clock.
package testutil

import (
	"context"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strings"
	"sync"
	"testing"
	"time"

	"netlab/internal/todo"
)

// Timeout is the upper bound for "this should finish almost instantly" waits: a hang becomes a clear failure.
const Timeout = 5 * time.Second

// Receive waits for one value from ch, failing the test (instead of hanging) after d. It fails if ch is closed.
func Receive[T any](t testing.TB, ch <-chan T, d time.Duration) T {
	t.Helper()
	select {
	case v, ok := <-ch:
		if !ok {
			t.Fatal("channel closed while waiting for a value")
		}
		return v
	case <-time.After(d):
		t.Fatalf("timed out after %v waiting for a value", d)
		panic("unreachable")
	}
}

// TryReceive is Receive that reports (value, true), or (zero, false) if the channel was closed. Times out like Receive.
func TryReceive[T any](t testing.TB, ch <-chan T, d time.Duration) (T, bool) {
	t.Helper()
	select {
	case v, ok := <-ch:
		return v, ok
	case <-time.After(d):
		t.Fatalf("timed out after %v waiting for a value or a close", d)
		panic("unreachable")
	}
}

// Drain reads ch until it is closed and returns everything, failing the test if that takes longer than d.
func Drain[T any](t testing.TB, ch <-chan T, d time.Duration) []T {
	t.Helper()
	var out []T
	deadline := time.After(d)
	for {
		select {
		case v, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, v)
		case <-deadline:
			t.Fatalf("timed out after %v: the channel was never closed (got %d values so far)", d, len(out))
			return out
		}
	}
}

// WaitClosed waits for a signal channel to be closed.
func WaitClosed(t testing.TB, ch <-chan struct{}, d time.Duration) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(d):
		t.Fatalf("timed out after %v waiting for a channel to close", d)
	}
}

// NoBlock asserts that nothing is ready to be received from ch right now.
func NoBlock[T any](t testing.TB, ch <-chan T) {
	t.Helper()
	select {
	case v, ok := <-ch:
		t.Fatalf("expected nothing to be ready, got (%v, open=%v)", v, ok)
	default:
	}
}

// ExpectError fails the test unless err is a REAL error. An unfinished stub's "not implemented" error does not count,
// otherwise "this should fail" tests would pass before you had written anything.
func ExpectError(t testing.TB, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if errors.Is(err, todo.ErrNotImplemented) {
		t.Fatalf("expected a real error from your code, but this is still the unfinished stub: %v", err)
	}
}

// NoLeaks fails the test if goroutines started during it are still running when it ends. Call it first in the test.
func NoLeaks(t testing.TB) {
	t.Helper()
	baseline := runtime.NumGoroutine()
	t.Cleanup(func() {
		deadline := time.Now().Add(3 * time.Second)
		for runtime.NumGoroutine() > baseline {
			if time.Now().After(deadline) {
				var sb strings.Builder
				_ = pprof.Lookup("goroutine").WriteTo(&sb, 1)
				t.Errorf("goroutine leak: %d running, %d at the start of the test.\n%s", runtime.NumGoroutine(), baseline, sb.String())
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
}

// Rand returns a deterministic random source: the same seed always produces the same data, so tests never flake.
func Rand(seed int64) *rand.Rand { return rand.New(rand.NewSource(seed)) }

// WriteFile creates dir/name with content and returns its path.
func WriteFile(t testing.TB, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// ReadLines returns the lines of a text file (without trailing newlines).
func ReadLines(t testing.TB, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := strings.TrimSuffix(string(b), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// DirEntries lists the names inside dir (non-recursive).
func DirEntries(t testing.TB, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// Within runs fn and fails the test if it does not return within d. fn must not call t.Fatal (it runs in another goroutine).
func Within(t testing.TB, d time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("did not finish within %v", d)
	}
}

// FakeClock only moves when the test says so.
type FakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func NewFakeClock() *FakeClock { return &FakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)} }

func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// RecordingSleeper records every requested delay and returns immediately (refusing to "sleep" on a cancelled context).
type RecordingSleeper struct {
	mu     sync.Mutex
	delays []time.Duration
}

func (s *RecordingSleeper) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.delays = append(s.delays, d)
	s.mu.Unlock()
	return nil
}

func (s *RecordingSleeper) Delays() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.delays...)
}
