package mongod

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

// Callers in one process that need the same image from the same daemon share
// one pull, so parallel tests on a fresh machine ask the registry once rather
// than once each.

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
}{inProgress: make(map[pullKey]*sharedPull)}

// pullShared pulls image, or joins a pull of it from the same daemon that is
// already in progress, and waits for it within ctx and timeout. inStart says
// the pull is part of Start rather than PullImage.
func pullShared(ctx context.Context, docker dockerclient.Client, image string, timeout time.Duration, inStart bool) error {
	key := pullKey{host: docker.Host(), image: image}
	p := join(ctx, docker, key, timeout)
	defer leave(key, p)
	return adviseOnRateLimit(image, p.wait(ctx, timeout, inStart))
}

// adviseOnRateLimit adds a way round Docker Hub's rate limit to a pull of
// the official image that hit it, since ImagePublicECR mirrors that image.
func adviseOnRateLimit(image string, err error) error {
	if errors.Is(err, dockerclient.ErrRateLimited) && isOfficialOnDockerHub(image) {
		return &publicECRAdvice{err: err}
	}
	return err
}

// isOfficialOnDockerHub reports whether ref names the official image on
// Docker Hub, at any tag or digest.
func isOfficialOnDockerHub(ref string) bool {
	rest, ok := strings.CutPrefix(ref, registryDockerHub+"/"+repositoryOfficial)
	return ok && rest != "" && strings.ContainsRune(":@", rune(rest[0]))
}

// publicECRAdvice is a rate-limited pull of the official image. It matches
// the daemon's error, and so ErrRateLimited.
type publicECRAdvice struct{ err error }

func (e *publicECRAdvice) Error() string {
	return e.err.Error() + ". To avoid Docker Hub's limit, pull the same image from Amazon ECR Public: " +
		"mongod.DefaultOptions().WithMongoImage(mongod.ImagePublicECR)"
}

func (e *publicECRAdvice) Unwrap() error { return e.err }

// join returns the pull in progress for key, starting one if there is none,
// and counts the caller as waiting on it.
func join(ctx context.Context, docker dockerclient.Client, key pullKey, timeout time.Duration) *sharedPull {
	pulls.Lock()
	defer pulls.Unlock()
	p := pulls.inProgress[key]
	if p == nil {
		p = startPull(ctx, docker, key, timeout)
		pulls.inProgress[key] = p
	}
	p.waiters++
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
		forget(key, p)
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

// forget removes p from the pulls in progress, unless a newer pull has
// already taken its place.
func forget(key pullKey, p *sharedPull) {
	pulls.Lock()
	defer pulls.Unlock()
	forgetLocked(key, p)
}

// forgetLocked is forget for a caller that holds the lock.
func forgetLocked(key pullKey, p *sharedPull) {
	if pulls.inProgress[key] == p {
		delete(pulls.inProgress, key)
	}
}

// wait returns the pull's result, or this caller's own error if its context
// or its pull timeout ends first. If a pull timeout ended it, rather than a
// context, the error says how to allow longer. A caller that joined a pull
// waits no longer than the pull timeout of the caller that started it.
func (p *sharedPull) wait(ctx context.Context, timeout time.Duration, inStart bool) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-p.done:
		if p.timedOut {
			return &pullTimeoutError{timeout: p.timeout, err: p.err, inStart: inStart}
		}
		return p.err
	case <-ctx.Done():
		return withCause(ctx.Err(), p.lastFailure())
	case <-timer.C:
		return &pullTimeoutError{timeout: timeout, err: withCause(context.DeadlineExceeded, p.lastFailure()), inStart: inStart}
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

// The daemon already retries a failed request once or twice within a second
// or two. These waits are longer, to outlast a registry that is briefly down,
// and stop after about a minute.
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

// temporary reports whether a failed pull may work if tried again. Only the
// daemon's text tells: it reports a registry's failure as a 500 or inside
// the pull's stream, alongside failures no retry can fix, such as "pull
// access denied". A rate limit is not retried: Docker Hub counts it per
// address over hours, and the daemon has already retried the refusal.
func temporary(err error) bool {
	if errors.Is(err, dockerclient.ErrRateLimited) {
		return false
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

// pullWaiters reports how many callers are waiting on the pull of image from
// the daemon at host. Only tests ask.
func pullWaiters(host, image string) int {
	pulls.Lock()
	defer pulls.Unlock()
	if p := pulls.inProgress[pullKey{host: host, image: image}]; p != nil {
		return p.waiters
	}
	return 0
}
