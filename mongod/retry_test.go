package mongod_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockermock"
	"github.com/tophergopher/mongotest/mongod"
)

// noWait makes the waits between pull attempts end at once.
func noWait(context.Context, time.Duration) error { return nil }

// waitForever makes a wait between pull attempts last until its context
// ends, as a long backoff would.
func waitForever(ctx context.Context, _ time.Duration) error {
	<-ctx.Done()
	return ctx.Err()
}

// Failures as the daemon reports them; see dockerclient's tests for where
// each wording comes from.
var (
	rateLimitedWhenResolving = &dockerclient.StatusError{StatusCode: 500, Method: "POST", Path: "/images/create",
		Message: `unknown: failed to resolve reference "docker.io/library/mongo:8.0": unexpected status from HEAD request to https://registry-1.docker.io/v2/library/mongo/manifests/8.0: 429 Too Many Requests`}
	rateLimitedInTheStream = &dockerclient.PullError{Ref: "docker.io/library/mongo:8.0",
		Message: "error from registry: You have reached your unauthenticated pull rate limit. https://www.docker.com/increase-rate-limit"}
	registryDown = &dockerclient.StatusError{StatusCode: 500, Method: "POST", Path: "/images/create",
		Message: `unknown: failed to resolve reference "docker.io/library/mongo:8.0": unexpected status from HEAD request: 503 Service Unavailable`}
)

// failingThen makes m's pulls fail with err the given number of times, and
// then succeed.
func failingThen(m *dockermock.Mock, err error, failures int) {
	pulls := 0
	m.ImagePullFunc = func(context.Context, string) error {
		pulls++
		if pulls <= failures {
			return err
		}
		return nil
	}
}

func TestAPullThatFailsForATemporaryReasonIsTriedAgain(t *testing.T) {
	mongod.SetPullWait(t, noWait)
	temporary := map[string]error{
		"a rate limit when resolving the tag": rateLimitedWhenResolving,
		"a rate limit in the stream":          rateLimitedInTheStream,
		"a 429 status":                        &dockerclient.StatusError{StatusCode: 429, Message: "too many requests"},
		"a registry that is down":             registryDown,
		"a gateway that timed out": &dockerclient.StatusError{StatusCode: 500,
			Message: `unknown: failed to resolve reference "docker.io/library/mongo:8.0": unexpected status from HEAD request: 504 Gateway Timeout`},
		"a connection that timed out": &dockerclient.PullError{Ref: "docker.io/library/mongo:8.0", Message: "read tcp 172.17.0.1:43210->104.18.0.1:443: i/o timeout"},
		"a dropped download":          &dockerclient.PullError{Ref: "docker.io/library/mongo:8.0", Message: "short read: expected 100 bytes but got 50: unexpected EOF"},
	}
	for name, failure := range temporary {
		t.Run(name, func(t *testing.T) {
			m := pullMock(t)
			missingImage(m)
			failingThen(m, failure, 2)

			err := mongod.PullImage(context.Background(), mongod.DefaultOptions().WithDocker(m))

			assert.NoError(t, err, "a temporary failure that clears is not reported")
			assert.Len(t, m.CallsTo("ImagePull"), 3, "the pull was tried until it succeeded")
		})
	}
}

func TestAPullThatARetryCannotFixFailsAtOnce(t *testing.T) {
	mongod.SetPullWait(t, noWait)
	permanent := map[string]error{
		"a missing tag":       &dockerclient.StatusError{StatusCode: 404, Message: "manifest for mongo:0.1 not found"},
		"refused credentials": &dockerclient.StatusError{StatusCode: 401, Message: "authentication required"},
		"access denied, as a 500": &dockerclient.StatusError{StatusCode: 500,
			Message: "pull access denied, repository does not exist or may require authorization: authorization failed: no basic auth credentials"},
		"access denied, in the stream": &dockerclient.PullError{Ref: "docker.io/library/mongo:8.0", Message: "error from registry: denied"},
		"an unreachable daemon":        &dockerclient.ConnectionError{Host: "unix:///var/run/docker.sock", Problem: "no such file"},
	}
	for name, failure := range permanent {
		t.Run(name, func(t *testing.T) {
			m := pullMock(t)
			missingImage(m)
			failingThen(m, failure, 2)

			err := mongod.PullImage(context.Background(), mongod.DefaultOptions().WithDocker(m))

			assert.ErrorIs(t, err, failure, "the failure is reported")
			assert.Len(t, m.CallsTo("ImagePull"), 1, "without trying again, which would only fail the same way and cost time")
		})
	}
}

// A rate limit that outlasts every attempt is remembered, so a run of tests
// does not wait out the attempts again in each test.
func TestASpentRateLimitIsRememberedForAWhile(t *testing.T) {
	mongod.SetPullWait(t, noWait)
	mongod.SetRateLimitMemory(t, time.Hour)
	m := pullMock(t)
	missingImage(m)
	m.ImagePullFunc = func(context.Context, string) error { return rateLimitedInTheStream }
	opts := mongod.DefaultOptions().WithDocker(m)

	first := mongod.PullImage(context.Background(), opts)
	require.ErrorIs(t, first, dockerclient.ErrRateLimited, "the caller can tell it was the rate limit")
	attempts := len(m.CallsTo("ImagePull"))
	require.Greater(t, attempts, 1, "it was tried more than once")

	second := mongod.PullImage(context.Background(), opts)

	assert.ErrorIs(t, second, dockerclient.ErrRateLimited, "a later pull of the same image is told the limit is spent")
	assert.Len(t, m.CallsTo("ImagePull"), attempts, "without asking the registry again")
}

func TestASpentRateLimitIsTriedAgainOnceForgotten(t *testing.T) {
	mongod.SetPullWait(t, noWait)
	mongod.SetRateLimitMemory(t, time.Millisecond)
	m := pullMock(t)
	missingImage(m)
	limited := true
	m.ImagePullFunc = func(context.Context, string) error {
		if limited {
			return rateLimitedInTheStream
		}
		return nil
	}
	opts := mongod.DefaultOptions().WithDocker(m)
	require.ErrorIs(t, mongod.PullImage(context.Background(), opts), dockerclient.ErrRateLimited, "the first pull uses up its attempts on the limit")
	limited = false
	time.Sleep(10 * time.Millisecond)

	assert.NoError(t, mongod.PullImage(context.Background(), opts), "once the limit is forgotten, the next pull asks the registry")
}

func TestARateLimitCutShortByTheCallerIsNotRemembered(t *testing.T) {
	mongod.SetPullWait(t, waitForever)
	mongod.SetRateLimitMemory(t, time.Hour)
	m := pullMock(t)
	missingImage(m)
	limited := true
	m.ImagePullFunc = func(context.Context, string) error {
		if limited {
			return rateLimitedInTheStream
		}
		return nil
	}
	opts := mongod.DefaultOptions().WithDocker(m)
	short := opts.WithPullTimeout(50 * time.Millisecond)
	require.ErrorIs(t, mongod.PullImage(context.Background(), short), context.DeadlineExceeded, "the pull timeout ends the wait for another attempt")
	limited = false

	assert.NoError(t, mongod.PullImage(context.Background(), opts),
		"a pull cut short is not remembered, so the next pull asks the registry")
}

func TestRetryingStopsWhenTheTimeRunsOut(t *testing.T) {
	cases := map[string]struct {
		opts   mongod.Options
		cancel bool
		want   error
	}{
		"the pull timeout ends": {opts: mongod.DefaultOptions().WithPullTimeout(50 * time.Millisecond), want: context.DeadlineExceeded},
		"the context ends":      {opts: mongod.DefaultOptions(), cancel: true, want: context.Canceled},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			// The time runs out while the pull waits to try again.
			mongod.SetPullWait(t, func(pullCtx context.Context, _ time.Duration) error {
				if tc.cancel {
					cancel()
				}
				return waitForever(pullCtx, 0)
			})
			m := pullMock(t)
			missingImage(m)
			m.ImagePullFunc = func(context.Context, string) error { return registryDown }

			err := receive(t, pullInBackground(ctx, tc.opts.WithDocker(m)))

			require.ErrorIs(t, err, tc.want, "retrying stops when the caller's time runs out")
			assert.ErrorIs(t, err, registryDown, "and the error still says why the pull failed")
			assert.Len(t, m.CallsTo("ImagePull"), 1, "no attempt is made once the time is up")
		})
	}
}

func TestARateLimitedPullOfTheOfficialImageSuggestsPublicECR(t *testing.T) {
	mongod.SetPullWait(t, noWait)
	cases := map[string]struct {
		image   string
		failure error
		want    bool
	}{
		"the official image, rate limited":  {"8.0", rateLimitedInTheStream, true},
		"the official image, pinned":        {"mongo@sha256:" + strings.Repeat("a", 64), rateLimitedInTheStream, true},
		"the community image, rate limited": {"mongodb/mongodb-community-server:8.0-ubi8", rateLimitedInTheStream, false},
		"the official image, a missing tag": {"8.0", &dockerclient.StatusError{StatusCode: 404, Message: "not found"}, false},
		"another image with a similar name": {"mongo-express:1", rateLimitedInTheStream, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := pullMock(t)
			missingImage(m)
			m.ImagePullFunc = func(context.Context, string) error { return tc.failure }

			err := mongod.PullImage(context.Background(), mongod.DefaultOptions().WithDocker(m).WithImage(tc.image))

			require.Error(t, err, "the pull fails")
			assert.Equal(t, tc.want, strings.Contains(err.Error(), "ImagePublicECR"),
				"ECR Public is a way round Docker Hub's limit only for the image it mirrors, and the reader has to be told its name")
		})
	}
}
