package mongod

import (
	"bytes"
	"context"
	"unicode/utf8"

	"github.com/tophergopher/mongotest/dockerclient"
)

// hasExited reports whether a container's process has ended for good.
func hasExited(state dockerclient.ContainerState) bool {
	return state.Status == "exited" || state.Status == "dead"
}

// newExitedError reports a container that exited before mongod was ready,
// with the end of what it wrote, which usually says why.
func newExitedError(ctx context.Context, docker dockerclient.Client, name, id string, state dockerclient.ContainerState, cause error) *ContainerExitedError {
	return &ContainerExitedError{
		Name: name, ID: id, Status: state.Status,
		ExitCode: state.ExitCode, HasExitCode: true,
		Output: lastOutput(ctx, docker, id), Err: cause,
	}
}

// lastOutput returns the end of what a container wrote, up to
// exitOutputLimit bytes. A read that fails part way still returns what it
// read: the container is about to be removed, so this is the last chance.
func lastOutput(ctx context.Context, docker dockerclient.Client, id string) string {
	end := &tail{limit: exitOutputLimit}
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
