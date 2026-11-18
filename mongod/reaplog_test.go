package mongod_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/mongod"
)

// signalledHelper names the environment variable that makes
// TestSignalledStartHelper act as a process for another test to signal.
const signalledHelper = "MONGOD_SIGNALLED_HELPER"

// TestSignalledStartHelper is not a test of its own. In a copy of this test
// binary started by TestAContainerLeftAfterASignalIsLoggedToItsStartsLogger,
// it starts a container the mocked daemon will not remove, logging Error
// records as JSON on stdout, and waits to be signalled.
func TestSignalledStartHelper(t *testing.T) {
	if os.Getenv(signalledHelper) == "" {
		return
	}
	m, _ := readyMock(t)
	m.ContainerRemoveFunc = func(context.Context, string, dockerclient.RemoveOptions) error {
		return errors.New("the daemon refused")
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	c, err := mongod.StartWithOptions(context.Background(), mongod.DefaultOptions().WithDocker(m).WithLogger(logger))
	if err != nil {
		fmt.Fprintln(os.Stderr, "the helper could not start:", err)
		os.Exit(2)
	}
	fmt.Printf("{\"ready\":%q}\n", c.Name())
	time.Sleep(time.Minute)
	os.Exit(3) // getting here means no signal ended the process
}

// logRecord is the part of a JSON log record the test reads.
type logRecord struct {
	Level     string `json:"level"`
	Ready     string `json:"ready"`
	Container string `json:"container"`
	Signal    string `json:"signal"`
}

func TestAContainerLeftAfterASignalIsLoggedToItsStartsLogger(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestSignalledStartHelper$")
	cmd.Env = append(os.Environ(), signalledHelper+"=1")
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err, "the test reads the helper's records from its stdout")
	require.NoError(t, cmd.Start(), "the helper must start")
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	records := make(chan logRecord, 16)
	go func() {
		defer close(records)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			var r logRecord
			if json.Unmarshal(scanner.Bytes(), &r) == nil {
				records <- r
			}
		}
	}()
	name := nextRecord(t, records).Ready
	require.NotEmpty(t, name, "the helper reports the container it started")

	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM), "the signal starts the reap")

	var logged []logRecord
	for r := range recordsUntilClosed(t, records) {
		logged = append(logged, r)
	}
	assert.Contains(t, logged, logRecord{Level: "ERROR", Container: name, Signal: syscall.SIGTERM.String()},
		"the logger given to WithLogger is told about the container the signal left behind")
}

// nextRecord returns the next record, or fails rather than hanging.
func nextRecord(t *testing.T, records <-chan logRecord) logRecord {
	t.Helper()
	select {
	case r, ok := <-records:
		require.True(t, ok, "the helper exited before reporting its container")
		return r
	case <-time.After(time.Minute):
		require.Fail(t, "the helper did not report its container")
		return logRecord{}
	}
}

// recordsUntilClosed passes on records until the helper's stdout closes,
// failing the test if that takes too long.
func recordsUntilClosed(t *testing.T, records <-chan logRecord) <-chan logRecord {
	t.Helper()
	out := make(chan logRecord, 16)
	timeout := time.After(time.Minute)
	go func() {
		defer close(out)
		for {
			select {
			case r, ok := <-records:
				if !ok {
					return
				}
				out <- r
			case <-timeout:
				return
			}
		}
	}()
	return out
}
