package testutil

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"
)

// Main runs a package's tests under a WATCHDOG. Every package's main_test.go calls it:
//
//	func TestMain(m *testing.M) { testutil.Main(m) }
//
// Why: a network program with a bug usually does not crash, it HANGS (a goroutine waits for a packet that never comes),
// and plain `go test` then waits ten minutes before it says anything. This watchdog gives up after 90 seconds (override with
// the environment variable NETLAB_TEST_TIMEOUT, e.g. 30s or 5m; multiplied by -count), prints an explanation and the stack of EVERY goroutine --
// which shows exactly where each one is stuck -- and exits. Healthy runs take a few seconds per package.
func Main(m *testing.M) {
	limit := 90 * time.Second
	if v := os.Getenv("NETLAB_TEST_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			limit = d
		}
	}
	// `go test -count=10` repeats the whole package inside THIS process, so the budget grows with the repeat count.
	flag.Parse()
	if f := flag.Lookup("test.count"); f != nil {
		var count int
		if _, err := fmt.Sscan(f.Value.String(), &count); err == nil && count > 1 {
			limit *= time.Duration(count)
		}
	}
	timer := time.AfterFunc(limit, func() {
		fmt.Fprintf(os.Stderr, "\nWATCHDOG: the tests of this package have been running for %v.\n"+
			"That almost always means a DEADLOCK in your code: a goroutine is waiting for something that never happens\n"+
			"(a read on a socket whose peer never writes, a channel nobody sends on, a WaitGroup that is never Done...).\n"+
			"The stack of every goroutine follows; look for the ones stuck in YOUR code, not in the testing package.\n\n", limit)
		buf := make([]byte, 1<<20)
		os.Stderr.Write(buf[:runtime.Stack(buf, true)])
		os.Exit(2)
	})
	code := m.Run()
	timer.Stop()
	os.Exit(code)
}
