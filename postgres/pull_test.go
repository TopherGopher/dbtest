package postgres_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/dbtest/dockerclient"
	"github.com/tophergopher/dbtest/dockermock"
	"github.com/tophergopher/dbtest/internal/pull"
	"github.com/tophergopher/dbtest/postgres"
)

// rateLimited is how the daemon reports Docker Hub's pull rate limit.
var rateLimited = &dockerclient.PullError{Ref: "docker.io/library/postgres:18",
	Message: "error from registry: You have reached your unauthenticated pull rate limit. https://www.docker.com/increase-rate-limit"}

// pullMock is readyMock for a daemon that lacks every image, on an address
// of its own, so that no other test shares its pulls.
func pullMock(t *testing.T) *dockermock.Mock {
	t.Helper()
	t.Cleanup(pull.ForgetSpentLimits)
	m, _ := readyMock(t)
	m.HostFunc = t.Name
	m.ImageInspectFunc = func(context.Context, string) (dockerclient.ImageInspect, error) {
		return dockerclient.ImageInspect{}, dockerclient.ErrNotFound
	}
	creates := 0
	m.ContainerCreateFunc = func(context.Context, string, dockerclient.ContainerConfig) (string, []string, error) {
		creates++
		if creates == 1 {
			return "", nil, dockerclient.ErrNotFound
		}
		return containerID, nil, nil
	}
	return m
}

// When Docker Hub's rate limit refuses the official image, the container
// runs Amazon ECR Public's copy, and says so.
func TestARateLimitedImageComesFromTheMirror(t *testing.T) {
	m := pullMock(t)
	m.ImagePullFunc = func(_ context.Context, ref string) error {
		if strings.HasPrefix(ref, "docker.io/") {
			return rateLimited
		}
		return nil
	}

	c, err := start(t, m, postgres.DefaultOptions())

	require.NoError(t, err, "a rate limit falls back to the mirror")
	const mirror = "public.ecr.aws/docker/library/postgres:18"
	_, request := lastCreate(t, m)
	assert.Equal(t, mirror, request.Image, "the container is created from the mirror's copy")
	assert.Equal(t, mirror, c.Image(), "and reports it")
}

// A failed pull says it was the pull that failed, whether in a start or in
// PullImage, and a pull that ran out of time matches the deadline.
func TestAPullThatRunsOutOfTimeSaysSo(t *testing.T) {
	pullForever := func(ctx context.Context, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	}
	opts := postgres.DefaultOptions().WithPullTimeout(50 * time.Millisecond)

	t.Run("in a start", func(t *testing.T) {
		m := pullMock(t)
		m.ImagePullFunc = pullForever
		_, err := start(t, m, opts)
		assertPullTimedOut(t, err)
	})
	t.Run("in PullImage", func(t *testing.T) {
		m := pullMock(t)
		m.ImagePullFunc = pullForever
		err := postgres.PullImage(context.Background(), opts.WithDocker(m))
		assertPullTimedOut(t, err)
	})
}

// assertPullTimedOut checks the error of a pull cut short by its timeout.
func assertPullTimedOut(t *testing.T, err error) {
	t.Helper()
	var failed *postgres.StartError
	require.ErrorAs(t, err, &failed, "the error names the step that failed")
	assert.Equal(t, postgres.OpPullImage, failed.Op, "which was the pull")
	assert.ErrorIs(t, err, context.DeadlineExceeded, "because its time ran out")
}

// A caller who gave up is not told that a pull is under way.
func TestPullImageAfterTheCallerGaveUpPullsNothing(t *testing.T) {
	m := pullMock(t)
	logger := &levelLogger{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := postgres.PullImage(ctx, postgres.DefaultOptions().WithDocker(m).WithLogger(logger))

	assert.ErrorIs(t, err, context.Canceled, "the caller's context ended")
	assert.Empty(t, m.CallsTo("ImagePull"), "so nothing is pulled")
	assert.Empty(t, logger.at("info"), "and no pull is announced")
}

// lastCreate returns the name and request of the last container created.
func lastCreate(t *testing.T, m *dockermock.Mock) (string, dockerclient.ContainerConfig) {
	t.Helper()
	calls := m.CallsTo("ContainerCreate")
	require.NotEmpty(t, calls, "a container is created")
	name, _ := calls[len(calls)-1].Args[0].(string)
	cfg, _ := calls[len(calls)-1].Args[1].(dockerclient.ContainerConfig)
	return name, cfg
}
