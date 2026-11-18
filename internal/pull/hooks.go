package pull

import (
	"context"
	"time"
)

// These let tests, here and in the image packages, observe and steer pulls
// without sleeping or reading messages.

// Waiters reports how many callers are waiting on the pull of image from the
// daemon at host, so a test can tell when every caller has joined it.
func Waiters(host, image string) int {
	pulls.Lock()
	defer pulls.Unlock()
	if p := pulls.inProgress[pullKey{host: host, image: image}]; p != nil {
		return p.waiters
	}
	return 0
}

// SetWait replaces the wait between pull attempts until the test ends, so a
// test of the retry does not sleep for real.
func SetWait(t interface{ Cleanup(func()) }, wait func(ctx context.Context, d time.Duration) error) {
	saved := pullWait
	pullWait = wait
	t.Cleanup(func() { pullWait = saved })
}

// ForgetSpentLimits forgets every spent rate limit, so none carries into the
// next test.
func ForgetSpentLimits() {
	pulls.Lock()
	defer pulls.Unlock()
	clear(pulls.spent)
}

// SetRateLimitMemory sets how long a spent rate limit is remembered, for the
// rest of the test. When the test ends it restores the default and forgets
// every spent limit, so none carries into the next test.
func SetRateLimitMemory(t interface{ Cleanup(func()) }, d time.Duration) {
	pulls.Lock()
	saved := rateLimitMemory
	rateLimitMemory = d
	pulls.Unlock()
	t.Cleanup(func() {
		pulls.Lock()
		defer pulls.Unlock()
		rateLimitMemory = saved
		clear(pulls.spent)
	})
}
