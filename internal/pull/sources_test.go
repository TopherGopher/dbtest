package pull

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/dbtest/dockerclient"
	"github.com/tophergopher/dbtest/dockermock"
)

const gcrImage = "mirror.gcr.io/library/mongo:8.0"

// officialSources are the mirrors mongod passes for an official image.
var officialSources = []Mirror{OfficialImages, DockerHubCache}

var (
	registryDown = &dockerclient.StatusError{StatusCode: 500, Method: "POST", Path: "/images/create",
		Message: "Get \"https://registry-1.docker.io/v2/\": 503 Service Unavailable"}
	notOnMirror = &dockerclient.StatusError{StatusCode: 404, Method: "POST", Path: "/images/create",
		Message: "manifest unknown"}
	daemonGone = &dockerclient.ConnectionError{Host: "unix:///var/run/docker.sock", Err: errors.New("connection refused")}
)

// bySource is a daemon that has no image and answers each pull by the
// registry the reference names: the error for the first prefix it starts
// with, or success if none matches.
func bySource(t *testing.T, failures map[string]error) *dockermock.Mock {
	t.Cleanup(ForgetSpentLimits)
	SetWait(t, func(context.Context, time.Duration) error { return nil })
	return &dockermock.Mock{
		HostFunc: t.Name,
		ImageInspectFunc: func(context.Context, string) (dockerclient.ImageInspect, error) {
			return dockerclient.ImageInspect{}, dockerclient.ErrNotFound
		},
		ImagePullFunc: func(_ context.Context, ref string) error {
			for prefix, err := range failures {
				if strings.HasPrefix(ref, prefix) {
					return err
				}
			}
			return nil
		},
	}
}

// pulledRefs returns the references m was asked to pull, in order.
func pulledRefs(m *dockermock.Mock) []string {
	var refs []string
	for _, call := range m.CallsTo("ImagePull") {
		refs = append(refs, call.Args[0].(string))
	}
	return refs
}

func TestSourcesAreTriedInOrderUntilOneServesTheImage(t *testing.T) {
	m := bySource(t, map[string]error{"docker.io/": rateLimited, "public.ecr.aws/": notOnMirror})

	ref, err := Shared(context.Background(), m, hubImage, time.Minute, officialSources...)

	require.NoError(t, err, "the last source serves the image")
	assert.Equal(t, gcrImage, ref, "the image is on the daemon under the source it came from")
	assert.Equal(t, []string{hubImage, mirrorImage, gcrImage}, pulledRefs(m), "Docker Hub, then ECR, then mirror.gcr.io")
}

func TestATemporaryFailureMovesOnOnlyAfterItsRetries(t *testing.T) {
	m := bySource(t, map[string]error{"docker.io/": registryDown})

	ref, err := Shared(context.Background(), m, hubImage, time.Minute, officialSources...)

	require.NoError(t, err, "the next source serves the image")
	assert.Equal(t, mirrorImage, ref, "from ECR")
	refs := pulledRefs(m)
	assert.Equal(t, pullAttempts, countRef(refs, hubImage), "Docker Hub is retried before the pull moves on")
	assert.Equal(t, mirrorImage, refs[len(refs)-1], "then ECR is asked")
}

func TestThePullStopsWhereNoOtherSourceCouldHelp(t *testing.T) {
	cases := map[string]struct {
		failures map[string]error
		want     error
		asked    []string
	}{
		"Docker Hub has no such image": {map[string]error{"docker.io/": dockerclient.ErrNotFound},
			dockerclient.ErrNotFound, []string{hubImage}},
		"Docker Hub refuses access": {map[string]error{"docker.io/": dockerclient.ErrUnauthorized},
			dockerclient.ErrUnauthorized, []string{hubImage}},
		"the daemon is gone at Docker Hub": {map[string]error{"docker.io/": daemonGone},
			dockerclient.ErrConnectionFailed, []string{hubImage}},
		"the daemon is gone at ECR": {map[string]error{"docker.io/": rateLimited, "public.ecr.aws/": daemonGone},
			dockerclient.ErrConnectionFailed, []string{hubImage, mirrorImage}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := bySource(t, tc.failures)

			_, err := Shared(context.Background(), m, hubImage, time.Minute, officialSources...)

			assert.ErrorIs(t, err, tc.want, "the failure that stopped the pull")
			assert.Equal(t, tc.asked, pulledRefs(m), "no later source is asked")
		})
	}
}

func TestWhenEverySourceFailsEachFailureIsReported(t *testing.T) {
	m := bySource(t, map[string]error{"docker.io/": rateLimited, "public.ecr.aws/": notOnMirror, "mirror.gcr.io/": registryDown})

	_, err := Shared(context.Background(), m, hubImage, time.Minute, officialSources...)

	var failed *SourcesError
	require.ErrorAs(t, err, &failed, "one error for every source")
	require.Len(t, failed.Failures, 3, "a failure for each source")
	assert.Equal(t, []string{hubImage, mirrorImage, gcrImage},
		[]string{failed.Failures[0].Ref, failed.Failures[1].Ref, failed.Failures[2].Ref}, "in the order they were tried")
	assert.ErrorIs(t, err, dockerclient.ErrRateLimited, "Docker Hub's rate limit")
	assert.ErrorIs(t, err, notOnMirror, "ECR's missing image")
	assert.ErrorIs(t, err, registryDown, "and mirror.gcr.io's failure")
}

func TestAFailureWithNoRateLimitDoesNotMatchIt(t *testing.T) {
	m := bySource(t, map[string]error{"docker.io/": registryDown, "public.ecr.aws/": notOnMirror, "mirror.gcr.io/": notOnMirror})

	_, err := Shared(context.Background(), m, hubImage, time.Minute, officialSources...)

	var failed *SourcesError
	require.ErrorAs(t, err, &failed, "every source failed")
	assert.NotErrorIs(t, err, dockerclient.ErrRateLimited, "no rate limit, so no advice to log in")
}

func TestARateLimitIsRememberedForTheSourceThatRefused(t *testing.T) {
	SetRateLimitMemory(t, time.Hour)
	failures := map[string]error{"docker.io/": rateLimited}
	m := bySource(t, failures)
	_, err := Shared(context.Background(), m, hubImage, time.Minute, officialSources...)
	require.NoError(t, err, "ECR serves the image")
	require.Equal(t, []string{hubImage, mirrorImage}, pulledRefs(m), "after Docker Hub refuses")

	_, err = Shared(context.Background(), m, hubImage, time.Minute, officialSources...)
	require.NoError(t, err, "ECR serves it again")
	assert.Equal(t, []string{hubImage, mirrorImage, mirrorImage}, pulledRefs(m), "without asking Docker Hub again")

	failures["public.ecr.aws/"] = rateLimited
	_, err = Shared(context.Background(), m, hubImage, time.Minute, officialSources...)
	require.NoError(t, err, "mirror.gcr.io serves it")
	_, err = Shared(context.Background(), m, hubImage, time.Minute, officialSources...)
	require.NoError(t, err, "and serves it again")
	assert.Equal(t, []string{hubImage, mirrorImage, mirrorImage, mirrorImage, gcrImage, gcrImage}, pulledRefs(m),
		"once ECR refuses too, it is skipped as well")
}

func TestAMirrorWithoutTheImageIsAskedAgainNextTime(t *testing.T) {
	SetRateLimitMemory(t, time.Hour)
	m := bySource(t, map[string]error{"docker.io/": rateLimited, "public.ecr.aws/": notOnMirror})
	_, err := Shared(context.Background(), m, hubImage, time.Minute, officialSources...)
	require.NoError(t, err, "mirror.gcr.io serves the image")

	_, err = Shared(context.Background(), m, hubImage, time.Minute, officialSources...)

	require.NoError(t, err, "and serves it again")
	assert.Equal(t, 2, countRef(pulledRefs(m), mirrorImage), "only a rate limit is remembered, so ECR is asked again")
}

func TestAnEarlierCopyOnTheDaemonIsUsedBeforePulling(t *testing.T) {
	m := bySource(t, map[string]error{"docker.io/": rateLimited, "public.ecr.aws/": notOnMirror})
	m.ImageInspectFunc = func(_ context.Context, ref string) (dockerclient.ImageInspect, error) {
		if ref == gcrImage {
			return dockerclient.ImageInspect{}, nil
		}
		return dockerclient.ImageInspect{}, dockerclient.ErrNotFound
	}

	ref, err := Shared(context.Background(), m, hubImage, time.Minute, officialSources...)

	require.NoError(t, err, "the image is already on the daemon")
	assert.Equal(t, gcrImage, ref, "under mirror.gcr.io's name")
	assert.Equal(t, []string{hubImage, mirrorImage}, pulledRefs(m), "so mirror.gcr.io is not pulled")
}

// countRef returns how many of refs are ref.
func countRef(refs []string, ref string) int {
	n := 0
	for _, r := range refs {
		if r == ref {
			n++
		}
	}
	return n
}

func TestThePullStopsWhenItsTimeRunsOut(t *testing.T) {
	m := bySource(t, nil)
	m.ImagePullFunc = func(ctx context.Context, ref string) error {
		if strings.HasPrefix(ref, "docker.io/") {
			return rateLimited
		}
		<-ctx.Done() // ECR never answers
		return ctx.Err()
	}

	_, err := Shared(context.Background(), m, hubImage, 50*time.Millisecond, officialSources...)

	var timedOut *TimeoutError
	require.ErrorAs(t, err, &timedOut, "the pull timeout ended it")
	assert.Equal(t, []string{hubImage, mirrorImage}, pulledRefs(m), "mirror.gcr.io is not asked once the time is up")
}
