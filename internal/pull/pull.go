// Package pull fetches images for any image package. Callers in one process
// that need the same image from the same daemon share one pull, a pull that
// fails for a reason that may pass is tried again, and a pull that one
// registry cannot serve moves on to the image's mirrors, in order.
package pull

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tophergopher/dbtest/dockerclient"
)

// pullKey identifies a pull: the daemon's address and the image reference.
type pullKey struct{ host, image string }

// sharedPull is one pull in progress.
type sharedPull struct {
	cancel   context.CancelFunc
	done     chan struct{} // closed once ref, err and timedOut are set
	ref      string        // the reference the image was pulled as
	err      error
	timedOut bool          // set when the starting caller's pull timeout ended the pull
	timeout  time.Duration // that pull timeout
	waiters  int           // guarded by pulls
	image    string        // the reference asked for
	sources  []string      // where to pull it from, in order
	// failures are the sources that have failed so far, current the one
	// being pulled and last its latest failure, all guarded by pulls. A
	// caller that leaves during the pull is told of them.
	failures []SourceFailure
	current  string
	last     error
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
	// spent holds, for each source of an image, a rate limit that refused
	// it, and how long to keep acting on that.
	spent map[pullKey]*spentLimitError
}{inProgress: make(map[pullKey]*sharedPull), spent: make(map[pullKey]*spentLimitError)}

// rateLimitMemory is how long a refusal by a rate limit is acted on: a pull
// skips that source, and fails at once if it has no other. Tests change it.
var rateLimitMemory = 5 * time.Minute

// Shared pulls image, or joins a pull of it from the same daemon that is
// already in progress, and waits for it within ctx and timeout. If image's
// registry cannot serve it, it comes from the name each of mirrors gives
// it, in turn; a copy already on the daemon by that name is used as it is.
// Shared returns the reference the image is on the daemon as. A pull the
// timeout cut short returns a *TimeoutError, and one that failed at more
// than one source a *SourcesError.
func Shared(ctx context.Context, docker dockerclient.Client, image string, timeout time.Duration, mirrors ...Mirror) (string, error) {
	key := pullKey{host: docker.Host(), image: image}
	p := join(ctx, docker, key, timeout, sources(image, mirrors))
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
func join(ctx context.Context, docker dockerclient.Client, key pullKey, timeout time.Duration, from []string) *sharedPull {
	pulls.Lock()
	defer pulls.Unlock()
	p := pulls.inProgress[key]
	if p == nil {
		p = startPull(ctx, docker, key, timeout, from)
		pulls.inProgress[key] = p
	}
	p.waiters++
	return p
}

// startPull starts a pull bounded by the caller's pull timeout but not by its
// context, so the caller can leave without failing others waiting on it.
func startPull(ctx context.Context, docker dockerclient.Client, key pullKey, timeout time.Duration, from []string) *sharedPull {
	bounded, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	p := &sharedPull{cancel: cancel, done: make(chan struct{}), timeout: timeout, pause: pullWait, image: key.image, sources: from}
	go func() {
		defer cancel()
		p.ref, p.err = p.fetch(bounded, docker, key.host)
		p.timedOut = p.err != nil && errors.Is(bounded.Err(), context.DeadlineExceeded)
		finish(key, p)
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

// finish removes p from the pulls in progress.
func finish(key pullKey, p *sharedPull) {
	pulls.Lock()
	defer pulls.Unlock()
	forgetLocked(key, p)
}

// remember records that a rate limit refused source, so later pulls skip it
// until the refusal is forgotten.
func remember(key pullKey, refusal error) {
	pulls.Lock()
	defer pulls.Unlock()
	pulls.spent[key] = &spentLimitError{err: refusal, until: time.Now().Add(rateLimitMemory)}
}

// spentFor returns the remembered refusal of key, or nil once there is none
// or it has been forgotten.
func spentFor(key pullKey) *spentLimitError {
	pulls.Lock()
	defer pulls.Unlock()
	spent := pulls.spent[key]
	if spent != nil && time.Now().Before(spent.until) {
		return spent
	}
	delete(pulls.spent, key)
	return nil
}

// spentLimitError is a refusal by a rate limit that is still remembered. It
// stands for a source that is skipped without being asked, and wraps the
// earlier pull's error, so it matches ErrRateLimited.
type spentLimitError struct {
	err   error
	until time.Time
}

func (e *spentLimitError) Error() string {
	return fmt.Sprintf("%v; the rate limit refused an earlier pull of this image, "+
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
// context, the error is a *TimeoutError. A caller that joined a pull waits
// no longer than the pull timeout of the caller that started it.
func (p *sharedPull) wait(ctx context.Context, timeout time.Duration) (string, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-p.done:
		if p.timedOut {
			return "", &TimeoutError{Timeout: p.timeout, Err: p.err}
		}
		return p.ref, p.err
	case <-ctx.Done():
		return "", withCause(ctx.Err(), p.lastFailure())
	case <-timer.C:
		return "", &TimeoutError{Timeout: timeout, Err: withCause(context.DeadlineExceeded, p.lastFailure())}
	}
}

// lastFailure returns why the pull has not succeeded so far, or nil, so a
// caller that leaves during a wait still learns why it failed: every source
// that has failed, and the latest failure of the one being pulled.
func (p *sharedPull) lastFailure() error {
	pulls.Lock()
	defer pulls.Unlock()
	failures := slices.Clone(p.failures)
	if p.last != nil {
		failures = append(failures, SourceFailure{Ref: p.current, Err: p.last})
	}
	return failedAt(p.image, failures)
}

// begin records that source is now being pulled.
func (p *sharedPull) begin(source string) {
	pulls.Lock()
	defer pulls.Unlock()
	p.current, p.last = source, nil
}

// fail records that source could not provide the image.
func (p *sharedPull) fail(source string, err error) {
	pulls.Lock()
	defer pulls.Unlock()
	p.failures = append(p.failures, SourceFailure{Ref: source, Err: err})
	p.current, p.last = "", nil
}

// failed returns the error for every source that has failed.
func (p *sharedPull) failed() error {
	pulls.Lock()
	defer pulls.Unlock()
	return failedAt(p.image, slices.Clone(p.failures))
}

// withCause adds the last attempt's failure, if there was one, to the error
// that ended the wait. Both match with errors.Is.
func withCause(ended, last error) error {
	if last == nil {
		return ended
	}
	return fmt.Errorf("%w; the last attempt failed: %w", ended, last)
}

// fetch pulls the image from each source in turn until one provides it, and
// returns the reference it is on the daemon as. A source whose rate limit
// refused an earlier pull is skipped while that is remembered, and a
// mirror's copy already on the daemon is used as it is. A rate limit lasts
// hours, so it is not waited out while another source remains; any other
// failure that may pass is tried again at that source first.
func (p *sharedPull) fetch(ctx context.Context, docker dockerclient.Client, host string) (string, error) {
	for i, source := range p.sources {
		if i > 0 && onDaemon(ctx, docker, source) {
			return source, nil
		}
		key := pullKey{host: host, image: source}
		if spent := spentFor(key); spent != nil {
			p.fail(source, spent)
			continue
		}
		another := i < len(p.sources)-1
		p.begin(source)
		err := p.pullWithRetries(ctx, docker, source, another)
		if err == nil {
			return source, nil
		}
		p.fail(source, err)
		if refusedForLong(ctx, err, another) {
			remember(key, err)
		}
		if !fallsThrough(ctx, err, i == 0) {
			break
		}
	}
	return "", p.failed()
}

// onDaemon reports whether the daemon has the image ref names.
func onDaemon(ctx context.Context, docker dockerclient.Client, ref string) bool {
	_, err := docker.ImageInspect(ctx, ref)
	return err == nil
}

// refusedForLong reports whether a source's rate limit refused a pull in a
// way worth remembering: at once, when another source remains, or on every
// attempt, which a pull cut short does not show.
func refusedForLong(ctx context.Context, err error, another bool) bool {
	if !errors.Is(err, dockerclient.ErrRateLimited) {
		return false
	}
	return another || ctx.Err() == nil
}

// fallsThrough reports whether a failure at one source sends the pull to the
// next. It does not when the daemon is gone or the pull's time is up, since
// no source can help then, nor when the image's own registry, which the
// mirrors copy, refuses it for good, such as a missing or private image.
func fallsThrough(ctx context.Context, err error, ownRegistry bool) bool {
	switch {
	case ctx.Err() != nil, errors.Is(err, dockerclient.ErrConnectionFailed):
		return false
	case errors.Is(err, dockerclient.ErrRateLimited), temporary(err):
		return true
	}
	return !ownRegistry
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

// pullWithRetries pulls image, trying again after a temporary failure. With
// stopAtLimit, a rate limit's refusal is returned at once, for the caller to
// go elsewhere. When ctx ends during a wait, the error matches both the
// context's error and the last attempt's failure.
func (p *sharedPull) pullWithRetries(ctx context.Context, docker dockerclient.Client, image string, stopAtLimit bool) error {
	var err error
	for attempt := range pullAttempts {
		if attempt > 0 {
			if waitErr := p.pause(ctx, backoff(attempt)); waitErr != nil {
				return withCause(waitErr, err)
			}
		}
		err = docker.ImagePull(ctx, image)
		if err != nil {
			pulls.Lock()
			p.last = err
			pulls.Unlock()
		}
		if !worthRetrying(err, stopAtLimit) {
			return err
		}
	}
	return err
}

// worthRetrying reports whether to try a failed pull again where it failed.
func worthRetrying(err error, stopAtLimit bool) bool {
	if stopAtLimit && errors.Is(err, dockerclient.ErrRateLimited) {
		return false
	}
	return temporary(err)
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
