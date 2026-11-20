package reaper_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/dbtest/dockerapi"
	"github.com/tophergopher/dbtest/dockerclient"
)

// This test needs a real Docker daemon and fails, rather than skipping, when
// there is none. The signal path is the one piece of this package that unit
// tests cannot reach, so a silent skip would leave it untested.

// TestIntegrationSignalReapsAndStillDies is the regression test for the
// implementation this package replaces, which reaped and then returned: the
// process went on running, so Ctrl-C no longer terminated it.
//
// Both halves are asserted, because either alone would have passed against the
// old code at some point: the container has to be gone, and the process has to
// have died of the signal it was sent rather than exiting on its own terms.
func TestIntegrationSignalReapsAndStillDies(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(sig.String(), func(t *testing.T) {
			docker := liveDocker(t)
			helper := buildHelper(t)
			label := fmt.Sprintf("%s-%d", strings.ReplaceAll(t.Name(), "/", "_"), time.Now().UnixNano())

			cmd := exec.Command(helper)
			cmd.Env = programEnv("REAPHELPER_LABEL=" + label)
			stdout, err := cmd.StdoutPipe()
			require.NoError(t, err, "the test reads the container id from the helper's stdout")
			cmd.Stderr = os.Stderr
			require.NoError(t, cmd.Start(), "the helper binary must start")

			id, name := containerFromHelper(t, stdout)
			t.Cleanup(func() {
				// Whatever happens, do not leave a container behind.
				_ = docker.ContainerRemove(context.Background(), id, dockerclient.RemoveOptions{Force: true, RemoveVolumes: true})
			})

			_, err = docker.ContainerInspect(context.Background(), id)
			require.NoError(t, err, "the container exists before the signal")

			require.NoError(t, cmd.Process.Signal(sig), "sending %s to the helper must succeed", sig)

			// Generous against the worst legitimate case -- a reap that uses
			// its whole 30s budget plus the re-raise grace -- without making a
			// real failure take a minute and a half to report.
			state := waitFor(t, cmd, 60*time.Second)

			status, ok := state.Sys().(syscall.WaitStatus)
			require.True(t, ok, "the test is unix-only")
			assert.True(t, status.Signaled(),
				"the process dies of the signal (exit code %d)", state.ExitCode())
			assert.Equal(t, sig, status.Signal(),
				"of the signal it was sent")

			_, err = docker.ContainerInspect(context.Background(), id)
			assert.True(t, dockerclient.IsNotFound(err),
				"container %s (%s) was not reaped; err %v", name, id, err)
		})
	}
}

// buildHelper compiles the helper once per test run.
func buildHelper(t *testing.T) string {
	t.Helper()
	return buildProgram(t, "github.com/tophergopher/dbtest/internal/reaphelper")
}

// containerFromHelper reads the line the helper prints once its container is up.
func containerFromHelper(t *testing.T, stdout interface{ Read([]byte) (int, error) }) (id, name string) {
	t.Helper()
	// Generous: the first run of the day may be pulling the image.
	text := firstLine(t, stdout, 4*time.Minute)
	fields := strings.Fields(text)
	require.Len(t, fields, 4, "the helper prints \"reaphelper: container <id> <name>\"; got %q", text)
	return fields[2], fields[3]
}

// firstLine reads one line from a helper's stdout, or fails rather than
// hanging. Every helper in this package announces itself on one line before
// there is anything worth signalling it about.
func firstLine(t *testing.T, stdout interface{ Read([]byte) (int, error) }, timeout time.Duration) string {
	t.Helper()
	type line struct {
		text string
		err  error
	}
	lines := make(chan line, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			lines <- line{text: scanner.Text()}
			return
		}
		lines <- line{err: scanner.Err()}
	}()

	select {
	case got := <-lines:
		require.NoError(t, got.err, "reading the helper's first line must succeed")
		return got.text
	case <-time.After(timeout):
		require.Fail(t, "the helper never announced itself", "it may be pulling, or it died before printing")
		return ""
	}
}

// waitFor waits for the process to end, or fails the test rather than hanging.
func waitFor(t *testing.T, cmd *exec.Cmd, timeout time.Duration) *os.ProcessState {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var exit *exec.ExitError
		if err != nil && !errors.As(err, &exit) {
			require.NoError(t, err, "only the exit status may fail")
		}
		return cmd.ProcessState
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		require.Fail(t, "the helper did not exit after being signalled",
			"a signalled process ends")
		return nil
	}
}

// liveDocker returns a client for the real daemon, or fails with guidance.
func liveDocker(t *testing.T) dockerclient.Client {
	t.Helper()
	client, err := dockerapi.FromEnv()
	require.NoError(t, err, "no Docker client; set DOCKER_HOST to a running daemon")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	require.NoError(t, client.Negotiate(ctx), "reaper integration: no Docker daemon reachable at %s", client.Host())
	return client
}
