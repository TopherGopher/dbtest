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

// rateLimitedOnDockerHub makes m's pulls from Docker Hub fail on its rate
// limit, and its pulls from anywhere else end with mirrorErr.
func rateLimitedOnDockerHub(m *dockermock.Mock, mirrorErr error) {
	m.ImagePullFunc = func(_ context.Context, ref string) error {
		if strings.HasPrefix(ref, "docker.io/") {
			return rateLimitedInTheStream
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
	cases := map[string]struct{ image, hub, mirror string }{
		"the official image": {"8.0",
			"docker.io/library/mongo:8.0", "public.ecr.aws/docker/library/mongo:8.0"},
		"the official image by digest": {"mongo@" + digest,
			"docker.io/library/mongo@" + digest, "public.ecr.aws/docker/library/mongo@" + digest},
		"MongoDB's community image": {"mongodb/mongodb-community-server:8.0-ubi9",
			"docker.io/mongodb/mongodb-community-server:8.0-ubi9", "quay.io/mongodb/mongodb-community-server:8.0-ubi9"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			mongod.SetPullWait(t, noWait)
			m := pullMock(t)
			missingImage(m)
			rateLimitedOnDockerHub(m, nil)

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
	assert.Equal(t, []string{"docker.io/library/mongo:8.0", "public.ecr.aws/docker/library/mongo:8.0", "public.ecr.aws/docker/library/mongo:8.0"}, pulled(m),
		"without asking Docker Hub again while its limit is known to be spent")
}

func TestAnImageWithNoMirrorIsNotMirrored(t *testing.T) {
	for _, image := range []string{"bitnami/mongodb:8.0", "registry.example.com/team/mongo:8.0"} {
		t.Run(image, func(t *testing.T) {
			mongod.SetPullWait(t, noWait)
			m := pullMock(t)
			missingImage(m)
			m.ImagePullFunc = func(context.Context, string) error { return rateLimitedInTheStream }

			err := mongod.PullImage(context.Background(), mongod.DefaultOptions().WithDocker(m).WithImage(image))

			require.ErrorIs(t, err, dockerclient.ErrRateLimited, "the rate limit is reported as before")
			for _, ref := range pulled(m) {
				assert.NotContains(t, ref, "public.ecr.aws", "only Docker Hub's official and MongoDB images have a known mirror")
				assert.NotContains(t, ref, "quay.io", "only Docker Hub's official and MongoDB images have a known mirror")
			}
		})
	}
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
