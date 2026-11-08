package httpmini_test

import (
	"testing"

	"netlab/internal/testutil"
)

// TestMain runs the tests under a watchdog (see testutil.Main): a hang is reported after 90s with a goroutine dump.
func TestMain(m *testing.M) { testutil.Main(m) }
