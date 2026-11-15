package mongod_test

import (
	"context"
	"errors"
	"testing"

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
