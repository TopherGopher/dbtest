package reaper_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A non-interactive shell starts a background job with SIGINT ignored, and
// os/signal says that calling Notify for an ignored SIGINT installs a handler
// and stops it being ignored. A process that was meant to survive Ctrl-C would
// then start reaping and exiting on one, because it happened to register a
// container.
//
// This needs a real process. The disposition is inherited at exec, the test
// runner has handlers of its own, and the outcome being asserted -- that the
// process is still there afterwards -- is something only a parent can see.
// `trap ” INT` in the shell is what produces the inherited "ignored" state.
func TestIgnoredSignalsAreLeftIgnored(t *testing.T) {
	helper := buildIgnoreHelper(t)
	marker := filepath.Join(t.TempDir(), "reaped")

	// exec so that the shell is replaced: the signal has to reach the process
	// under test, not a shell that would decide for itself what to do with it.
	cmd := exec.Command("/bin/sh", "-c", `trap '' INT; exec "$0"`, helper)
	cmd.Env = programEnv("IGNOREHELPER_MARKER=" + marker)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err, "the test reads the helper's readiness line from stdout")
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Start(), "the helper binary must start")
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	ready := firstLine(t, stdout, 2*time.Minute)
	require.Equal(t, "ready true", ready,
		"SIGTERM is still worth listening for; got %q", ready)

	require.NoError(t, cmd.Process.Signal(syscall.SIGINT), "sending SIGINT to the helper must succeed")

	// Long enough for a handler that should not exist to have reaped and
	// re-raised: the reap is immediate here, and the re-raise follows it.
	time.Sleep(2 * time.Second)
	require.NoError(t, cmd.Process.Signal(syscall.Signal(0)),
		"an ignored SIGINT does not end the process")
	assert.NoFileExists(t, marker,
		"nor tear anything down")

	// The other half: a signal that was not ignored still works, so the
	// filter has not simply disabled the package.
	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM), "sending SIGTERM to the helper must succeed")
	state := waitFor(t, cmd, 60*time.Second)

	status, ok := state.Sys().(syscall.WaitStatus)
	require.True(t, ok, "the test is unix-only")
	assert.True(t, status.Signaled(), "SIGTERM reaps, then ends the process (exit code %d)", state.ExitCode())
	assert.Equal(t, syscall.SIGTERM, status.Signal(), "and it has to die of the signal it was sent")
	assert.FileExists(t, marker, "the teardown ran")
}

// buildIgnoreHelper compiles the helper for this test.
func buildIgnoreHelper(t *testing.T) string {
	t.Helper()
	return buildProgram(t, "github.com/tophergopher/dbtest/internal/ignorehelper")
}

// A program built with -cover writes its coverage only when it exits
// normally. After its teardown, the reaper sends the signal to the program
// again so that it dies of it, which would lose the coverage. CI counts the
// reaper's signal path only because the helpers write their counters first,
// through BeforeReRaise.
func TestASignalledHelperKeepsItsCoverage(t *testing.T) {
	coverDir := t.TempDir()
	t.Setenv(helperCoverDirEnv, coverDir)
	helper := buildIgnoreHelper(t)
	cmd := exec.Command(helper)
	cmd.Env = programEnv()
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err, "the test reads the helper's readiness line from stdout")
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Start(), "the helper binary must start")
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	require.Equal(t, "ready true", firstLine(t, stdout, 2*time.Minute), "the helper registered, with the handler installed")

	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM), "sending SIGTERM to the helper must succeed")
	state := waitFor(t, cmd, 60*time.Second)

	status, ok := state.Sys().(syscall.WaitStatus)
	require.True(t, ok && status.Signaled(), "the helper dies of the signal, as it would without coverage")
	counters, err := filepath.Glob(filepath.Join(coverDir, "covcounters.*"))
	require.NoError(t, err, "the coverage directory can be listed")
	assert.NotEmpty(t, counters, "the coverage counters were written before the process died")
}
