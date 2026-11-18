package mongod_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockermock"
	"github.com/tophergopher/mongotest/internal/reaper"
	"github.com/tophergopher/mongotest/mongod"
)

func TestStartWithNoOptions(t *testing.T) {
	m, port := readyMock(t)

	c := mustStart(t, m, mongod.DefaultOptions())

	request := created(t, m)
	binding := request.HostConfig.PortBindings["27017/tcp"][0]
	assert.Equal(t, "docker.io/library/mongo:8.0", request.Image, "the default image, under its full name")
	assert.Equal(t, "docker.io/library/mongo:8.0", c.Image(), "which the container reports")
	assert.Equal(t, "127.0.0.1", binding.HostIP, "a local container is not published to the network")
	assert.Empty(t, binding.HostPort, "the daemon chooses the port, so parallel starts cannot collide")
	assert.Equal(t, port, c.Port(), "and the container reports the one it chose")
	assert.Equal(t, "127.0.0.1", c.Host(), "where the port is published")
	assert.Equal(t, mongod.RequiredLabelValue, request.Labels[mongod.RequiredLabelKey], "every container can be found by its label")
	assert.Equal(t, createdName(t, m), c.Name(), "the container reports its generated name")
	assert.Equal(t, containerID, c.ID(), "and the id the daemon gave it")
	assert.Empty(t, request.Cmd, "mongod gets no extra flags")
}

// The daemon can warn when it creates a container, such as about a limit
// the kernel cannot apply; each warning reaches the caller's logger.
func TestTheDaemonsWarningsAreLogged(t *testing.T) {
	warnings := []string{"the kernel cannot limit memory", "the kernel cannot limit swap"}
	m, _ := readyMock(t)
	m.ContainerCreateFunc = func(context.Context, string, dockerclient.ContainerConfig) (string, []string, error) {
		return containerID, warnings, nil
	}
	logger := &levelLogger{}

	c := mustStart(t, m, mongod.DefaultOptions().WithLogger(logger))

	for _, warning := range warnings {
		warned := logger.about("warn", "warning", warning)
		require.Len(t, warned, 1, "each warning is logged once at warn")
		assert.Equal(t, c.Name(), warned[0].attrs["container"], "naming the container")
	}
}

func TestGeneratedNamesDiffer(t *testing.T) {
	first, _ := readyMock(t)
	second, _ := readyMock(t)
	mustStart(t, first, mongod.DefaultOptions())
	mustStart(t, second, mongod.DefaultOptions())

	assert.NotEqual(t, createdName(t, first), createdName(t, second), "parallel starts must not ask for the same name")
}

func TestStartPullsAnImageTheDaemonDoesNotHave(t *testing.T) {
	m, _ := readyMock(t)
	creates := 0
	m.ContainerCreateFunc = func(context.Context, string, dockerclient.ContainerConfig) (string, []string, error) {
		creates++
		if creates == 1 {
			return "", nil, dockerclient.ErrNotFound
		}
		return containerID, nil, nil
	}

	_, err := start(t, m, mongod.DefaultOptions())

	require.NoError(t, err, "a missing image is pulled, not reported")
	require.Len(t, m.CallsTo("ImagePull"), 1, "once")
	assert.Equal(t, "docker.io/library/mongo:8.0", m.CallsTo("ImagePull")[0].Args[0], "the image that was asked for")
}

// A first run can sit in a pull for minutes. Without a line at the level
// loggers show by default, it looks like a hang.
func TestAPullIsAnnouncedWhereAFirstTimeUserSeesIt(t *testing.T) {
	const image = "docker.io/library/mongo:8.0"
	pulls := map[string]func(*dockermock.Mock, mongod.Options) error{
		"by a start": func(m *dockermock.Mock, opts mongod.Options) error {
			creates := 0
			m.ContainerCreateFunc = func(context.Context, string, dockerclient.ContainerConfig) (string, []string, error) {
				creates++
				if creates == 1 {
					return "", nil, dockerclient.ErrNotFound
				}
				return containerID, nil, nil
			}
			_, err := start(t, m, opts)
			return err
		},
		"by PullImage": func(m *dockermock.Mock, opts mongod.Options) error {
			missingImage(m)
			return mongod.PullImage(context.Background(), opts.WithDocker(m))
		},
	}
	for name, pull := range pulls {
		t.Run(name, func(t *testing.T) {
			m, _ := readyMock(t)
			logger := &levelLogger{}
			announcedBeforeThePull := false
			m.ImagePullFunc = func(context.Context, string) error {
				announcedBeforeThePull = len(logger.about("info", "image", image)) > 0
				return nil
			}

			require.NoError(t, pull(m, mongod.DefaultOptions().WithLogger(logger)), "the pull succeeds")

			assert.True(t, announcedBeforeThePull, "the pull is logged at info")
		})
	}
}

// A pull can take minutes, and the start timeout is for mongod, not for the
// registry.
func TestAPullLongerThanTheStartTimeoutDoesNotUseItUp(t *testing.T) {
	const timeout = time.Second
	m, _ := readyMock(t)
	var firstDeadline time.Time
	m.ContainerCreateFunc = func(ctx context.Context, _ string, _ dockerclient.ContainerConfig) (string, []string, error) {
		if firstDeadline.IsZero() {
			firstDeadline, _ = ctx.Deadline()
			return "", nil, dockerclient.ErrNotFound
		}
		return containerID, nil, nil
	}
	m.ImagePullFunc = func(context.Context, string) error {
		time.Sleep(time.Until(firstDeadline) + 50*time.Millisecond) // until the timeout that began before the pull has passed
		return nil
	}

	_, err := start(t, m, mongod.DefaultOptions().WithStartTimeout(timeout))

	assert.NoError(t, err, "the timeout restarts after the pull")
}

func TestStartNamesTheStepThatFailed(t *testing.T) {
	daemonSaidNo := errors.New("the daemon said no")
	cases := []struct {
		op      string
		arrange func(*dockermock.Mock)
	}{
		{mongod.OpPullImage, func(m *dockermock.Mock) {
			m.ContainerCreateFunc = func(context.Context, string, dockerclient.ContainerConfig) (string, []string, error) {
				return "", nil, dockerclient.ErrNotFound
			}
			m.ImagePullFunc = func(context.Context, string) error { return daemonSaidNo }
		}},
		{mongod.OpCreate, func(m *dockermock.Mock) {
			m.ContainerCreateFunc = func(context.Context, string, dockerclient.ContainerConfig) (string, []string, error) {
				return "", nil, daemonSaidNo
			}
		}},
		{mongod.OpStart, func(m *dockermock.Mock) {
			m.ContainerStartFunc = func(context.Context, string) error { return daemonSaidNo }
		}},
		{mongod.OpInspect, func(m *dockermock.Mock) {
			m.ContainerInspectFunc = func(context.Context, string) (dockerclient.ContainerInspect, error) {
				return dockerclient.ContainerInspect{}, daemonSaidNo
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.op, func(t *testing.T) {
			m, _ := readyMock(t)
			tc.arrange(m)

			_, err := start(t, m, mongod.DefaultOptions())

			var startErr *mongod.StartError
			require.ErrorAs(t, err, &startErr, "a failed step is a StartError")
			assert.Equal(t, tc.op, startErr.Op, "naming the step")
			assert.ErrorIs(t, err, daemonSaidNo, "and wrapping the daemon's own error")
		})
	}
}

func TestAFailedStartRemovesItsContainer(t *testing.T) {
	m, _ := readyMock(t)
	m.ContainerStartFunc = func(context.Context, string) error { return errors.New("no") }

	_, err := start(t, m, mongod.DefaultOptions())

	require.Error(t, err, "the start fails")
	assert.Len(t, m.CallsTo("ContainerRemove"), 1, "and does not leave the container it created")
}

func TestAFailedStartRemovesItsContainerEvenWhenTheContextIsWhatFailed(t *testing.T) {
	m, _ := readyMock(t)
	ctx, cancel := context.WithCancel(context.Background())
	m.ContainerStartFunc = func(context.Context, string) error {
		cancel()
		return context.Canceled
	}
	removed, live, bounded := false, false, false
	m.ContainerRemoveFunc = func(ctx context.Context, _ string, _ dockerclient.RemoveOptions) error {
		removed, live = true, ctx.Err() == nil
		_, bounded = ctx.Deadline()
		return nil
	}

	_, err := mongod.StartWithOptions(ctx, mongod.DefaultOptions().WithDocker(m).WithHostIP("127.0.0.1"))

	require.Error(t, err, "the start fails")
	require.True(t, removed, "the container is still removed")
	assert.True(t, live, "on a context that is not the cancelled one")
	assert.True(t, bounded, "and that has a deadline, so a stalled daemon cannot hang the caller")
}

func TestStartFailsWhenTheDaemonPublishedNoPort(t *testing.T) {
	m, _ := readyMock(t)
	m.ContainerInspectFunc = func(context.Context, string) (dockerclient.ContainerInspect, error) {
		return dockerclient.ContainerInspect{State: dockerclient.ContainerState{Running: true}}, nil
	}

	_, err := start(t, m, mongod.DefaultOptions())

	assert.ErrorIs(t, err, mongod.ErrNoPublishedPort, "a container with nowhere to connect to is not returned")
}

func TestStartTimesOutWhenNothingAnswers(t *testing.T) {
	m, _ := readyMock(t)
	dead := closedPort(t)
	m.ContainerInspectFunc = func(context.Context, string) (dockerclient.ContainerInspect, error) {
		return running(dead), nil
	}

	_, err := start(t, m, mongod.DefaultOptions().WithHostIP("127.0.0.1").WithStartTimeout(100*time.Millisecond))

	require.ErrorIs(t, err, mongod.ErrNotReady, "a mongod nothing can connect to is not ready")
	assert.ErrorIs(t, err, context.DeadlineExceeded, "and the error says the time ran out")
	var notReady *mongod.NotReadyError
	require.ErrorAs(t, err, &notReady, "with the details")
	assert.Equal(t, dead, notReady.Port, "including the port that was dialled")
	assert.Equal(t, "WithHostIP", notReady.HostSource, "and what chose the address")
	assert.ErrorIs(t, err, mongod.ErrNoReply, "nothing answered there")
	assert.True(t, mongod.AddressInDoubt(notReady), "with advice about the address")
	assert.Len(t, m.CallsTo("ContainerRemove"), 1, "the container is removed")
}

func TestStartStopsWaitingWhenTheCallerCancels(t *testing.T) {
	m := notReadyMock(t)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)

	started := time.Now()
	_, err := mongod.StartWithOptions(ctx, mongod.DefaultOptions().WithDocker(m).WithHostIP("127.0.0.1"))

	require.ErrorIs(t, err, context.Canceled, "the caller's context ends the wait")
	assert.ErrorIs(t, err, mongod.ErrNotReady, "still a not-ready error")
	assert.Less(t, time.Since(started), 10*time.Second, "long before the start timeout")
}

func TestAnAddressFromTheEnvironmentIsReportedAsComingFromIt(t *testing.T) {
	t.Setenv(mongod.EnvHostIP, "127.0.0.1")
	m := notReadyMock(t)

	_, err := mongod.StartWithOptions(context.Background(), mongod.DefaultOptions().WithDocker(m).WithStartTimeout(50*time.Millisecond))

	var notReady *mongod.NotReadyError
	require.ErrorAs(t, err, &notReady, "the start times out")
	assert.Equal(t, mongod.EnvHostIP, notReady.HostSource, "the error names the variable, so a wrong address can be found")
}

func TestStartFailsAtOnceWhenTheContainerHasExited(t *testing.T) {
	cases := []struct {
		status      string
		exitCode    int
		hasExitCode bool
		inspect     func() (dockerclient.ContainerInspect, error)
	}{
		{"exited", 14, true, func() (dockerclient.ContainerInspect, error) {
			return dockerclient.ContainerInspect{State: dockerclient.ContainerState{Status: "exited", ExitCode: 14}}, nil
		}},
		{"dead", 137, true, func() (dockerclient.ContainerInspect, error) {
			return dockerclient.ContainerInspect{State: dockerclient.ContainerState{Status: "dead", ExitCode: 137}}, nil
		}},
		{"removed", 0, false, func() (dockerclient.ContainerInspect, error) {
			return dockerclient.ContainerInspect{}, dockerclient.ErrNotFound
		}},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			m, _ := readyMock(t)
			port := closedPort(t) // nothing answers once the container is gone
			inspects := 0
			m.ContainerInspectFunc = func(context.Context, string) (dockerclient.ContainerInspect, error) {
				inspects++
				if inspects == 1 {
					return running(port), nil // the start's own look at the ports
				}
				return tc.inspect()
			}

			started := time.Now()
			_, err := start(t, m, mongod.DefaultOptions())

			require.ErrorIs(t, err, mongod.ErrContainerExited, "a container that is gone will never be ready")
			var exited *mongod.ContainerExitedError
			require.ErrorAs(t, err, &exited, "with the details")
			assert.Equal(t, tc.status, exited.Status, "what happened to it")
			assert.Equal(t, tc.exitCode, exited.ExitCode, "and its exit code")
			assert.Equal(t, tc.hasExitCode, exited.HasExitCode, "which only a container that still exists has")
			assert.Less(t, time.Since(started), 10*time.Second, "without waiting out the start timeout")
		})
	}
}

func TestStopRemovesTheContainerAndItsVolumes(t *testing.T) {
	m, _ := readyMock(t)
	c := mustStart(t, m, mongod.DefaultOptions())

	require.NoError(t, c.Stop(context.Background()), "stopping succeeds")
	require.NoError(t, c.Stop(context.Background()), "and is safe to do twice")

	removals := m.CallsTo("ContainerRemove")
	require.Len(t, removals, 1, "the container is removed once")
	assert.Equal(t, dockerclient.RemoveOptions{Force: true, RemoveVolumes: true}, removals[0].Args[1], "running or not, with its volumes")
}

func TestStopOnANilContainerDoesNothing(t *testing.T) {
	var c *mongod.Container

	assert.NoError(t, c.Stop(context.Background()), "a deferred Stop after a failed Start must not panic")
}

func TestStopTreatsAContainerThatIsAlreadyGoneAsStopped(t *testing.T) {
	m, _ := readyMock(t)
	m.ContainerRemoveFunc = func(context.Context, string, dockerclient.RemoveOptions) error {
		return dockerclient.ErrNotFound
	}
	c := mustStart(t, m, mongod.DefaultOptions())

	assert.NoError(t, c.Stop(context.Background()), "something else removing it is not a failure")
}

func TestAStopTheDaemonRefusedCanBeTriedAgain(t *testing.T) {
	m, _ := readyMock(t)
	refused := errors.New("the daemon refused")
	attempts := 0
	m.ContainerRemoveFunc = func(context.Context, string, dockerclient.RemoveOptions) error {
		attempts++
		if attempts == 1 {
			return refused
		}
		return nil
	}
	c := mustStart(t, m, mongod.DefaultOptions())

	assert.ErrorIs(t, c.Stop(context.Background()), refused, "the refusal is reported")
	assert.NoError(t, c.Stop(context.Background()), "and the next Stop tries again")
}

func TestStopGivesUpAtItsDeadlineWhileAnotherStopIsStuck(t *testing.T) {
	m, _ := readyMock(t)
	entered, release := make(chan struct{}), make(chan struct{})
	m.ContainerRemoveFunc = func(context.Context, string, dockerclient.RemoveOptions) error {
		close(entered)
		<-release
		return nil
	}
	c := mustStart(t, m, mongod.DefaultOptions())
	t.Cleanup(func() { close(release) })

	go func() { _ = c.Stop(context.Background()) }() // a test's own Stop, against a stalled daemon
	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- c.Stop(ctx) }() // the reaper's, on a signal

	select {
	case err := <-stopped:
		assert.ErrorIs(t, err, context.DeadlineExceeded, "a Stop waiting behind another gives up when its context ends")
	case <-time.After(3 * time.Second):
		assert.Fail(t, "Stop keeps to its deadline")
	}
}

func TestURIAsksForADirectConnection(t *testing.T) {
	m, port := readyMock(t)

	c := mustStart(t, m, mongod.DefaultOptions())

	uri := parseURI(t, c)
	assert.Equal(t, "mongodb", uri.Scheme, "a MongoDB connection string")
	assert.Equal(t, fmt.Sprintf("127.0.0.1:%d", port), uri.Host, "to the published address")
	assert.Equal(t, "true", uri.Query().Get("directConnection"), "the driver connects directly")
}

// t.Context() has already ended by the time a t.Cleanup runs, and a Stop
// that gave up there would leave the container running behind a green test.
func TestStopStillRemovesTheContainerWhenItsContextHasAlreadyEnded(t *testing.T) {
	m, _ := readyMock(t)
	c := mustStart(t, m, mongod.DefaultOptions())
	removed, live := false, false
	m.ContainerRemoveFunc = func(ctx context.Context, _ string, _ dockerclient.RemoveOptions) error {
		removed, live = true, ctx.Err() == nil
		return nil
	}
	ended, cancel := context.WithCancel(context.Background())
	cancel()

	require.NoError(t, c.Stop(ended), "the container is removed all the same")
	require.True(t, removed, "the daemon was asked")
	assert.True(t, live, "on a context that can still carry the request")
}

func TestAWaitEndedByTheCallersDeadlineSaysSo(t *testing.T) {
	m := notReadyMock(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := mongod.StartWithOptions(ctx, mongod.DefaultOptions().WithDocker(m).WithHostIP("127.0.0.1"))

	require.ErrorIs(t, err, mongod.ErrNotReady, "mongod was not ready")
	require.ErrorIs(t, err, context.DeadlineExceeded, "because a deadline passed")
	var notReady *mongod.NotReadyError
	require.ErrorAs(t, err, &notReady, "with the details")
	assert.True(t, notReady.ContextEnded, "the caller's context ended")
}

func TestAWaitEndedByTheStartTimeoutSaysSo(t *testing.T) {
	m := notReadyMock(t)

	_, err := start(t, m, mongod.DefaultOptions().WithStartTimeout(50*time.Millisecond))

	var notReady *mongod.NotReadyError
	require.ErrorAs(t, err, &notReady, "mongod was not ready")
	assert.False(t, notReady.ContextEnded, "the start timeout ran out")
}

// Docker publishes on exactly the address it is given, so the interface the
// port is bound to has to follow the address the caller will dial.
func TestThePortIsPublishedWhereItWillBeDialled(t *testing.T) {
	t.Run("loopback stays off the network", func(t *testing.T) {
		for host, dialled := range map[string]string{
			"127.0.0.1": "127.0.0.1",
			"localhost": "localhost",
			"::1":       "127.0.0.1",
			"127.0.1.1": "127.0.0.1",
		} {
			m, _ := readyMock(t)

			c := mustStart(t, m, mongod.DefaultOptions().WithHostIP(host))

			binding := created(t, m).HostConfig.PortBindings["27017/tcp"][0]
			assert.Equal(t, "127.0.0.1", binding.HostIP, "%s is this machine, so nothing else needs to reach the port", host)
			assert.Equal(t, dialled, c.Host(), "%s is dialled at 127.0.0.1", host)
		}
	})
	t.Run("any other address needs every interface", func(t *testing.T) {
		for _, host := range []string{siblingGateway, "build-host"} {
			m := &dockermock.Mock{
				ContainerInspectFunc: func(context.Context, string) (dockerclient.ContainerInspect, error) {
					return running(1), nil
				},
			}

			_, err := start(t, m, mongod.DefaultOptions().WithHostIP(host).WithStartTimeout(20*time.Millisecond))

			var notReady *mongod.NotReadyError
			require.ErrorAs(t, err, &notReady, "nothing answers at that address")
			binding := created(t, m).HostConfig.PortBindings["27017/tcp"][0]
			assert.Equal(t, "0.0.0.0", binding.HostIP, "the daemon's host may not be able to bind %s itself", host)
			assert.Equal(t, host, notReady.Host, "and the address is dialled as given")
		}
	})
}

// removedContainers returns what each removal a mocked daemon was asked for
// named: a container's id, or its name.
func removedContainers(m *dockermock.Mock) []string {
	var removed []string
	for _, call := range m.CallsTo("ContainerRemove") {
		removed = append(removed, call.Args[0].(string))
	}
	return removed
}

// A signal can arrive while the daemon is still creating the container,
// before this process has an id to remove it by.
func TestAReapDuringCreationWaitsForTheContainerAndRemovesIt(t *testing.T) {
	m, _ := readyMock(t)
	var created atomic.Bool
	waited := make(chan bool, 1)
	m.ContainerCreateFunc = func(context.Context, string, dockerclient.ContainerConfig) (string, []string, error) {
		go func() {
			err := mongod.ReapRunningContainers(context.Background())
			waited <- err == nil && created.Load()
		}()
		time.Sleep(50 * time.Millisecond) // long enough for the reap to reach this container
		created.Store(true)
		return containerID, nil, nil
	}

	c, err := start(t, m, mongod.DefaultOptions())

	require.ErrorIs(t, err, mongod.ErrContainerExited, "a reaped container is not returned")
	var exited *mongod.ContainerExitedError
	require.ErrorAs(t, err, &exited, "with the details")
	assert.Equal(t, "removed", exited.Status, "removed, not exited")
	assert.Nil(t, c, "so the caller is handed nothing to use")
	assert.True(t, <-waited, "the reap waits for the create")
	assert.Contains(t, removedContainers(m), containerID, "and then removes the container by the id the daemon gave it")
}

// The image is pulled between two attempts to create the container, which
// can take minutes. A reap must not wait for it, and nothing may be created
// once the reap has been and gone.
func TestAReapDuringThePullCreatesNothing(t *testing.T) {
	m, _ := readyMock(t)
	m.ContainerCreateFunc = func(context.Context, string, dockerclient.ContainerConfig) (string, []string, error) {
		return "", nil, dockerclient.ErrNotFound
	}
	m.ImagePullFunc = func(ctx context.Context, _ string) error {
		return mongod.ReapRunningContainers(ctx)
	}

	_, err := start(t, m, mongod.DefaultOptions())

	require.ErrorIs(t, err, mongod.ErrContainerExited, "a start that was reaped does not carry on")
	var exited *mongod.ContainerExitedError
	require.ErrorAs(t, err, &exited, "with the details")
	assert.Equal(t, "removed", exited.Status, "removed, not exited")
	assert.Len(t, m.CallsTo("ContainerCreate"), 1, "the daemon is not asked again for a container nothing would remove")
	assert.Empty(t, m.CallsTo("ContainerRemove"), "and there is nothing to remove")
}

// A reap with a short deadline can give up while the daemon is still
// creating the container. The start, which learns of the reap when the
// daemon answers, removes the container itself.
func TestAReapThatGivesUpDuringCreationStillGetsTheContainerRemoved(t *testing.T) {
	m, _ := readyMock(t)
	m.ContainerCreateFunc = func(context.Context, string, dockerclient.ContainerConfig) (string, []string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		_ = mongod.ReapRunningContainers(ctx) // gives up: this creation holds the container
		return containerID, nil, nil
	}

	c, err := start(t, m, mongod.DefaultOptions())

	require.ErrorIs(t, err, mongod.ErrContainerExited, "a reaped container is not returned")
	var exited *mongod.ContainerExitedError
	require.ErrorAs(t, err, &exited, "with the details")
	assert.Equal(t, "removed", exited.Status, "removed, not exited")
	assert.Nil(t, c, "so the caller is handed nothing to use")
	assert.Contains(t, removedContainers(m), containerID, "the start removes the container the reap could not wait for")
}

func TestAFailedCreationIsNotKeptForReaping(t *testing.T) {
	m, _ := readyMock(t)
	m.ContainerCreateFunc = func(context.Context, string, dockerclient.ContainerConfig) (string, []string, error) {
		return "", nil, errors.New("the daemon refused")
	}

	_, err := start(t, m, mongod.DefaultOptions())

	require.Error(t, err, "the start fails")
	assert.NotContains(t, reaper.Names(), createdName(t, m), "a container the daemon never created is not kept for the reaper")
}

func TestAFailedCreationLeavesNothingToReap(t *testing.T) {
	m, _ := readyMock(t)
	m.ContainerCreateFunc = func(context.Context, string, dockerclient.ContainerConfig) (string, []string, error) {
		return "", nil, errors.New("the daemon refused")
	}

	_, err := start(t, m, mongod.DefaultOptions())
	require.Error(t, err, "the start fails")

	require.NoError(t, mongod.ReapRunningContainers(context.Background()), "a later reap has nothing to do")
	assert.Empty(t, m.CallsTo("ContainerRemove"), "no container exists, so none is asked for")
}

// startWithin starts a container with no deadline of the caller's, and
// fails the test if the start outlasts the limit. The examples pass
// context.Background(), so a daemon that never answers must not be able to
// hold Start for ever.
func startWithin(t *testing.T, limit time.Duration, opts mongod.Options) error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := mongod.StartWithOptions(context.Background(), opts)
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(limit):
		require.Fail(t, "Start is still waiting for a daemon that will never answer")
		return nil
	}
}

// The start timeout covers every call to the daemon after the pull. Running
// out of it during one is the same failure as running out of it while
// waiting for mongod, and has the same remedy.
func TestAStartTimeoutThatEndsDuringAStepIsAStartThatWasNotReady(t *testing.T) {
	cases := []struct {
		op    string
		opts  mongod.Options
		stall func(*dockermock.Mock)
	}{
		{mongod.OpCreate, mongod.DefaultOptions(), func(m *dockermock.Mock) {
			m.ContainerCreateFunc = func(ctx context.Context, _ string, _ dockerclient.ContainerConfig) (string, []string, error) {
				<-ctx.Done()
				return "", nil, ctx.Err()
			}
		}},
		{mongod.OpCopyKeyFile, mongod.DefaultOptions().WithReplicaSet("rs0").WithAuth("admin", "s3cret"), func(m *dockermock.Mock) {
			m.CopyArchiveToContainerFunc = func(ctx context.Context, _, _ string, _ io.Reader) error {
				<-ctx.Done()
				return ctx.Err()
			}
		}},
		{mongod.OpTLSCertificate, mongod.DefaultOptions().WithTLS(), func(m *dockermock.Mock) {
			m.CopyToContainerFunc = func(ctx context.Context, _, _ string, _ []dockerclient.File) error {
				<-ctx.Done()
				return ctx.Err()
			}
		}},
		{mongod.OpStart, mongod.DefaultOptions(), func(m *dockermock.Mock) {
			m.ContainerStartFunc = func(ctx context.Context, _ string) error {
				<-ctx.Done()
				return ctx.Err()
			}
		}},
		{mongod.OpInspect, mongod.DefaultOptions(), func(m *dockermock.Mock) {
			m.ContainerInspectFunc = func(ctx context.Context, _ string) (dockerclient.ContainerInspect, error) {
				<-ctx.Done()
				return dockerclient.ContainerInspect{}, ctx.Err()
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.op, func(t *testing.T) {
			m, _ := authMock(t)
			tc.stall(m)

			err := startWithin(t, 5*time.Second, tc.opts.WithDocker(m).WithStartTimeout(50*time.Millisecond))

			require.ErrorIs(t, err, mongod.ErrNotReady, "the start ran out of time, which is what a caller checks for")
			assert.ErrorIs(t, err, context.DeadlineExceeded, "and it still matches the context error that ended it")
			var notReady *mongod.NotReadyError
			require.ErrorAs(t, err, &notReady, "with the details")
			assert.Equal(t, 50*time.Millisecond, notReady.Timeout, "including the timeout to make longer")
			assert.False(t, notReady.ContextEnded, "which is what ended it, not the caller's context")
			var step *mongod.StartError
			require.ErrorAs(t, err, &step, "the step it was cut short in is kept")
			assert.Equal(t, tc.op, step.Op, "by name")
		})
	}
}

// The daemon can go on to create a container after this process has stopped
// waiting for it. Its id never arrives, so its name is all there is to
// remove it by.
func TestACreationCutShortIsRemovedByItsGeneratedName(t *testing.T) {
	m, _ := readyMock(t)
	m.ContainerCreateFunc = func(ctx context.Context, _ string, _ dockerclient.ContainerConfig) (string, []string, error) {
		<-ctx.Done()
		return "", nil, ctx.Err()
	}
	live, bounded := false, false
	m.ContainerRemoveFunc = func(ctx context.Context, _ string, _ dockerclient.RemoveOptions) error {
		live = ctx.Err() == nil
		_, bounded = ctx.Deadline()
		return dockerclient.ErrNotFound // the usual case: the daemon never got as far as creating it
	}
	logger := &levelLogger{}

	err := startWithin(t, 5*time.Second, mongod.DefaultOptions().WithDocker(m).WithLogger(logger).WithStartTimeout(50*time.Millisecond))

	require.Error(t, err, "the start fails")
	removals := m.CallsTo("ContainerRemove")
	require.Len(t, removals, 1, "the container is asked for once")
	name := createdName(t, m)
	assert.Equal(t, name, removals[0].Args[0], "by the name this start generated for it")
	assert.Equal(t, dockerclient.RemoveOptions{Force: true, RemoveVolumes: true}, removals[0].Args[1], "running or not, with its volumes")
	assert.True(t, live, "on a context that is not the one that ended")
	assert.True(t, bounded, "and that has a deadline, so a stalled daemon cannot hang the caller")
	assert.Empty(t, logger.about("error", "container", name), "a container that was never created is not reported as left behind")
}

func TestACreationCutShortThatCannotBeRemovedIsLogged(t *testing.T) {
	m, _ := readyMock(t)
	m.ContainerCreateFunc = func(ctx context.Context, _ string, _ dockerclient.ContainerConfig) (string, []string, error) {
		<-ctx.Done()
		return "", nil, ctx.Err()
	}
	m.ContainerRemoveFunc = func(context.Context, string, dockerclient.RemoveOptions) error {
		return errors.New("the daemon is not answering")
	}
	logger := &levelLogger{}

	err := startWithin(t, 5*time.Second, mongod.DefaultOptions().WithDocker(m).WithLogger(logger).WithStartTimeout(50*time.Millisecond))

	require.Error(t, err, "the start fails")
	assert.Len(t, logger.about("error", "container", createdName(t, m)), 1, "the container that may exist is named, so it can be removed by hand")
}

// A name given with WithName may belong to a container that was there
// first, and is not this start's to remove.
func TestACreationCutShortUnderAGivenNameRemovesNothing(t *testing.T) {
	m, _ := readyMock(t)
	m.ContainerCreateFunc = func(ctx context.Context, _ string, _ dockerclient.ContainerConfig) (string, []string, error) {
		<-ctx.Done()
		return "", nil, ctx.Err()
	}

	err := startWithin(t, 5*time.Second, mongod.DefaultOptions().WithDocker(m).WithName("orders-db").WithStartTimeout(50*time.Millisecond))

	require.Error(t, err, "the start fails")
	assert.Empty(t, m.CallsTo("ContainerRemove"), "nothing is removed by a name the caller chose")
}

// A removal holds the container's lock while the daemon works, so it must
// have a deadline even when the caller gave none.
func TestARemovalHasADeadlineEvenWhenTheCallerSetNone(t *testing.T) {
	removals := map[string]func(*mongod.Container) error{
		"Stop":                  func(c *mongod.Container) error { return c.Stop(context.Background()) },
		"ReapRunningContainers": func(*mongod.Container) error { return mongod.ReapRunningContainers(context.Background()) },
	}
	for name, remove := range removals {
		t.Run(name, func(t *testing.T) {
			m, _ := readyMock(t)
			bounded := false
			m.ContainerRemoveFunc = func(ctx context.Context, _ string, _ dockerclient.RemoveOptions) error {
				_, bounded = ctx.Deadline()
				return nil
			}
			c := mustStart(t, m, mongod.DefaultOptions())

			require.NoError(t, remove(c), "the removal succeeds")

			assert.True(t, bounded, "a daemon that never answered would otherwise hold the caller for ever")
		})
	}
}

func TestStoppingTwiceAsksTheDaemonOnce(t *testing.T) {
	m, _ := readyMock(t)
	c := mustStart(t, m, mongod.DefaultOptions())
	require.NoError(t, c.Stop(context.Background()), "the first stop removes the container")
	ended, cancel := context.WithCancel(context.Background())
	cancel()

	started := time.Now()
	err := c.Stop(ended)

	assert.NoError(t, err, "a second stop has nothing to do")
	assert.Len(t, m.CallsTo("ContainerRemove"), 1, "and it does not ask the daemon again")
	assert.Less(t, time.Since(started), time.Second, "nor wait for anything, even with a context that has ended")
}
