package mongod_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/dbtest/dockerclient"
	"github.com/tophergopher/dbtest/dockermock"
	"github.com/tophergopher/dbtest/mongod"
)

// The image every test here pulls, as Start and PullImage name it.
const defaultImage = "docker.io/library/mongo:8.0"

// heldPull is a mocked daemon's pull that does not finish until the test
// says so. Until a pull has succeeded, the daemon says it lacks the image.
type heldPull struct {
	started chan string   // receives each image as its pull begins
	release chan struct{} // closing it lets every pull finish
	result  error         // what a released pull returns
	pulled  atomic.Bool
	once    sync.Once
}

// letGo lets every pull finish. It may be called more than once.
func (h *heldPull) letGo() { h.once.Do(func() { close(h.release) }) }

// pullMock is a mocked daemon whose address is the test's own. Pulls in
// progress are shared across the process by daemon address, so this keeps
// a pull one test leaves behind from being joined by the next.
func pullMock(t *testing.T) *dockermock.Mock {
	t.Cleanup(mongod.ForgetSpentLimits)
	return &dockermock.Mock{HostFunc: t.Name}
}

// holdPulls makes m's pulls wait for the test, and its image missing until
// one succeeds, so that a caller arriving late finds the image and pulls
// nothing. The pulls are let go when the test ends, if not before.
func holdPulls(t *testing.T, m *dockermock.Mock) *heldPull {
	if m.HostFunc == nil {
		m.HostFunc = t.Name
	}
	h := &heldPull{started: make(chan string, 16), release: make(chan struct{})}
	t.Cleanup(h.letGo)
	create := m.ContainerCreateFunc
	m.ContainerCreateFunc = func(ctx context.Context, name string, cfg dockerclient.ContainerConfig) (string, []string, error) {
		if !h.pulled.Load() {
			return "", nil, dockerclient.ErrNotFound
		}
		return create(ctx, name, cfg)
	}
	m.ImageInspectFunc = func(context.Context, string) (dockerclient.ImageInspect, error) {
		if !h.pulled.Load() {
			return dockerclient.ImageInspect{}, dockerclient.ErrNotFound
		}
		return dockerclient.ImageInspect{}, nil
	}
	m.ImagePullFunc = func(ctx context.Context, image string) error {
		h.started <- image
		select {
		case <-h.release:
		case <-ctx.Done():
			return ctx.Err()
		}
		if h.result != nil {
			return h.result
		}
		h.pulled.Store(true)
		return nil
	}
	return h
}

// waitForWaiters waits until n callers are waiting on the pull of image
// from m's daemon.
func waitForWaiters(t *testing.T, m *dockermock.Mock, image string, n int) {
	t.Helper()
	require.Eventually(t, func() bool { return mongod.PullWaiters(m.Host(), image) == n }, 10*time.Second, time.Millisecond,
		"%d callers should be waiting on one pull of %s", n, image)
}

// pullInBackground calls PullImage on a goroutine and returns its result.
func pullInBackground(ctx context.Context, opts mongod.Options) <-chan error {
	done := make(chan error, 1)
	go func() { done <- mongod.PullImage(ctx, opts) }()
	return done
}

// receive returns the next error from done, failing rather than hanging.
func receive(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		require.Fail(t, "the caller is still waiting")
		return nil
	}
}

func TestStartsAndPullImageThatNeedTheSameImageShareOnePull(t *testing.T) {
	m, _ := readyMock(t)
	held := holdPulls(t, m)
	opts := mongod.DefaultOptions().WithDocker(m)
	const callers = 6
	done := make(chan error, callers)
	for i := range callers {
		if i%2 == 0 {
			go func() { done <- mongod.PullImage(context.Background(), opts) }()
			continue
		}
		go func() {
			c, err := mongod.StartWithOptions(context.Background(), opts)
			if err == nil {
				t.Cleanup(func() { _ = c.Stop(context.Background()) })
			}
			done <- err
		}()
	}

	waitForWaiters(t, m, defaultImage, callers)
	held.letGo()

	for range callers {
		assert.NoError(t, receive(t, done), "every caller gets the image")
	}
	assert.Len(t, m.CallsTo("ImagePull"), 1, "the registry is asked once")
}

func TestTheSameImageNamedTwoWaysSharesOnePull(t *testing.T) {
	m := pullMock(t)
	held := holdPulls(t, m)

	short := pullInBackground(context.Background(), mongod.DefaultOptions().WithDocker(m).WithImage("mongo:8.0"))
	long := pullInBackground(context.Background(), mongod.DefaultOptions().WithDocker(m).WithImage("docker.io/library/mongo:8.0"))
	waitForWaiters(t, m, defaultImage, 2)
	held.letGo()

	assert.NoError(t, receive(t, short), "the short name gets the image")
	assert.NoError(t, receive(t, long), "and so does the full one")
	assert.Len(t, m.CallsTo("ImagePull"), 1, "they name one image, so it is pulled once")
}

func TestPullsOfDifferentImagesRunTogether(t *testing.T) {
	m := pullMock(t)
	held := holdPulls(t, m)
	opts := mongod.DefaultOptions().WithDocker(m)

	older := pullInBackground(context.Background(), opts.WithImage("7.0"))
	newer := pullInBackground(context.Background(), opts.WithImage("8.0"))
	waitForPulls(t, held, 2)
	held.letGo()

	assert.NoError(t, receive(t, older), "one image is pulled")
	assert.NoError(t, receive(t, newer), "and the other, while the first was still pulling")
}

func TestCallersUsingDifferentDaemonsEachPull(t *testing.T) {
	local, remote := pullMock(t), &dockermock.Mock{HostFunc: func() string { return t.Name() + "-remote" }}
	heldLocal, heldRemote := holdPulls(t, local), holdPulls(t, remote)

	fromLocal := pullInBackground(context.Background(), mongod.DefaultOptions().WithDocker(local))
	fromRemote := pullInBackground(context.Background(), mongod.DefaultOptions().WithDocker(remote))
	waitForPulls(t, heldLocal, 1)
	waitForPulls(t, heldRemote, 1)
	heldLocal.letGo()
	heldRemote.letGo()

	assert.NoError(t, receive(t, fromLocal), "each daemon gets the image")
	assert.NoError(t, receive(t, fromRemote), "a pull on one daemon does not put the image on another")
}

func TestCallersWaitingOnAFailedPullGetItsErrorAndALaterCallerPullsAgain(t *testing.T) {
	m := pullMock(t)
	held := holdPulls(t, m)
	held.result = errors.New("the registry is down")
	opts := mongod.DefaultOptions().WithDocker(m)

	first := pullInBackground(context.Background(), opts)
	second := pullInBackground(context.Background(), opts)
	waitForWaiters(t, m, defaultImage, 2)
	held.letGo()

	assert.ErrorIs(t, receive(t, first), held.result, "the caller that started the pull gets its error")
	assert.ErrorIs(t, receive(t, second), held.result, "and so does the one that joined it")
	held.result = nil
	assert.NoError(t, mongod.PullImage(context.Background(), opts), "a failed pull is not remembered: the next caller tries again")
	assert.Len(t, m.CallsTo("ImagePull"), 2, "with a pull of its own")
}

func TestAWaitingCallerLeavesWhenItsOwnTimeRunsOut(t *testing.T) {
	cases := map[string]struct {
		opts   mongod.Options
		cancel bool
		want   error
	}{
		"its context ends":      {mongod.DefaultOptions(), true, context.Canceled},
		"its pull timeout ends": {mongod.DefaultOptions().WithPullTimeout(time.Millisecond), false, context.DeadlineExceeded},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := pullMock(t)
			held := holdPulls(t, m)
			patient := pullInBackground(context.Background(), mongod.DefaultOptions().WithDocker(m))
			waitForWaiters(t, m, defaultImage, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			impatient := pullInBackground(ctx, tc.opts.WithDocker(m))
			if tc.cancel {
				waitForWaiters(t, m, defaultImage, 2)
				cancel()
			}

			assert.ErrorIs(t, receive(t, impatient), tc.want, "a caller whose own time is up returns at once")
			held.letGo()
			assert.NoError(t, receive(t, patient), "and the pull carries on for the caller still waiting")
			assert.Len(t, m.CallsTo("ImagePull"), 1, "it was one pull throughout")
		})
	}
}

// The pull a caller joins is bounded by the pull timeout of the caller that
// started it, and every caller still waiting learns why the last attempt
// failed.
func TestAJoinedPullEndsAtTheStartersPullTimeout(t *testing.T) {
	mongod.SetPullWait(t, waitForever)
	m := pullMock(t)
	missingImage(m)
	attempted := make(chan struct{}, 1)
	m.ImagePullFunc = func(context.Context, string) error {
		select {
		case attempted <- struct{}{}:
		default:
		}
		return registryDown
	}
	starter := pullInBackground(context.Background(), mongod.DefaultOptions().WithDocker(m).WithPullTimeout(time.Second))
	<-attempted
	joiner := pullInBackground(context.Background(), mongod.DefaultOptions().WithDocker(m))
	waitForWaiters(t, m, defaultImage, 2)

	err := receive(t, joiner)

	require.ErrorIs(t, err, context.DeadlineExceeded, "the joiner waits no longer than the starter's pull timeout")
	assert.ErrorIs(t, err, registryDown, "and its error says why the pull failed")
	assert.ErrorIs(t, receive(t, starter), context.DeadlineExceeded, "the starter's own timeout ended it too")
}

func TestTheCallerThatStartedAPullCanLeaveWithoutFailingTheOthers(t *testing.T) {
	m := pullMock(t)
	held := holdPulls(t, m)
	opts := mongod.DefaultOptions().WithDocker(m)
	ctx, cancel := context.WithCancel(context.Background())
	starter := pullInBackground(ctx, opts)
	waitForWaiters(t, m, defaultImage, 1)
	joiner := pullInBackground(context.Background(), opts)
	waitForWaiters(t, m, defaultImage, 2)

	cancel()
	assert.ErrorIs(t, receive(t, starter), context.Canceled, "the caller that started the pull leaves")
	held.letGo()

	assert.NoError(t, receive(t, joiner), "and the caller that joined it still gets the image")
}

// waitForPulls waits until n pulls have begun on held's daemon.
func waitForPulls(t *testing.T, held *heldPull, n int) {
	t.Helper()
	for range n {
		select {
		case <-held.started:
		case <-time.After(10 * time.Second):
			require.Fail(t, "the pulls should be in flight at the same time")
		}
	}
}

func TestAPullNobodyIsWaitingForIsAbandoned(t *testing.T) {
	m := pullMock(t)
	missingImage(m)
	abandoned := make(chan struct{})
	pulls := 0
	m.ImagePullFunc = func(ctx context.Context, _ string) error {
		if pulls++; pulls > 1 {
			return nil
		}
		<-ctx.Done()
		close(abandoned)
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	only := pullInBackground(ctx, mongod.DefaultOptions().WithDocker(m))
	waitForWaiters(t, m, defaultImage, 1)

	cancel()
	assert.ErrorIs(t, receive(t, only), context.Canceled, "the only caller leaves")
	select {
	case <-abandoned:
	case <-time.After(10 * time.Second):
		require.Fail(t, "an abandoned pull is cancelled")
	}

	assert.NoError(t, mongod.PullImage(context.Background(), mongod.DefaultOptions().WithDocker(m)), "a later caller starts a pull of its own")
	assert.Len(t, m.CallsTo("ImagePull"), 2, "rather than joining the abandoned one")
}
