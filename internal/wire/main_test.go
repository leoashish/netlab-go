package wire_test

import (
	"testing"

	"netlab/internal/testutil"
)

// Every package's tests run under the shared watchdog: a bug that hangs fails with a goroutine dump instead of
// waiting for `go test`'s (much longer) panic timeout.
func TestMain(m *testing.M) { testutil.Main(m) }
