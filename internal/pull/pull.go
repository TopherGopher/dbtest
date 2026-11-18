// Package pull fetches images for any image package. Callers in one process
// that need the same image from the same daemon share one pull, a pull that
// fails for a reason that may pass is tried again, and a pull Docker Hub's
// rate limit refuses comes from a mirror instead, where there is one.
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
	done     chan struct{} // closed once ref, err and timedOut are set
	ref      string        // the reference the image was pulled as
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
	// spent holds, for each image, a rate limit that refused it, and how
	// long to keep acting on that.
	spent map[pullKey]*spentLimitError
}{inProgress: make(map[pullKey]*sharedPull), spent: make(map[pullKey]*spentLimitError)}

// rateLimitMemory is how long a refusal by a rate limit is acted on: a pull
// goes straight to the mirror, or fails at once if there is none or it
// failed too. Tests change it.
var rateLimitMemory = 5 * time.Minute

// Shared pulls image, or joins a pull of it from the same daemon that is
// already in progress, and waits for it within ctx and timeout. If Docker
// Hub's rate limit refuses it, the image comes from the first of mirrors
// that has it. Shared returns the reference the image was pulled as, which
// is the mirror's when it came from there. A pull the timeout cut short
// returns a *TimeoutError.
func Shared(ctx context.Context, docker dockerclient.Client, image string, timeout time.Duration, mirrors ...Mirror) (string, error) {
	key := pullKey{host: docker.Host(), image: image}
	p := join(ctx, docker, key, timeout, firstMirror(mirrors, image))
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
func join(ctx context.Context, docker dockerclient.Client, key pullKey, timeout time.Duration, mirror string) *sharedPull {
	pulls.Lock()
	defer pulls.Unlock()
	p := pulls.inProgress[key]
	if p == nil {
		p = startUnlessSpentLocked(ctx, docker, key, timeout, mirror)
	}
	p.waiters++
	return p
}

// startUnlessSpentLocked starts a pull. While a refusal by a rate limit is
// remembered, the pull goes straight to the mirror, or, with no mirror or
// one that failed too, ends with the earlier failure without asking any
// registry. The caller holds the lock.
func startUnlessSpentLocked(ctx context.Context, docker dockerclient.Client, key pullKey, timeout time.Duration, mirror string) *sharedPull {
	r := route{image: key.image, mirror: mirror}
	if spent := pulls.spent[key]; spent != nil && time.Now().Before(spent.until) {
		if !spent.leavesMirror(mirror) {
			return endedPull(spent)
		}
		r.limited = spent.err
	} else {
		delete(pulls.spent, key)
	}
	p := startPull(ctx, docker, key, timeout, r)
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
func startPull(ctx context.Context, docker dockerclient.Client, key pullKey, timeout time.Duration, r route) *sharedPull {
	bounded, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	p := &sharedPull{cancel: cancel, done: make(chan struct{}), timeout: timeout, pause: pullWait}
	go func() {
		defer cancel()
		got := p.fetch(bounded, docker, r)
		p.ref, p.err = got.ref, got.err
		p.timedOut = p.err != nil && errors.Is(bounded.Err(), context.DeadlineExceeded)
		finish(key, p, got.spentLimit(bounded.Err() == nil))
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

// finish removes p from the pulls in progress, and remembers a refusal by a
// rate limit, if there was one.
func finish(key pullKey, p *sharedPull, spent *spentLimitError) {
	pulls.Lock()
	defer pulls.Unlock()
	forgetLocked(key, p)
	if spent != nil {
		spent.until = time.Now().Add(rateLimitMemory)
		pulls.spent[key] = spent
	}
}

// spentLimitError is a refusal by a rate limit that is still remembered. It
// is returned instead of pulling when there is nowhere else to go, and
// wraps the earlier pull's error, so it matches ErrRateLimited.
type spentLimitError struct {
	err   error
	until time.Time
	// mirrorFailed says the mirror failed after the refusal, so a pull
	// does not go there either.
	mirrorFailed bool
}

func (e *spentLimitError) Error() string {
	return fmt.Sprintf("%v; the rate limit refused an earlier pull of this image, "+
		"so it is not pulled again until %s. Try again after then", e.err, e.until.Format(time.TimeOnly))
}

func (e *spentLimitError) Unwrap() error { return e.err }

// leavesMirror reports whether a pull may still go to mirror.
func (e *spentLimitError) leavesMirror(mirror string) bool {
	return mirror != "" && !e.mirrorFailed
}

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

// route is where a pull goes: the image itself, unless a rate limit has
// already refused it, and then the mirror, if there is one.
type route struct {
	image  string
	mirror string // "" when no registry mirrors the image
	// limited is a remembered refusal, which sends the pull straight to
	// the mirror.
	limited error
}

// fetched is how a pull ended.
type fetched struct {
	ref string
	err error
	// limited is the rate limit's refusal, if one sent the pull to the
	// mirror; mirrored says the mirror was then tried.
	limited  error
	mirrored bool
}

// fetch pulls the image, and pulls the mirror's copy instead if a rate
// limit refuses it. A rate limit lasts hours, so it is not waited out when
// there is a mirror; any other failure that may pass is tried again where
// it happened.
func (p *sharedPull) fetch(ctx context.Context, docker dockerclient.Client, r route) fetched {
	limited := r.limited
	if limited == nil {
		err := p.pullWithRetries(ctx, docker, r.image, r.mirror != "")
		if !mirrorFor(err, r.mirror) {
			return fetched{ref: r.image, err: err}
		}
		limited = err
	}
	if err := p.pullWithRetries(ctx, docker, r.mirror, false); err != nil {
		failed := &mirrorError{image: r.image, mirror: r.mirror, limited: limited, mirrored: err}
		return fetched{err: failed, limited: limited, mirrored: true}
	}
	return fetched{ref: r.mirror, limited: limited}
}

// mirrorFor reports whether a failed pull should go to mirror: only a rate
// limit's refusal does, and only when there is a mirror.
func mirrorFor(err error, mirror string) bool {
	return mirror != "" && errors.Is(err, dockerclient.ErrRateLimited)
}

// spentLimit returns what to remember of the pull, or nil. complete says no
// timeout cut the pull short. A refusal that sent the pull to a mirror is
// remembered, and so is the mirror's failure, when it ran its course. With
// no mirror, a rate limit is remembered only if it refused every attempt,
// which a pull cut short does not show.
func (f fetched) spentLimit(complete bool) *spentLimitError {
	if f.limited != nil {
		return &spentLimitError{err: f.rememberedErr(complete), mirrorFailed: f.mirrored && complete}
	}
	if complete && errors.Is(f.err, dockerclient.ErrRateLimited) {
		return &spentLimitError{err: f.err}
	}
	return nil
}

// rememberedErr is the error a later pull reports while the refusal is
// remembered: the mirror's failure too, if the mirror ran its course.
func (f fetched) rememberedErr(complete bool) error {
	if f.mirrored && complete {
		return f.err
	}
	return f.limited
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
		if err = docker.ImagePull(ctx, image); !worthRetrying(err, stopAtLimit) {
			return err
		}
		pulls.Lock()
		p.last = err
		pulls.Unlock()
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
