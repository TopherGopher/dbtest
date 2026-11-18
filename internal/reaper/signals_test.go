package reaper

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A process that ignores every signal the reaper would listen for must be
// left ignoring them: listening would turn a signal it was meant to survive
// into a reap and an exit. ignored_test.go proves that against a real
// process; this covers the state the package is left in.
func TestRegisteringWhileEverySignalIsIgnoredInstallsNothing(t *testing.T) {
	if os.Getenv(ownProcess) == "" {
		runInOwnProcess(t)
		return
	}
	signal.Ignore(syscall.SIGINT, syscall.SIGTERM)

	Register("container", func(context.Context) error { return nil }, nil)

	assert.False(t, Installed(), "there is nothing to listen for, so no handler is claimed")
	assert.True(t, signal.Ignored(syscall.SIGINT), "and the signal is still ignored")
	assert.Equal(t, []string{"container"}, Names(), "while the container is still registered for an explicit Reap")
}

// ownProcess is set in the copy of the test binary runInOwnProcess starts.
const ownProcess = "REAPER_TEST_OWN_PROCESS"

// runInOwnProcess runs the calling test again in a copy of this test binary,
// and passes if that copy does.
//
// A test that ignores SIGINT needs it. Go cannot take signal.Ignore back for
// SIGINT: the signal stays ignored by the operating system, and every process
// started afterwards inherits that, so the helpers other tests signal would
// never die.
func runInOwnProcess(t *testing.T) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v")
	cmd.Env = append(os.Environ(), ownProcess+"=1")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "the test must pass in a process of its own:\n%s", out)
	require.Contains(t, string(out), "--- PASS: "+t.Name(), "and must really have run there:\n%s", out)
}
