package mongod_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockermock"
	"github.com/tophergopher/mongotest/mongod"
)

// rateLimitedOnDockerHub makes m's pulls from Docker Hub fail on its rate
// limit, and its pulls from anywhere else end with mirrorErr.
func rateLimitedOnDockerHub(m *dockermock.Mock, mirrorErr error) {
	refusedOnDockerHub(m, rateLimitedInTheStream, mirrorErr)
}

// refusedOnDockerHub makes m's pulls from Docker Hub fail with refusal, and
// its pulls from anywhere else end with mirrorErr.
func refusedOnDockerHub(m *dockermock.Mock, refusal, mirrorErr error) {
	m.ImagePullFunc = func(_ context.Context, ref string) error {
		if strings.HasPrefix(ref, "docker.io/") {
			return refusal
		}
		return mirrorErr
	}
}

// pulled returns the references m was asked to pull, in order.
func pulled(m *dockermock.Mock) []string {
	var refs []string
	for _, call := range m.CallsTo("ImagePull") {
		refs = append(refs, call.Args[0].(string))
	}
	return refs
}

func TestARateLimitedPullComesFromTheMirror(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	cases := map[string]struct {
		image, hub, mirror string
		refusal            error
	}{
		"the official image": {"8.0",
			"docker.io/library/mongo:8.0", "public.ecr.aws/docker/library/mongo:8.0", rateLimitedInTheStream},
		"the official image, refused while resolving its tag": {"8.0",
			"docker.io/library/mongo:8.0", "public.ecr.aws/docker/library/mongo:8.0", rateLimitedWhenResolving},
		"the official image by digest": {"mongo@" + digest,
			"docker.io/library/mongo@" + digest, "public.ecr.aws/docker/library/mongo@" + digest, rateLimitedInTheStream},
		"MongoDB's community image": {"mongodb/mongodb-community-server:8.0-ubi9",
			"docker.io/mongodb/mongodb-community-server:8.0-ubi9", "quay.io/mongodb/mongodb-community-server:8.0-ubi9", rateLimitedInTheStream},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			mongod.SetPullWait(t, noWait)
			m := pullMock(t)
			missingImage(m)
			refusedOnDockerHub(m, tc.refusal, nil)

			err := mongod.PullImage(context.Background(), mongod.DefaultOptions().WithDocker(m).WithImage(tc.image))

			require.NoError(t, err, "a rate limit on Docker Hub is no reason to fail when a mirror has the same image")
			assert.Equal(t, []string{tc.hub, tc.mirror}, pulled(m),
				"Docker Hub is asked once, since its limit lasts hours, and then the mirror for the same tag or digest")
		})
	}
}

func TestARegistryFailureIsRetriedAndNeverMirrored(t *testing.T) {
	mongod.SetPullWait(t, noWait)
	m := pullMock(t)
	missingImage(m)
	failingThen(m, registryDown, 2)

	err := mongod.PullImage(context.Background(), mongod.DefaultOptions().WithDocker(m))

	require.NoError(t, err, "a registry that is briefly down is waited out")
	assert.Equal(t, []string{"docker.io/library/mongo:8.0", "docker.io/library/mongo:8.0", "docker.io/library/mongo:8.0"}, pulled(m),
		"only a rate limit sends the pull to a mirror; anything else that may pass is tried again where it was")
}

func TestARememberedRateLimitGoesStraightToTheMirror(t *testing.T) {
	mongod.SetPullWait(t, noWait)
	mongod.SetRateLimitMemory(t, time.Hour)
	m := pullMock(t)
	missingImage(m)
	rateLimitedOnDockerHub(m, nil)
	opts := mongod.DefaultOptions().WithDocker(m)
	require.NoError(t, mongod.PullImage(context.Background(), opts), "the first pull comes from the mirror")

	err := mongod.PullImage(context.Background(), opts)

	require.NoError(t, err, "so does the next")
	assert.Equal(t, 1, countOf(pulled(m), "docker.io/library/mongo:8.0"),
		"without asking Docker Hub again while its limit is known to be spent")
}

func TestAnImageWithNoMirrorIsNotMirrored(t *testing.T) {
	for _, image := range []string{noMirrorImage, "registry.example.com/team/mongo:8.0"} {
		t.Run(image, func(t *testing.T) {
			mongod.SetPullWait(t, noWait)
			mongod.SetRateLimitMemory(t, time.Hour)
			m := pullMock(t)
			missingImage(m)
			m.ImagePullFunc = func(context.Context, string) error { return rateLimitedInTheStream }
			opts := mongod.DefaultOptions().WithDocker(m).WithImage(image)

			err := mongod.PullImage(context.Background(), opts)

			require.ErrorIs(t, err, dockerclient.ErrRateLimited, "the rate limit is reported as before")
			refs := pulled(m)
			require.Greater(t, len(refs), 1, "with no mirror to go to, the limit is waited out")
			for _, ref := range refs {
				assert.Equal(t, refs[0], ref, "only Docker Hub's official and MongoDB images have a known mirror")
			}
			again := mongod.PullImage(context.Background(), opts)
			assert.ErrorIs(t, again, dockerclient.ErrRateLimited, "a later pull is told the limit is spent")
			assert.Len(t, pulled(m), len(refs), "without asking the registry again")
		})
	}
}

// countOf returns how many of refs are ref.
func countOf(refs []string, ref string) int {
	n := 0
	for _, r := range refs {
		if r == ref {
			n++
		}
	}
	return n
}

func TestWhenTheMirrorFailsTooBothFailuresAreReported(t *testing.T) {
	mongod.SetPullWait(t, noWait)
	mongod.SetRateLimitMemory(t, time.Hour)
	m := pullMock(t)
	missingImage(m)
	mirrorErr := &dockerclient.StatusError{StatusCode: 404, Method: "POST", Path: "/images/create", Message: "manifest unknown"}
	rateLimitedOnDockerHub(m, mirrorErr)
	opts := mongod.DefaultOptions().WithDocker(m)

	err := mongod.PullImage(context.Background(), opts)

	require.ErrorIs(t, err, dockerclient.ErrRateLimited, "the rate limit is the cause a caller tests for")
	var status *dockerclient.StatusError
	require.ErrorAs(t, err, &status, "and the mirror's own failure is there to read")
	assert.Equal(t, 404, status.StatusCode, "the mirror's failure, not Docker Hub's")
	attempts := len(pulled(m))

	again := mongod.PullImage(context.Background(), opts)

	assert.ErrorIs(t, again, dockerclient.ErrRateLimited, "a later pull is told the same")
	assert.ErrorAs(t, again, &status, "including why the mirror failed")
	assert.Len(t, pulled(m), attempts, "without asking either registry again while the failure is remembered")
}

func TestAContainerIsCreatedFromTheMirrorsImage(t *testing.T) {
	mongod.SetPullWait(t, noWait)
	m, _ := readyMock(t)
	t.Cleanup(mongod.ForgetSpentLimits)
	m.HostFunc = t.Name
	create := m.ContainerCreateFunc
	m.ContainerCreateFunc = func(ctx context.Context, name string, cfg dockerclient.ContainerConfig) (string, []string, error) {
		if strings.HasPrefix(cfg.Image, "docker.io/") {
			return "", nil, dockerclient.ErrNotFound // only the mirror's copy is on the daemon
		}
		return create(ctx, name, cfg)
	}
	rateLimitedOnDockerHub(m, nil)

	c := mustStart(t, m, mongod.DefaultOptions())

	creates := m.CallsTo("ContainerCreate")
	require.NotEmpty(t, creates, "a container is created")
	request, ok := creates[len(creates)-1].Args[1].(dockerclient.ContainerConfig)
	require.True(t, ok, "the create call records its request")
	assert.Equal(t, "public.ecr.aws/docker/library/mongo:8.0", request.Image,
		"the image is not tagged with Docker Hub's name, so the container is created from the mirror's")
	assert.Equal(t, "public.ecr.aws/docker/library/mongo:8.0", c.Image(), "and the container reports the image it runs")
}

func TestAMirrorsCopyOnTheDaemonIsNotPulledAgain(t *testing.T) {
	mongod.SetPullWait(t, noWait)
	m := pullMock(t)
	m.ImageInspectFunc = func(_ context.Context, ref string) (dockerclient.ImageInspect, error) {
		if strings.HasPrefix(ref, "docker.io/") {
			return dockerclient.ImageInspect{}, dockerclient.ErrNotFound
		}
		return dockerclient.ImageInspect{}, nil // an earlier run pulled the mirror's copy
	}
	rateLimitedOnDockerHub(m, nil)

	err := mongod.PullImage(context.Background(), mongod.DefaultOptions().WithDocker(m))

	require.NoError(t, err, "the image is on the daemon, under its mirror's name")
	assert.Equal(t, []string{"docker.io/library/mongo:8.0"}, pulled(m),
		"Docker Hub is asked once, and the mirror's copy is used without downloading it again")
}

func TestAStartUsesTheMirrorsCopyOnTheDaemon(t *testing.T) {
	mongod.SetPullWait(t, noWait)
	m, _ := readyMock(t)
	t.Cleanup(mongod.ForgetSpentLimits)
	m.HostFunc = t.Name
	create := m.ContainerCreateFunc
	m.ContainerCreateFunc = func(ctx context.Context, name string, cfg dockerclient.ContainerConfig) (string, []string, error) {
		if strings.HasPrefix(cfg.Image, "docker.io/") {
			return "", nil, dockerclient.ErrNotFound
		}
		return create(ctx, name, cfg)
	}
	rateLimitedOnDockerHub(m, errors.New("the mirror must not be pulled: its copy is on the daemon"))

	c := mustStart(t, m, mongod.DefaultOptions())

	assert.Equal(t, "public.ecr.aws/docker/library/mongo:8.0", c.Image(), "the copy an earlier run left is run, under the mirror's name")
	assert.Equal(t, []string{"docker.io/library/mongo:8.0"}, pulled(m), "and only Docker Hub is asked")
}

func TestCallersThatNeedARateLimitedImageShareOneMirrorPull(t *testing.T) {
	mongod.SetPullWait(t, noWait)
	m, _ := readyMock(t)
	held := holdPulls(t, m)
	hold := m.ImagePullFunc
	m.ImagePullFunc = func(ctx context.Context, ref string) error {
		if strings.HasPrefix(ref, "docker.io/") {
			return rateLimitedInTheStream
		}
		return hold(ctx, ref)
	}
	opts := mongod.DefaultOptions().WithDocker(m)
	const callers = 4
	started := make(chan *mongod.Container, callers)
	failed := make(chan error, callers)
	for range callers {
		go func() {
			c, err := mongod.StartWithOptions(context.Background(), opts)
			if err != nil {
				failed <- err
				return
			}
			t.Cleanup(func() { _ = c.Stop(context.Background()) })
			started <- c
		}()
	}

	waitForWaiters(t, m, defaultImage, callers)
	held.letGo()

	for range callers {
		select {
		case c := <-started:
			assert.Equal(t, "public.ecr.aws/docker/library/mongo:8.0", c.Image(), "every container runs the mirror's copy")
		case err := <-failed:
			assert.NoError(t, err, "every caller gets a container")
		case <-time.After(10 * time.Second):
			require.Fail(t, "a caller never returned")
		}
	}
	assert.Equal(t, []string{defaultImage, "public.ecr.aws/docker/library/mongo:8.0"}, pulled(m),
		"parallel tests ask Docker Hub once and the mirror once, not once each")
}

func TestATimeoutDuringTheMirrorPullStillNamesTheRateLimit(t *testing.T) {
	mongod.SetPullWait(t, waitForever)
	m := pullMock(t)
	missingImage(m)
	rateLimitedOnDockerHub(m, registryDown)

	err := mongod.PullImage(context.Background(), mongod.DefaultOptions().WithDocker(m).WithPullTimeout(50*time.Millisecond))

	require.ErrorIs(t, err, context.DeadlineExceeded, "the pull ran out of time")
	assert.ErrorIs(t, err, dockerclient.ErrRateLimited, "the caller learns Docker Hub refused it")
	assert.ErrorIs(t, err, registryDown, "and why its mirror was slow")

	mongod.SetPullWait(t, noWait)
	rateLimitedOnDockerHub(m, nil)
	require.NoError(t, mongod.PullImage(context.Background(), mongod.DefaultOptions().WithDocker(m)), "once the mirror recovers, the pull works")
	assert.Equal(t, 1, countOf(pulled(m), defaultImage), "without asking Docker Hub again while its refusal is remembered")
}
