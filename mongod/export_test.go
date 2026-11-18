package mongod

import (
	"context"
	"time"
)

// This file is compiled only into the tests. It lets a test outside the
// package assert on a private error or decision, where it would otherwise
// have to read the wording of a message.

// Why a hello did not find mongod ready.
var (
	ErrNoReply      = errNoReply
	ErrNotMongod    = errNotMongod
	ErrHelloRefused = errHelloRefused
)

// AddressInDoubt reports whether the error advises correcting the address.
func (e *NotReadyError) AddressInDoubt() bool { return e.addressInDoubt() }

// PullWaiters reports how many callers are waiting on the pull of image from
// the daemon at host, so a test can tell when every caller has joined it.
func PullWaiters(host, image string) int { return pullWaiters(host, image) }

// SetPullWait replaces the wait between pull attempts until the test ends,
// so a test of the retry does not sleep for real.
func SetPullWait(t interface{ Cleanup(func()) }, wait func(ctx context.Context, d time.Duration) error) {
	saved := pullWait
	pullWait = wait
	t.Cleanup(func() { pullWait = saved })
}

// SetRateLimitMemory sets how long a spent rate limit is remembered, for the
// rest of the test. When the test ends it restores the default and forgets
// every spent limit, so none carries into the next test.
func SetRateLimitMemory(t interface{ Cleanup(func()) }, d time.Duration) {
	saved := rateLimitMemory
	rateLimitMemory = d
	t.Cleanup(func() {
		pulls.Lock()
		defer pulls.Unlock()
		rateLimitMemory = saved
		clear(pulls.spent)
	})
}

// ExitOutputLimit is how much of an exited container's output is kept.
const ExitOutputLimit = exitOutputLimit
