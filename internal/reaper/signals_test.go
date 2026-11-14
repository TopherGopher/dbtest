package reaper

import (
	"context"
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
	t.Cleanup(func() { signal.Reset(syscall.SIGINT, syscall.SIGTERM) })

	Register("container", func(context.Context) error { return nil })

	assert.False(t, Installed(), "there is nothing to listen for, so no handler is claimed")
	assert.True(t, signal.Ignored(syscall.SIGINT), "and the signal is still ignored")
	assert.Equal(t, []string{"container"}, Names(), "while the container is still registered for an explicit Reap")
}
