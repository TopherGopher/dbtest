package container

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/tophergopher/mongotest/dockerclient"
)

// ErrExited: the container stopped or was removed before it was ready, so
// waiting longer would not have helped.
var ErrExited = errors.New("the container exited before it was ready")

// ExitOutputLimit is the most bytes of an exited container's output that
// ExitedError keeps, enough for the last several lines of a log.
const ExitOutputLimit = 8 << 10

// ExitedError reports that the container stopped, or was removed, before it
// was ready. It matches ErrExited. Image packages re-export it and ErrExited under
// their own names, so its fields and its message are public API.
type ExitedError struct {
	Name string
	ID   string
	// Status is "exited", "dead", or "removed".
	Status string
	// ExitCode is meaningful only when HasExitCode is true.
	ExitCode    int
	HasExitCode bool
	// Output is the end of what the container wrote to stdout and stderr,
	// at most 8 KiB, starting at a whole line. It usually says why the
	// container stopped. It is empty if the container was removed or its
	// log could not be read.
	Output string
	// Err is what the wait for readiness saw that led to the container
	// being inspected, or nil.
	Err error
}

func (e *ExitedError) Error() string {
	if !e.HasExitCode {
		return fmt.Sprintf("container %s was removed before it was ready: Stop was called, the process was signalled, "+
			"or something else, such as docker rm, removed it", e.where())
	}
	verb := "exited"
	if e.Status == "dead" {
		verb = "died"
	}
	return fmt.Sprintf("container %s %s with code %d before it was ready. %s",
		e.where(), verb, e.ExitCode, e.why())
}

// where names the container, and its id once the daemon has assigned one.
func (e *ExitedError) where() string {
	if e.ID == "" {
		return e.Name
	}
	return e.Name + " (" + ShortID(e.ID) + ")"
}

// why shows the end of what the container wrote, which usually says why it
// stopped, or how to find out when there is nothing to show.
func (e *ExitedError) why() string {
	if e.Output == "" {
		return "Its log was empty or could not be read. To see why it stopped, run the same image by hand with the same options"
	}
	return "The end of what the container wrote:\n" + strings.TrimRight(e.Output, "\n")
}

func (e *ExitedError) Unwrap() error { return e.Err }

func (e *ExitedError) Is(target error) bool { return target == ErrExited }

// HasExited reports whether a container's process has ended for good.
func HasExited(state dockerclient.ContainerState) bool {
	return state.Status == "exited" || state.Status == "dead"
}

// NewExitedError reports a container that exited before it was ready, with
// the end of what it wrote, which usually says why.
func NewExitedError(ctx context.Context, docker dockerclient.Client, name, id string, state dockerclient.ContainerState, cause error) *ExitedError {
	return &ExitedError{
		Name: name, ID: id, Status: state.Status,
		ExitCode: state.ExitCode, HasExitCode: true,
		Output: lastOutput(ctx, docker, id), Err: cause,
	}
}

// ShortID abbreviates a container id for a message, as the docker CLI does.
func ShortID(id string) string {
	const length = 12
	if len(id) <= length {
		return id
	}
	return id[:length]
}

// lastOutput returns the end of what a container wrote, up to
// ExitOutputLimit bytes. A read that fails part way still returns what it
// read: the container is about to be removed, so this is the last chance.
func lastOutput(ctx context.Context, docker dockerclient.Client, id string) string {
	end := &tail{limit: ExitOutputLimit}
	_ = docker.ContainerLogsTo(ctx, id, end, end)
	return end.String()
}

// tail keeps the last limit bytes written to it, so a log of any length can
// stream through it.
type tail struct {
	buf   []byte
	limit int
	cut   bool // something was dropped from the front
}

func (t *tail) Write(p []byte) (int, error) {
	n := len(p)
	if n > t.limit {
		p, t.cut = p[n-t.limit:], true
	}
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.limit; over > 0 {
		t.buf, t.cut = t.buf[over:], true
	}
	return n, nil
}

// String returns what was kept. If the front was cut, it starts at the next
// whole line, or at least at a whole character.
func (t *tail) String() string {
	b := t.buf
	if t.cut {
		b = fromNextLine(b)
	}
	for len(b) > 0 && !utf8.RuneStart(b[0]) {
		b = b[1:]
	}
	return string(b)
}

// fromNextLine drops b's first, partial line, unless it is the only line.
func fromNextLine(b []byte) []byte {
	if i := bytes.IndexByte(b, '\n'); i >= 0 && i < len(b)-1 {
		return b[i+1:]
	}
	return b
}
