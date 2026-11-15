package mongod_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockermock"
	"github.com/tophergopher/mongotest/mongod"
)

// missingImage makes a mocked daemon report that it does not have the image.
func missingImage(m *dockermock.Mock) {
	m.ImageInspectFunc = func(context.Context, string) (dockerclient.ImageInspect, error) {
		return dockerclient.ImageInspect{}, dockerclient.ErrNotFound
	}
}

func TestPullImageLeavesAnImageTheDaemonHasAlone(t *testing.T) {
	m := &dockermock.Mock{}

	err := mongod.PullImage(context.Background(), mongod.DefaultOptions().WithDocker(m))

	require.NoError(t, err, "an image that is already there needs nothing")
	inspections := m.CallsTo("ImageInspect")
	require.Len(t, inspections, 1, "the daemon is asked whether it has the image")
	assert.Equal(t, "docker.io/library/mongo:8.0", inspections[0].Args[0], "and it is asked about the image a start would run")
	assert.Empty(t, m.CallsTo("ImagePull"), "pulling it again would be a trip to the registry for nothing, and can hit a rate limit")
}

func TestPullImagePullsAnImageTheDaemonLacks(t *testing.T) {
	m := &dockermock.Mock{}
	missingImage(m)

	err := mongod.PullImage(context.Background(), mongod.DefaultOptions().WithDocker(m).WithImage("7.0"))

	require.NoError(t, err, "a missing image is pulled")
	inspections, pulls := m.CallsTo("ImageInspect"), m.CallsTo("ImagePull")
	require.Len(t, inspections, 1, "the daemon is asked whether it has the image")
	require.Len(t, pulls, 1, "and the image is pulled once")
	assert.Equal(t, "docker.io/library/mongo:7.0", pulls[0].Args[0], "it is the image a start with these options would run")
	assert.Equal(t, pulls[0].Args[0], inspections[0].Args[0], "and the one the daemon was asked about, not some other it happens to have")
	assert.Empty(t, m.CallsTo("ContainerCreate"), "no container is created")
}

func TestPullImageReportsAPullThatFails(t *testing.T) {
	m := &dockermock.Mock{}
	missingImage(m)
	refused := errors.New("toomanyrequests: rate limit exceeded")
	m.ImagePullFunc = func(context.Context, string) error { return refused }

	err := mongod.PullImage(context.Background(), mongod.DefaultOptions().WithDocker(m))

	var failed *mongod.StartError
	require.ErrorAs(t, err, &failed, "a failed pull is the same error a start reports for one")
	assert.Equal(t, mongod.OpPullImage, failed.Op, "it says the pull is what failed")
	assert.Equal(t, "docker.io/library/mongo:8.0", failed.Image, "and names the image")
	assert.ErrorIs(t, err, refused, "with the registry's own reason")
}

func TestPullImageRefusesOptionsAStartWouldRefuse(t *testing.T) {
	m := &dockermock.Mock{}

	err := mongod.PullImage(context.Background(), mongod.DefaultOptions().WithDocker(m).WithImage(""))

	require.ErrorIs(t, err, mongod.ErrEmptyImage, "a mistake in the options is reported")
	assert.Empty(t, m.Calls(), "before the daemon is asked anything")
}

// missingUntilPulled makes a mocked daemon refuse the first container it is
// asked for, as it does when it lacks the image, so that a start pulls.
func missingUntilPulled(m *dockermock.Mock) {
	creates := 0
	m.ContainerCreateFunc = func(context.Context, string, dockerclient.ContainerConfig) (string, []string, error) {
		creates++
		if creates == 1 {
			return "", nil, dockerclient.ErrNotFound
		}
		return containerID, nil, nil
	}
}

// pullUntilCancelled is a pull that never finishes by itself, like one from a
// registry that has stopped sending.
func pullUntilCancelled(ctx context.Context, _ string) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestAPullThatOutlastsThePullTimeoutFails(t *testing.T) {
	opts := mongod.DefaultOptions().WithPullTimeout(50 * time.Millisecond)
	pulls := map[string]func(*dockermock.Mock) error{
		"by a start": func(m *dockermock.Mock) error {
			missingUntilPulled(m)
			_, err := start(t, m, opts)
			return err
		},
		"by PullImage": func(m *dockermock.Mock) error {
			missingImage(m)
			return mongod.PullImage(context.Background(), opts.WithDocker(m))
		},
	}
	for name, pull := range pulls {
		t.Run(name, func(t *testing.T) {
			m, _ := readyMock(t)
			m.ImagePullFunc = pullUntilCancelled

			err := pull(m)

			var failed *mongod.StartError
			require.ErrorAs(t, err, &failed, "a pull that runs out of time is reported like any failed pull")
			assert.Equal(t, mongod.OpPullImage, failed.Op, "it says the pull is what failed")
			assert.ErrorIs(t, err, context.DeadlineExceeded, "because it ran out of time")
			assert.LessOrEqual(t, len(m.CallsTo("ContainerCreate")), 1, "no container is asked for once the pull has failed")
			assert.Empty(t, m.CallsTo("ContainerStart"), "and nothing is started")
		})
	}
}

func TestAPullWithinThePullTimeoutMayOutlastTheStartTimeout(t *testing.T) {
	const startTimeout = 200 * time.Millisecond
	m, _ := readyMock(t)
	missingUntilPulled(m)
	m.ImagePullFunc = func(ctx context.Context, _ string) error {
		select {
		case <-time.After(2 * startTimeout):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	_, err := start(t, m, mongod.DefaultOptions().WithStartTimeout(startTimeout).WithPullTimeout(time.Minute))

	assert.NoError(t, err, "the pull has its own bound, and the start timeout begins again once the image is there")
}

func TestTheCallersContextEndsAPullBeforeThePullTimeout(t *testing.T) {
	m, _ := readyMock(t)
	missingUntilPulled(m)
	ctx, cancel := context.WithCancel(context.Background())
	m.ImagePullFunc = func(pullCtx context.Context, image string) error {
		cancel()
		return pullUntilCancelled(pullCtx, image)
	}

	_, err := mongod.StartWithOptions(ctx, mongod.DefaultOptions().WithDocker(m).WithPullTimeout(time.Hour))

	require.ErrorIs(t, err, context.Canceled, "the caller cancelling ends the pull at once")
	assert.NotErrorIs(t, err, context.DeadlineExceeded, "and it is not reported as the pull timeout running out")
}
