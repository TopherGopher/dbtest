package pull

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockermock"
)

const (
	hubImage    = "docker.io/library/mongo:8.0"
	mirrorImage = "public.ecr.aws/docker/library/mongo:8.0"
)

var rateLimited = &dockerclient.PullError{Ref: hubImage,
	Message: "error from registry: You have reached your unauthenticated pull rate limit. https://www.docker.com/increase-rate-limit"}

// limitedMock is a daemon that has no image, whose Docker Hub pulls the rate
// limit refuses, and whose mirror pulls end as mirror decides.
func limitedMock(t *testing.T, mirror func(ctx context.Context) error) *dockermock.Mock {
	t.Cleanup(ForgetSpentLimits)
	SetWait(t, func(context.Context, time.Duration) error { return nil })
	return &dockermock.Mock{
		HostFunc: t.Name,
		ImageInspectFunc: func(context.Context, string) (dockerclient.ImageInspect, error) {
			return dockerclient.ImageInspect{}, dockerclient.ErrNotFound
		},
		ImagePullFunc: func(ctx context.Context, ref string) error {
			if strings.HasPrefix(ref, "docker.io/") {
				return rateLimited
			}
			return mirror(ctx)
		},
	}
}

func rememberedUntil(t *testing.T, host string) time.Time {
	t.Helper()
	pulls.Lock()
	defer pulls.Unlock()
	spent := pulls.spent[pullKey{host: host, image: hubImage}]
	require.NotNil(t, spent, "the refusal is remembered")
	return spent.until
}

func TestPullsThroughTheMirrorDoNotExtendTheRefusal(t *testing.T) {
	SetRateLimitMemory(t, time.Hour)
	m := limitedMock(t, func(context.Context) error { return nil })
	_, err := Shared(context.Background(), m, hubImage, time.Minute, OfficialImages)
	require.NoError(t, err, "the first pull comes from the mirror")
	until := rememberedUntil(t, m.Host())

	_, err = Shared(context.Background(), m, hubImage, time.Minute, OfficialImages)

	require.NoError(t, err, "so does the next")
	assert.Equal(t, until, rememberedUntil(t, m.Host()),
		"Docker Hub is asked again once the refusal is forgotten, however often the mirror is used meanwhile")
}

func TestACallerThatLeavesDuringTheMirrorPullIsToldOfTheLimit(t *testing.T) {
	mirrorStarted := make(chan struct{})
	m := limitedMock(t, func(ctx context.Context) error {
		close(mirrorStarted)
		<-ctx.Done()
		return ctx.Err()
	})
	starterCtx, stopStarter := context.WithCancel(context.Background())
	starter := make(chan error, 1)
	go func() {
		_, err := Shared(starterCtx, m, hubImage, 10*time.Second, OfficialImages)
		starter <- err
	}()
	<-mirrorStarted

	_, err := Shared(context.Background(), m, hubImage, 50*time.Millisecond, OfficialImages)

	require.ErrorIs(t, err, context.DeadlineExceeded, "a caller whose own time runs out leaves")
	assert.ErrorIs(t, err, dockerclient.ErrRateLimited, "knowing that the rate limit is why it had to wait for the mirror")
	stopStarter()
	<-starter
}
