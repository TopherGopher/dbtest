package mongod_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/dbtest/dockerclient"
	"github.com/tophergopher/dbtest/dockermock"
	"github.com/tophergopher/dbtest/mongod"
)

// documentedOutputLimit is the most of a container's output that
// ContainerExitedError.Output holds, as its doc says.
const documentedOutputLimit = 8 << 10

// exitedWithCode is what the daemon reports for a container whose process
// has ended. It publishes no ports any more.
func exitedWithCode(code int) dockerclient.ContainerInspect {
	return dockerclient.ContainerInspect{ID: containerID, State: dockerclient.ContainerState{Status: "exited", ExitCode: code}}
}

// exitsAfterStarting makes m's container exit with code 2 once the start has
// seen its port, as mongod does when it rejects a flag. Nothing answers on
// the port, as nothing does once a container has exited.
func exitsAfterStarting(t testing.TB, m *dockermock.Mock) {
	port := closedPort(t)
	inspects := 0
	m.ContainerInspectFunc = func(context.Context, string) (dockerclient.ContainerInspect, error) {
		if inspects++; inspects == 1 {
			return running(port), nil
		}
		return exitedWithCode(2), nil
	}
}

// writing returns a ContainerLogsTo that writes stdout and stderr, then
// returns err.
func writing(stdout, stderr string, err error) func(context.Context, string, io.Writer, io.Writer) error {
	return func(_ context.Context, _ string, out, errOut io.Writer) error {
		if out != nil {
			_, _ = io.WriteString(out, stdout)
		}
		if errOut != nil {
			_, _ = io.WriteString(errOut, stderr)
		}
		return err
	}
}

// startUntilExited starts against m and returns the ContainerExitedError the
// start must fail with.
func startUntilExited(t *testing.T, m *dockermock.Mock, opts mongod.Options) *mongod.ContainerExitedError {
	t.Helper()
	_, err := start(t, m, opts)
	var exited *mongod.ContainerExitedError
	require.ErrorAs(t, err, &exited, "a container that exits will never be ready")
	return exited
}

func TestAContainerThatExitsReportsWhatItWrote(t *testing.T) {
	m, _ := readyMock(t)
	exitsAfterStarting(t, m)
	m.ContainerLogsToFunc = writing("Error parsing command line: unrecognised option '--notaflag'\n", "", nil)

	exited := startUntilExited(t, m, mongod.DefaultOptions())

	assert.Equal(t, "Error parsing command line: unrecognised option '--notaflag'\n", exited.Output, "the caller sees why mongod stopped without running the image again")
	assert.Equal(t, 2, exited.ExitCode, "along with its exit code")
}

func TestTheOutputOfAnExitedContainerKeepsItsEnd(t *testing.T) {
	const reason = "Fatal assertion: the reason mongod stopped\n"
	earlier := strings.Repeat("an earlier log line\n", 2000)
	cases := map[string]func(context.Context, string, io.Writer, io.Writer) error{
		// A client writes one frame at a time, which is usually one line.
		"a line at a time": func(_ context.Context, _ string, out, _ io.Writer) error {
			for line := range strings.Lines(earlier + reason) {
				_, _ = io.WriteString(out, line)
			}
			return nil
		},
		"in one write": writing(earlier+reason, "", nil),
	}
	for name, logs := range cases {
		t.Run(name, func(t *testing.T) {
			m, _ := readyMock(t)
			exitsAfterStarting(t, m)
			m.ContainerLogsToFunc = logs

			exited := startUntilExited(t, m, mongod.DefaultOptions())

			assert.True(t, strings.HasSuffix(exited.Output, reason), "the reason mongod stopped comes last, so the end of the output is kept")
			assert.True(t, strings.HasPrefix(exited.Output, "an earlier log line\n"), "starting at a whole line")
			assert.LessOrEqual(t, len(exited.Output), documentedOutputLimit, "at most the documented 8 KiB")
		})
	}
}

func TestTheOutputOfAnExitedContainerStartsAtAWholeCharacter(t *testing.T) {
	m, _ := readyMock(t)
	exitsAfterStarting(t, m)
	// One long line of three-byte characters, with no line break to cut at.
	m.ContainerLogsToFunc = writing("x"+strings.Repeat("日", documentedOutputLimit), "", nil)

	exited := startUntilExited(t, m, mongod.DefaultOptions())

	assert.True(t, utf8.ValidString(exited.Output), "a cut output is still valid text")
	assert.LessOrEqual(t, len(exited.Output), documentedOutputLimit, "at most the documented 8 KiB")
}

func TestStderrIsPartOfTheOutput(t *testing.T) {
	m, _ := readyMock(t)
	exitsAfterStarting(t, m)
	m.ContainerLogsToFunc = writing("", "exec: mongod: not found\n", nil)

	exited := startUntilExited(t, m, mongod.DefaultOptions())

	assert.Equal(t, "exec: mongod: not found\n", exited.Output, "a broken image often reports its failure on stderr")
}

func TestAFailedLogReadStillReportsTheExit(t *testing.T) {
	cases := map[string]struct {
		logs func(context.Context, string, io.Writer, io.Writer) error
		want string
	}{
		"nothing read":    {writing("", "", errors.New("the daemon refused")), ""},
		"part of the log": {writing("the start of the log\n", "", errors.New("connection reset")), "the start of the log\n"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m, _ := readyMock(t)
			exitsAfterStarting(t, m)
			m.ContainerLogsToFunc = tc.logs

			exited := startUntilExited(t, m, mongod.DefaultOptions())

			assert.Equal(t, tc.want, exited.Output, "what was read is kept, and a failed read does not hide the exit")
		})
	}
}

func TestARemovedContainerHasNoLogToRead(t *testing.T) {
	m, _ := readyMock(t)
	port := closedPort(t)
	inspects := 0
	m.ContainerInspectFunc = func(context.Context, string) (dockerclient.ContainerInspect, error) {
		if inspects++; inspects == 1 {
			return running(port), nil
		}
		return dockerclient.ContainerInspect{}, dockerclient.ErrNotFound
	}

	exited := startUntilExited(t, m, mongod.DefaultOptions())

	assert.Equal(t, "removed", exited.Status, "the container is gone")
	assert.Empty(t, m.CallsTo("ContainerLogsTo"), "so there is no log to ask for")
}

func TestReadingTheLogEndsWithTheStartTimeout(t *testing.T) {
	m, _ := readyMock(t)
	exitsAfterStarting(t, m)
	m.ContainerLogsToFunc = func(ctx context.Context, _ string, out, _ io.Writer) error {
		select { // a daemon that never finishes sending the log
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
			_, _ = io.WriteString(out, "the read outlived the start timeout")
			return nil
		}
	}
	started := time.Now()

	exited := startUntilExited(t, m, mongod.DefaultOptions().WithStartTimeout(time.Second))

	assert.Empty(t, exited.Output, "nothing was read")
	assert.Less(t, time.Since(started), 10*time.Second, "and the read did not outlast the start timeout")
}

func TestAContainerThatExitsBeforeItsPortIsReadIsReportedAsExited(t *testing.T) {
	m, _ := readyMock(t)
	m.ContainerInspectFunc = func(context.Context, string) (dockerclient.ContainerInspect, error) {
		return exitedWithCode(2), nil
	}
	m.ContainerLogsToFunc = writing("Error parsing command line\n", "", nil)

	exited := startUntilExited(t, m, mongod.DefaultOptions())

	assert.Equal(t, "Error parsing command line\n", exited.Output, "reported as exited, with its reason")
}
