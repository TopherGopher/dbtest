package reaper

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
)

// A process that ignores every signal the reaper would listen for must be
// left ignoring them: listening would turn a signal it was meant to survive
// into a reap and an exit. ignored_test.go proves that against a real
// process; this covers the state the package is left in.
func TestRegisteringWhileEverySignalIsIgnoredInstallsNothing(t *testing.T) {
	resetForTest(t)
	signal.Ignore(syscall.SIGINT, syscall.SIGTERM)
	t.Cleanup(stopIgnoring)

	Register("container", func(context.Context) error { return nil })

	assert.False(t, Installed(), "there is nothing to listen for, so no handler is claimed")
	assert.True(t, signal.Ignored(syscall.SIGINT), "and the signal is still ignored")
	assert.Equal(t, []string{"container"}, Names(), "while the container is still registered for an explicit Reap")
}

// stopIgnoring puts SIGINT and SIGTERM back as a process normally has them.
// Listening and then stopping is what does it: signal.Reset alone leaves the
// runtime reporting them as ignored, and every later test would install
// nothing.
func stopIgnoring() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	signal.Stop(ch)
}
