// Package pull fetches images for any image package. Callers in one process
// that need the same image from the same daemon share one pull, a pull that
// fails for a reason that may pass is tried again, and a rate limit that
// refused every attempt is remembered for a while.
package pull

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tophergopher/mongotest/dockerclient"
)

// pullKey identifies a pull: the daemon's address and the image reference.
type pullKey struct{ host, image string }

// sharedPull is one pull in progress.
type sharedPull struct {
	cancel   context.CancelFunc
	done     chan struct{} // closed once err and timedOut are set
	err      error
	timedOut bool          // set when the starting caller's pull timeout ended the pull
	timeout  time.Duration // that pull timeout
	waiters  int           // guarded by pulls
	last     error         // the latest attempt's failure, guarded by pulls
	// pause waits between attempts. It is read once, when the pull starts,
	// so a test that replaces pullWait cannot race a pull still running.
	pause func(ctx context.Context, d time.Duration) error
}

// pulls holds the pulls in progress. A pull is removed when it ends or when
// its last waiter leaves, so no caller joins a pull that has failed or that
// nobody wants.
var pulls = struct {
	sync.Mutex
	inProgress map[pullKey]*sharedPull
	// spent holds, for each image, the failure of a pull whose every attempt
	// a rate limit refused, and how long to keep returning it.
	spent map[pullKey]*spentLimitError
}{inProgress: make(map[pullKey]*sharedPull), spent: make(map[pullKey]*spentLimitError)}

// rateLimitMemory is how long a pull fails at once after a rate limit refused
// every attempt of an earlier one. Tests change it.
var rateLimitMemory = 5 * time.Minute

// Shared pulls image, or joins a pull of it from the same daemon that is
// already in progress, and waits for it within ctx and timeout. A pull the
// timeout cut short returns a *TimeoutError.
func Shared(ctx context.Context, docker dockerclient.Client, image string, timeout time.Duration) error {
	key := pullKey{host: docker.Host(), image: image}
	p := join(ctx, docker, key, timeout)
	defer leave(key, p)
	return p.wait(ctx, timeout)
}

// TimeoutError is a pull that the pull timeout cut short. It matches
// context.DeadlineExceeded and the daemon's own error.
type TimeoutError struct {
	Timeout time.Duration
	Err     error
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("the pull did not finish within %s (%v)", e.Timeout, e.Err)
}

func (e *TimeoutError) Unwrap() []error { return []error{context.DeadlineExceeded, e.Err} }

// join returns the pull in progress for key, starting one if there is none,
// and counts the caller as waiting on it.
func join(ctx context.Context, docker dockerclient.Client, key pullKey, timeout time.Duration) *sharedPull {
	pulls.Lock()
	defer pulls.Unlock()
	p := pulls.inProgress[key]
	if p == nil {
		p = startUnlessSpentLocked(ctx, docker, key, timeout)
	}
	p.waiters++
	return p
}

// startUnlessSpentLocked starts a pull. If a rate limit refused every attempt
// of a recent pull, it returns that failure instead, without asking the
// registry. The caller holds the lock.
func startUnlessSpentLocked(ctx context.Context, docker dockerclient.Client, key pullKey, timeout time.Duration) *sharedPull {
	if spent := pulls.spent[key]; spent != nil && time.Now().Before(spent.until) {
		return endedPull(spent)
	}
	delete(pulls.spent, key)
	p := startPull(ctx, docker, key, timeout)
	pulls.inProgress[key] = p
	return p
}

// endedPull is a pull that has already failed with err.
func endedPull(err error) *sharedPull {
	p := &sharedPull{cancel: func() {}, done: make(chan struct{}), err: err}
	close(p.done)
	return p
}

// startPull starts a pull bounded by the caller's pull timeout but not by its
// context, so the caller can leave without failing others waiting on it.
func startPull(ctx context.Context, docker dockerclient.Client, key pullKey, timeout time.Duration) *sharedPull {
	bounded, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	p := &sharedPull{cancel: cancel, done: make(chan struct{}), timeout: timeout, pause: pullWait}
	go func() {
		defer cancel()
		p.err = p.pullWithRetries(bounded, docker, key.image)
		p.timedOut = p.err != nil && errors.Is(bounded.Err(), context.DeadlineExceeded)
		// A rate limit counts as spent only if it refused every attempt: a
		// pull its timeout cut short does not show the limit is still in
		// force.
		finish(key, p, errors.Is(p.err, dockerclient.ErrRateLimited) && bounded.Err() == nil)
		close(p.done)
	}()
	return p
}

// leave stops counting a caller as waiting. The last to leave cancels the
// pull and forgets it, under the same lock join takes, so no caller joins a
// pull that is being cancelled.
func leave(key pullKey, p *sharedPull) {
	pulls.Lock()
	defer pulls.Unlock()
	if p.waiters--; p.waiters > 0 {
		return
	}
	forgetLocked(key, p)
	p.cancel()
}

// finish removes p from the pulls in progress, and remembers its failure if
// a rate limit refused every attempt.
func finish(key pullKey, p *sharedPull, limitSpent bool) {
	pulls.Lock()
	defer pulls.Unlock()
	forgetLocked(key, p)
	if limitSpent {
		pulls.spent[key] = &spentLimitError{err: p.err, until: time.Now().Add(rateLimitMemory)}
	}
}

// spentLimitError is returned instead of pulling while a spent rate limit is
// remembered. It wraps the earlier pull's error, so it matches
// ErrRateLimited.
type spentLimitError struct {
	err   error
	until time.Time
}

func (e *spentLimitError) Error() string {
	return fmt.Sprintf("%v; the rate limit refused every attempt of an earlier pull of this image, "+
		"so it is not pulled again until %s. Try again after then", e.err, e.until.Format(time.TimeOnly))
}

func (e *spentLimitError) Unwrap() error { return e.err }

// forgetLocked removes p from the pulls in progress, unless a newer pull has
// already taken its place. The caller holds the lock.
func forgetLocked(key pullKey, p *sharedPull) {
	if pulls.inProgress[key] == p {
		delete(pulls.inProgress, key)
	}
}

// wait returns the pull's result, or this caller's own error if its context
// or its pull timeout ends first. If a pull timeout ended it, rather than a
// context, the error is a *TimeoutError. A caller that joined a pull
// waits no longer than the pull timeout of the caller that started it.
func (p *sharedPull) wait(ctx context.Context, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-p.done:
		if p.timedOut {
			return &TimeoutError{Timeout: p.timeout, Err: p.err}
		}
		return p.err
	case <-ctx.Done():
		return withCause(ctx.Err(), p.lastFailure())
	case <-timer.C:
		return &TimeoutError{Timeout: timeout, Err: withCause(context.DeadlineExceeded, p.lastFailure())}
	}
}

// lastFailure returns the latest attempt's failure, or nil, so a caller that
// leaves during a wait still learns why the pull failed.
func (p *sharedPull) lastFailure() error {
	pulls.Lock()
	defer pulls.Unlock()
	return p.last
}

// withCause adds the last attempt's failure, if there was one, to the error
// that ended the wait. Both match with errors.Is.
func withCause(ended, last error) error {
	if last == nil {
		return ended
	}
	return fmt.Errorf("%w; the last attempt failed: %w", ended, last)
}

// The daemon already retries a failed request a few times within a second
// or two. These waits are longer, to outlast a registry that is briefly down,
// or a rate limit that a burst of pulls set off, and stop after about a
// minute.
const (
	pullAttempts  = 6
	firstPullWait = 2 * time.Second
	maxPullWait   = 30 * time.Second
)

// pullWait waits d, or until ctx ends. Tests replace it.
var pullWait = func(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// pullWithRetries pulls image, trying again after a temporary failure. When
// ctx ends during a wait, the error matches both the context's error and the
// last attempt's failure.
func (p *sharedPull) pullWithRetries(ctx context.Context, docker dockerclient.Client, image string) error {
	var err error
	for attempt := range pullAttempts {
		if attempt > 0 {
			if waitErr := p.pause(ctx, backoff(attempt)); waitErr != nil {
				return withCause(waitErr, err)
			}
		}
		if err = docker.ImagePull(ctx, image); !temporary(err) {
			return err
		}
		pulls.Lock()
		p.last = err
		pulls.Unlock()
	}
	return err
}

// backoff returns how long to wait before the given attempt: a random length
// up to a ceiling that doubles with each attempt, so that many processes do
// not retry at the same moment.
func backoff(attempt int) time.Duration {
	ceiling := min(firstPullWait<<(attempt-1), maxPullWait)
	return rand.N(ceiling)
}

// temporaryFailures are the parts of a daemon's message that say a pull may
// work if tried again: a registry that answered with a 5xx, and a download
// that was cut off.
var temporaryFailures = []string{
	"500 internal server error", "502 bad gateway", "503 service unavailable", "504 gateway timeout",
	"unexpected eof", "connection reset by peer", "i/o timeout", "tls handshake timeout",
}

// temporary reports whether a failed pull may work if tried again: a rate
// limit, or a registry failure that may pass. For registry failures only the
// daemon's text tells: it reports them as a 500 or inside the pull's stream,
// alongside failures no retry can fix, such as "pull access denied".
func temporary(err error) bool {
	if errors.Is(err, dockerclient.ErrRateLimited) {
		return true
	}
	msg := strings.ToLower(failureMessage(err))
	for _, mark := range temporaryFailures {
		if strings.Contains(msg, mark) {
			return true
		}
	}
	return false
}

// failureMessage returns the daemon's message for a pull that failed in the
// registry: a 5xx answer, or a failure inside the pull's stream. Anything
// else has no message worth reading.
func failureMessage(err error) string {
	var status *dockerclient.StatusError
	if errors.As(err, &status) && status.StatusCode >= http.StatusInternalServerError {
		return status.Message
	}
	var pull *dockerclient.PullError
	if errors.As(err, &pull) {
		return pull.Message
	}
	return ""
}
