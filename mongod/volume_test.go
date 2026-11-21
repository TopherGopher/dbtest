package mongod_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/dbtest/dockerclient"
	"github.com/tophergopher/dbtest/dockermock"
	"github.com/tophergopher/dbtest/mongod"
)

func TestWithNamedVolumeKeepsTheDataInThatVolume(t *testing.T) {
	m, _ := readyMock(t)

	c := mustStart(t, m, mongod.DefaultOptions().WithNamedVolume("cart-tests"))

	require.NotNil(t, created(t, m).HostConfig, "the request has host settings")
	assert.Equal(t, []string{"cart-tests:/data/db"}, created(t, m).HostConfig.Binds, "the volume is mounted where mongod keeps its data")
	assert.Equal(t, "cart-tests", c.Volume(), "and the container reports it")
}

func TestWithoutAVolumeOptionNothingIsKept(t *testing.T) {
	m, _ := readyMock(t)

	c := mustStart(t, m, mongod.DefaultOptions())

	assert.Empty(t, created(t, m).HostConfig.Binds, "the data stays in an anonymous volume that Stop removes")
	assert.Empty(t, c.Volume(), "and the container reports no volume")
}

// go test runs each package in its own directory, so the name must be the
// same on every run of a package and differ between packages.
func TestWithDurableStorageNamesTheVolumeAfterTheDirectory(t *testing.T) {
	first := mongod.DefaultOptions().WithDurableStorage()
	again := mongod.DefaultOptions().WithDurableStorage()
	t.Chdir(t.TempDir())
	elsewhere := mongod.DefaultOptions().WithDurableStorage()

	name := volumeOf(t, first)
	assert.Equal(t, name, volumeOf(t, again), "the same directory gets the same volume")
	assert.NotEqual(t, name, volumeOf(t, elsewhere), "another directory gets another")
}

func TestTheLaterVolumeOptionWins(t *testing.T) {
	durable := volumeOf(t, mongod.DefaultOptions().WithDurableStorage())

	assert.Equal(t, "cart-tests", volumeOf(t, mongod.DefaultOptions().WithDurableStorage().WithNamedVolume("cart-tests")),
		"a name given after WithDurableStorage replaces the generated one")
	assert.Equal(t, durable, volumeOf(t, mongod.DefaultOptions().WithNamedVolume("cart-tests").WithDurableStorage()),
		"and WithDurableStorage after a name replaces it")
}

// A volume keeps the root user from its first start, but a generated
// password is new on every start, so the second start could not log in.
func TestAVolumeIsRefusedWhereCredentialsWouldBeGenerated(t *testing.T) {
	for name, opts := range map[string]mongod.Options{
		"WithNamedVolume":    mongod.DefaultOptions().WithNamedVolume("cart-tests"),
		"WithDurableStorage": mongod.DefaultOptions().WithDurableStorage(),
	} {
		t.Run(name, func(t *testing.T) {
			m, _ := readyMock(t)

			_, err := start(t, m, opts.WithHostIP(siblingGateway))

			require.ErrorIs(t, err, mongod.ErrVolumeNeedsCredentials, "the caller must choose the credentials")
			assert.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "as an option mistake")
			assert.Empty(t, m.CallsTo("ContainerCreate"), "before anything is created")
		})
	}
}

func TestAVolumeIsKeptWhereTheCallerChoseTheCredentials(t *testing.T) {
	for name, opts := range map[string]mongod.Options{
		"WithAuth":    mongod.DefaultOptions().WithAuth("admin", "s3cret"),
		"WithoutAuth": mongod.DefaultOptions().WithoutAuth(),
	} {
		t.Run(name, func(t *testing.T) {
			request := requestFor(t, opts.WithNamedVolume("cart-tests").WithHostIP(siblingGateway))

			assert.Equal(t, []string{"cart-tests:/data/db"}, request.HostConfig.Binds, "credentials that stay the same work on a kept volume")
		})
	}
}

func TestVolumeOptionsThatCannotWorkAreRefused(t *testing.T) {
	cases := []struct {
		name string
		opts mongod.Options
		want error
	}{
		{"an empty name", mongod.DefaultOptions().WithNamedVolume(""), mongod.ErrEmptyVolumeName},
		{"a host directory", mongod.DefaultOptions().WithNamedVolume("/srv/mongo"), dockerclient.ErrInvalidArgument},
		{"a relative directory", mongod.DefaultOptions().WithNamedVolume("./data"), dockerclient.ErrInvalidArgument},
		{"a name with a colon", mongod.DefaultOptions().WithNamedVolume("cart:tests"), dockerclient.ErrInvalidArgument},
		{"a named volume for Atlas Local",
			mongod.DefaultOptions().WithMongoImage(mongod.ImageAtlasLocal).WithNamedVolume("cart-tests"), mongod.ErrUnsupportedForImage},
		{"durable storage for Atlas Local",
			mongod.DefaultOptions().WithMongoImage(mongod.ImageAtlasLocal).WithDurableStorage(), mongod.ErrUnsupportedForImage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := readyMock(t)

			_, err := start(t, m, tc.opts)

			require.ErrorIs(t, err, tc.want, "the mistake is reported")
			assert.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "as an invalid argument")
			var fromDocker *dockerclient.InvalidArgumentError
			assert.NotErrorAs(t, err, &fromDocker, "that mongod reports, naming its own option")
			assert.Empty(t, m.CallsTo("ContainerCreate"), "before anything is created")
		})
	}
}

func TestAtlasLocalRefusesTheVolumeOptionThatWasCalled(t *testing.T) {
	for _, option := range []string{"WithNamedVolume", "WithDurableStorage"} {
		t.Run(option, func(t *testing.T) {
			opts := mongod.DefaultOptions().WithMongoImage(mongod.ImageAtlasLocal)
			if option == "WithNamedVolume" {
				opts = opts.WithNamedVolume("cart-tests")
			} else {
				opts = opts.WithDurableStorage()
			}
			m, _ := readyMock(t)

			_, err := start(t, m, opts)

			var unsupported *mongod.UnsupportedForImageError
			require.ErrorAs(t, err, &unsupported, "Atlas Local has not been shown to work on a kept volume")
			assert.Equal(t, option, unsupported.Option, "the error names the option to remove")
		})
	}
}

// volumeOf returns the volume a start with these options mounts.
func volumeOf(t testing.TB, opts mongod.Options) string {
	t.Helper()
	m, _ := readyMock(t)
	c := mustStart(t, m, opts)
	return c.Volume()
}

// Removing a container kills mongod, which loses writes it has acknowledged
// but not yet journaled. On a kept volume a later start would miss them, so
// every removal shuts mongod down cleanly first.
func TestEveryRemovalShutsMongodDownFirstOnAKeptVolume(t *testing.T) {
	removals := map[string]func(*mongod.Container) error{
		"Stop":                  func(c *mongod.Container) error { return c.Stop(context.Background()) },
		"ReapRunningContainers": func(*mongod.Container) error { return mongod.ReapRunningContainers(context.Background()) },
	}
	for name, remove := range removals {
		t.Run(name, func(t *testing.T) {
			m, _ := readyMock(t)
			c := mustStart(t, m, mongod.DefaultOptions().WithNamedVolume("cart-tests"))

			require.NoError(t, remove(c), "the removal succeeds")

			calls := methods(m.Calls())
			require.Contains(t, calls, "ContainerRemove", "the container is removed")
			assert.Contains(t, calls[:slices.Index(calls, "ContainerRemove")], "Exec", "after mongod was asked to shut down")
		})
	}
}

func TestAContainerWithoutAVolumeIsRemovedWithoutAShutdown(t *testing.T) {
	m, _ := readyMock(t)
	c := mustStart(t, m, mongod.DefaultOptions())

	require.NoError(t, c.Stop(context.Background()), "the stop succeeds")

	assert.Empty(t, m.CallsTo("Exec"), "its data goes with it, so there is nothing to keep")
}

// The shutdown is a courtesy: a container whose mongod cannot be shut down
// is still removed.
func TestAShutdownThatFailsDoesNotStopTheRemoval(t *testing.T) {
	m, _ := readyMock(t)
	c := mustStart(t, m, mongod.DefaultOptions().WithNamedVolume("cart-tests"))
	m.ExecFunc = func(context.Context, string, ...string) (dockerclient.ExecResult, error) {
		return dockerclient.ExecResult{}, errors.New("the container is not running")
	}

	require.NoError(t, c.Stop(context.Background()), "the stop succeeds")

	assert.Len(t, m.CallsTo("ContainerRemove"), 1, "and the container is removed")
}

// methods lists the methods of a mocked daemon's calls, in order.
func methods(calls []dockermock.Call) []string {
	names := make([]string, len(calls))
	for i, call := range calls {
		names[i] = call.Method
	}
	return names
}
