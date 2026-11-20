package mongod_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/dbtest/dockerclient"
	"github.com/tophergopher/dbtest/mongod"
)

// ownHandlerHelper names the environment variable that makes
// TestOwnSignalHandlerHelper act as a program for another test to signal. It
// holds the path of a file the mocked daemon writes when the container is
// removed.
const ownHandlerHelper = "MONGOD_OWN_HANDLER_HELPER"

// TestOwnSignalHandlerHelper is not a test of its own. In a copy of this
// test binary started by TestADisabledHandlerLeavesTheSignalToTheProgram, it
// is a program with its own shutdown: it starts containers, turns the
// handler off, and handles SIGTERM itself. It prints "ready", then "stopping"
// when its handler runs, waits for standard input to close, reaps and exits
// with status 7.
func TestOwnSignalHandlerHelper(t *testing.T) {
	marker := os.Getenv(ownHandlerHelper)
	if marker == "" {
		return
	}
	m, _ := readyMock(t)
	m.ContainerRemoveFunc = func(context.Context, string, dockerclient.RemoveOptions) error {
		return os.WriteFile(marker, nil, 0o600)
	}
	// One container starts before the handler is turned off and one after,
	// so both orders are covered.
	startOrExit(m)
	mongod.DisableSignalHandler()
	startOrExit(m)
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM)
	fmt.Println("ready")

	<-sigs
	fmt.Println("stopping")
	// Wait until the test closes standard input. It does so only after
	// checking that nothing was removed, so the check cannot race the reap.
	_, _ = io.Copy(io.Discard, os.Stdin)
	if err := mongod.ReapRunningContainers(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "the helper could not reap:", err)
		os.Exit(1)
	}
	os.Exit(7)
}

// startOrExit starts a container against the mocked daemon, or ends the
// helper.
func startOrExit(docker dockerclient.Client) {
	if _, err := mongod.StartWithOptions(context.Background(), mongod.DefaultOptions().WithDocker(docker)); err != nil {
		fmt.Fprintln(os.Stderr, "the helper could not start:", err)
		os.Exit(2)
	}
}

// A program with its own shutdown turns off mongod's signal handler. A SIGTERM must then
// reach the program's own handler, and the program decides when its
// containers are removed. With the handler on, the reaper would remove them
// and re-raise the signal, so the process would die of it.
func TestADisabledHandlerLeavesTheSignalToTheProgram(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "removed")
	cmd := exec.Command(os.Args[0], "-test.run=^TestOwnSignalHandlerHelper$")
	cmd.Env = append(os.Environ(), ownHandlerHelper+"="+marker)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err, "the test controls when the helper shuts down")
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err, "the test reads the helper's progress")
	require.NoError(t, cmd.Start(), "the helper starts")
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	lines := bufio.NewReader(stdout)
	require.Equal(t, "ready", readLine(t, lines), "the helper started its containers")

	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM), "the helper is signalled")
	require.Equal(t, "stopping", readLine(t, lines), "the program's own handler ran")
	// Long enough for a handler wrongly left installed to have removed the
	// container and re-raised the signal.
	time.Sleep(2 * time.Second)
	assert.NoFileExists(t, marker, "nothing is removed before the program asks")

	require.NoError(t, stdin.Close(), "the helper may now reap and exit")
	state := waitForExit(t, cmd)

	status, ok := state.Sys().(syscall.WaitStatus)
	require.True(t, ok, "the exit status shows whether a signal ended it")
	assert.False(t, status.Signaled(), "the signal did not end the process")
	assert.Equal(t, 7, state.ExitCode(), "the program exited as it chose")
	assert.FileExists(t, marker, "its own reap removed the containers")
}

// readLine reads the helper's next line, or fails rather than hanging.
func readLine(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	lines := make(chan string, 1)
	go func() {
		text, _ := r.ReadString('\n')
		lines <- strings.TrimSuffix(text, "\n")
	}()
	select {
	case text := <-lines:
		return text
	case <-time.After(time.Minute):
		require.Fail(t, "the helper printed nothing")
		return ""
	}
}

// waitForExit waits for the helper to end, or fails rather than hanging.
func waitForExit(t *testing.T, cmd *exec.Cmd) *os.ProcessState {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
		return cmd.ProcessState
	case <-time.After(time.Minute):
		require.Fail(t, "the helper did not exit")
		return nil
	}
}
