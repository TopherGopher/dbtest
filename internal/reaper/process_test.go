package reaper

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests signal a real process, because only a parent can see how its
// child died. The process is a second copy of this test binary: when
// stuckHelper is set, TestMain runs the helper instead of the tests. Because
// the helper is in this package, it can lower the unexported reap timeout.

// stuckHelper names the environment variable that turns the test binary into
// a helper. The helper registers one teardown that never returns, then waits
// for a signal. The variable's value is the helper's reap timeout.
const stuckHelper = "REAPER_STUCK_HELPER"

func TestMain(m *testing.M) {
	if timeout := os.Getenv(stuckHelper); timeout != "" {
		runStuckHelper(timeout)
	}
	os.Exit(m.Run())
}

// runStuckHelper prints "ready" once its teardown is registered, and
// "tearing down" once a reap has started it.
func runStuckHelper(timeout string) {
	var err error
	if reapTimeout, err = time.ParseDuration(timeout); err != nil {
		panic(err)
	}
	// The teardown ignores its context, as a removal stuck in a system call
	// would.
	Register("stuck", func(context.Context) error {
		os.Stdout.WriteString("tearing down\n")
		select {}
	}, nil)

	// Another package listening for the signal too, which only Reset undoes.
	signal.Notify(make(chan os.Signal, 1), syscall.SIGTERM)
	os.Stdout.WriteString("ready\n")
	time.Sleep(time.Minute)
	os.Exit(3) // getting here means no signal ended the process
}

func TestASecondSignalDuringAReapEndsTheProcessAtOnce(t *testing.T) {
	h := startStuckHelper(t, time.Minute)
	require.NoError(t, h.cmd.Process.Signal(syscall.SIGTERM), "the first signal starts the reap")
	h.waitForLine(t, "tearing down")

	require.NoError(t, h.cmd.Process.Signal(syscall.SIGTERM), "the second signal arrives during the reap")
	state := h.waitForExit(t, 10*time.Second)

	assert.Equal(t, syscall.SIGTERM, signalOf(t, state), "the process dies of the second signal without waiting for the stuck teardown, which is what a second Ctrl-C means")
}

func TestTheReapAfterASignalStopsAtItsTimeout(t *testing.T) {
	const timeout = 500 * time.Millisecond
	h := startStuckHelper(t, timeout)
	signalled := time.Now()
	require.NoError(t, h.cmd.Process.Signal(syscall.SIGTERM), "the signal starts the reap")
	h.waitForLine(t, "tearing down")

	state := h.waitForExit(t, 20*time.Second)

	assert.Equal(t, syscall.SIGTERM, signalOf(t, state), "a teardown that never returns cannot keep a dying process alive: the reap gives up and the signal still ends it")
	assert.GreaterOrEqual(t, time.Since(signalled), timeout, "the reap gave the stuck teardown its whole timeout before giving up")
}

// helper is a running copy of the test binary acting as the stuck helper.
type helper struct {
	cmd    *exec.Cmd
	lines  <-chan string // its stdout, a line at a time
	exited chan struct{} // closed once it has been waited for
}

// startStuckHelper starts the helper and waits until it is ready to be
// signalled. The helper is killed, if still running, when the test ends.
func startStuckHelper(t *testing.T, timeout time.Duration) *helper {
	t.Helper()
	// A pipe of the test's own, rather than StdoutPipe, so that Wait can run
	// while the lines are still being read.
	r, w := io.Pipe()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), stuckHelper+"="+timeout.String())
	// The helper's report of what it left behind would look like a real leak
	// in a passing run, so it is shown only when the test fails.
	var stderr strings.Builder
	cmd.Stdout, cmd.Stderr = w, &stderr
	require.NoError(t, cmd.Start(), "the helper must start")
	h := &helper{cmd: cmd, lines: readLines(r), exited: make(chan struct{})}
	go func() {
		_ = cmd.Wait() // a signal death is an error here; ProcessState says which signal
		_ = w.Close()
		close(h.exited)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-h.exited
		if t.Failed() {
			t.Logf("the helper's stderr:\n%s", stderr.String())
		}
	})
	h.waitForLine(t, "ready")
	return h
}

// readLines sends each line of r on the returned channel, and closes it at
// the end of r.
func readLines(r io.Reader) <-chan string {
	lines := make(chan string, 8)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	return lines
}

// waitForLine fails the test unless the next line the helper prints is want.
func (h *helper) waitForLine(t *testing.T, want string) {
	t.Helper()
	select {
	case got, ok := <-h.lines:
		require.True(t, ok, "the helper exited before printing %q", want)
		require.Equal(t, want, got, "the helper reports its progress in order")
	case <-time.After(time.Minute):
		require.Fail(t, "the helper stopped reporting its progress", "the test was waiting for %q", want)
	}
}

// waitForExit waits for the helper to end, or fails rather than hanging.
func (h *helper) waitForExit(t *testing.T, timeout time.Duration) *os.ProcessState {
	t.Helper()
	select {
	case <-h.exited:
		return h.cmd.ProcessState
	case <-time.After(timeout):
		require.Fail(t, "the helper was signalled and is still running")
		return nil
	}
}

// signalOf returns the signal the process died of, failing the test if it
// exited instead.
func signalOf(t *testing.T, state *os.ProcessState) syscall.Signal {
	t.Helper()
	status, ok := state.Sys().(syscall.WaitStatus)
	require.True(t, ok, "this test is unix-only, where a wait status shows a signal death")
	require.True(t, status.Signaled(), "the process has to die of a signal, not exit (exit code %d)", state.ExitCode())
	return status.Signal()
}
