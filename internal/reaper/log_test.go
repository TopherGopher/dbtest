package reaper

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// record is one call to a recordingLogger.
type record struct {
	msg string
	kv  map[string]any
}

// recordingLogger keeps what it is asked to log.
type recordingLogger struct {
	mu      sync.Mutex
	records []record
}

func (l *recordingLogger) Error(msg string, keysAndValues ...any) {
	kv := map[string]any{}
	for i := 0; i+1 < len(keysAndValues); i += 2 {
		kv[keysAndValues[i].(string)] = keysAndValues[i+1]
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = append(l.records, record{msg: msg, kv: kv})
}

func (l *recordingLogger) all() []record {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]record(nil), l.records...)
}

// panickingLogger stands for a logger whose output has gone, as one built
// on a finished test's t.Output() panics.
type panickingLogger struct{}

func (panickingLogger) Error(string, ...any) { panic("the test this logger wrote to has ended") }

func TestASignalsReapLogsEachContainerItLeavesBehind(t *testing.T) {
	resetForTest(t)
	refused := errors.New("the daemon refused")
	failedLog, stuckLog, removedLog := &recordingLogger{}, &recordingLogger{}, &recordingLogger{}
	Register("mongotest-1a2b3c4d", func(context.Context) error { return refused }, failedLog)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	Register("mongotest-5e6f7a8b", func(context.Context) error { <-release; return nil }, stuckLog)
	Register("mongotest-9c0d1e2f", func(context.Context) error { return nil }, removedLog)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	var stderr bytes.Buffer

	reapAndReport(ctx, syscall.SIGTERM, &stderr)

	assert.NotZero(t, stderr.Len(), "stderr still gets the report, since a logger in a dying process may never be flushed")
	failed := failedLog.all()
	require.Len(t, failed, 1, "a container whose removal failed is logged once, to the logger of the start that made it")
	assert.Equal(t, "mongotest-1a2b3c4d", failed[0].kv["container"], "naming the container")
	assert.Equal(t, syscall.SIGTERM, failed[0].kv["signal"], "and the signal that stopped the process")
	assert.ErrorIs(t, failed[0].kv["error"].(error), refused, "and why it was not removed")
	stuck := stuckLog.all()
	require.Len(t, stuck, 1, "a container whose removal did not finish is logged too")
	assert.Equal(t, "mongotest-5e6f7a8b", stuck[0].kv["container"], "naming the container")
	assert.Empty(t, removedLog.all(), "a container that was removed is not reported")
}

func TestALoggerThatPanicsDoesNotStopTheReport(t *testing.T) {
	resetForTest(t)
	Register("mongotest-1a2b3c4d", func(context.Context) error { return errors.New("the daemon refused") }, panickingLogger{})
	other := &recordingLogger{}
	Register("mongotest-5e6f7a8b", func(context.Context) error { return errors.New("the daemon refused") }, other)
	var stderr bytes.Buffer

	assert.NotPanics(t, func() { reapAndReport(context.Background(), syscall.SIGINT, &stderr) },
		"the process still has to re-raise the signal and die of it")
	assert.NotZero(t, stderr.Len(), "stderr gets the report")
	assert.Len(t, other.all(), 1, "and the other container's logger still gets its record")
}
