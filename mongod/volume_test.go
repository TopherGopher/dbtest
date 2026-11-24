package mongod_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/dbtest/dockerclient"
	"github.com/tophergopher/dbtest/dockermock"
	"github.com/tophergopher/dbtest/mongod"
)

func TestWithNamedVolumeKeepsTheDataInThatVolume(t *testing.T) {
	m, _ := readyMock(t)

	c := mustStart(t, m, mongod.DefaultOptions().WithNamedVolume("cart-tests"))

	request := created(t, m)
	require.NotNil(t, request.HostConfig, "the request has host settings")
	assert.Equal(t, []dockerclient.VolumeMount{{Name: "cart-tests", Path: "/data/db"}}, request.HostConfig.Volumes,
		"the volume is mounted where mongod keeps its data")
	assert.Equal(t, "cart-tests", c.Volume(), "and the container reports it")
}

func TestWithoutAVolumeNothingIsKept(t *testing.T) {
	m, _ := readyMock(t)

	c := mustStart(t, m, mongod.DefaultOptions())

	assert.Empty(t, created(t, m).HostConfig.Volumes, "the data stays in an anonymous volume that Stop removes")
	assert.Empty(t, c.Volume(), "and the container reports no volume")
}

func TestTheLaterVolumeNameWins(t *testing.T) {
	m, _ := readyMock(t)

	c := mustStart(t, m, mongod.DefaultOptions().WithNamedVolume("first").WithNamedVolume("second"))

	assert.Equal(t, "second", c.Volume(), "options apply in the order given")
}

// Docker removes only anonymous volumes with a container, so Stop's removal
// must ask for no more than that for a named volume to be kept.
func TestStopRemovesOnlyTheAnonymousVolumes(t *testing.T) {
	m, _ := readyMock(t)
	c := mustStart(t, m, mongod.DefaultOptions().WithNamedVolume("cart-tests"))

	require.NoError(t, c.Stop(context.Background()), "stopping succeeds")

	removals := m.CallsTo("ContainerRemove")
	require.Len(t, removals, 1, "the container is removed once")
	assert.Equal(t, dockerclient.RemoveOptions{Force: true, RemoveVolumes: true}, removals[0].Args[1],
		"with the removal that keeps named volumes")
}

// A volume keeps the root user from its first start, but a generated
// password is new on every start, so the second start could not log in.
func TestAVolumeIsRefusedWhereCredentialsWouldBeGenerated(t *testing.T) {
	m, _ := readyMock(t)

	_, err := start(t, m, mongod.DefaultOptions().WithNamedVolume("cart-tests").WithHostIP(siblingGateway).WithStartTimeout(time.Second))

	require.ErrorIs(t, err, mongod.ErrVolumeNeedsCredentials, "the caller must choose the credentials")
	assert.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "as an option mistake")
	var needs *mongod.VolumeNeedsCredentialsError
	require.ErrorAs(t, err, &needs, "with the details")
	assert.Equal(t, "cart-tests", needs.Volume, "naming the volume")
	assert.NotEmpty(t, needs.Reach, "and saying who else could connect")
	assert.Empty(t, m.CallsTo("ContainerCreate"), "before anything is created")
}

func TestAVolumeIsKeptWhereTheCallerChoseTheCredentials(t *testing.T) {
	for name, opts := range map[string]mongod.Options{
		"WithAuth":    mongod.DefaultOptions().WithAuth("admin", "s3cret"),
		"WithoutAuth": mongod.DefaultOptions().WithoutAuth(),
	} {
		t.Run(name, func(t *testing.T) {
			request := requestFor(t, opts.WithNamedVolume("cart-tests").WithHostIP(siblingGateway))

			assert.Len(t, request.HostConfig.Volumes, 1, "credentials that stay the same work on a kept volume")
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
		{"Atlas Local", mongod.DefaultOptions().WithMongoImage(mongod.ImageAtlasLocal).WithNamedVolume("cart-tests"), mongod.ErrUnsupportedForImage},
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

func TestAtlasLocalNamesTheVolumeOptionToRemove(t *testing.T) {
	m, _ := readyMock(t)

	_, err := start(t, m, mongod.DefaultOptions().WithMongoImage(mongod.ImageAtlasLocal).WithNamedVolume("cart-tests"))

	var unsupported *mongod.UnsupportedForImageError
	require.ErrorAs(t, err, &unsupported, "Atlas Local names its replica set after a container that changes every start")
	assert.Equal(t, "WithNamedVolume", unsupported.Option, "the error names the option to remove")
}

// A volume keeps the root user of its first start, so other credentials
// later pass the readiness check, which logs nobody in, and would fail at
// the caller's first command instead.
func TestCredentialsAVolumeDoesNotKeepAreReportedByTheStart(t *testing.T) {
	m, _ := authMock(t)
	shell(m, dockerclient.ExecResult{ExitCode: 1, Stderr: "Authentication failed."}, nil)

	_, err := start(t, m, mongod.DefaultOptions().WithNamedVolume("cart-tests").WithAuth("admin", "other"))

	require.ErrorIs(t, err, mongod.ErrVolumeCredentialsChanged, "the start fails, not the caller's first command")
	var changed *mongod.VolumeCredentialsChangedError
	require.ErrorAs(t, err, &changed, "with the details")
	assert.Equal(t, "cart-tests", changed.Volume, "naming the volume")
	assert.Len(t, m.CallsTo("ContainerRemove"), 1, "and the container is removed")
}

func TestCredentialsAVolumeKeepsAreChecked(t *testing.T) {
	m, _ := authMock(t)
	commands := shell(m, dockerclient.ExecResult{Stdout: "1\n"}, nil)

	_, err := start(t, m, mongod.DefaultOptions().WithNamedVolume("cart-tests").WithAuth("admin", "s3cret"))

	require.NoError(t, err, "the root user logs in")
	assert.Len(t, *commands, 1, "after one check")
}

func TestCredentialsAreNotCheckedWithoutAVolume(t *testing.T) {
	m, _ := authMock(t)
	commands := shell(m, dockerclient.ExecResult{}, nil)

	mustStart(t, m, mongod.DefaultOptions().WithAuth("admin", "s3cret"))

	assert.Empty(t, *commands, "a new data directory has the user the start asked for")
}

// mongod will not open a data directory another mongod holds, and parallel
// tests that share a volume name are the usual cause.
func TestAStartOnAVolumeThatExitsSaysWhatToTry(t *testing.T) {
	for name, volume := range map[string]bool{"with a volume": true, "without a volume": false} {
		t.Run(name, func(t *testing.T) {
			opts := mongod.DefaultOptions()
			if volume {
				opts = opts.WithNamedVolume("cart-tests")
			}
			m, _ := readyMock(t)
			m.ContainerInspectFunc = func(context.Context, string) (dockerclient.ContainerInspect, error) {
				return dockerclient.ContainerInspect{ID: containerID, State: dockerclient.ContainerState{Status: "exited", ExitCode: 100}}, nil
			}

			_, err := start(t, m, opts)

			var exited *mongod.ContainerExitedError
			require.ErrorAs(t, err, &exited, "mongod exited")
			assert.Equal(t, volume, exited.Advice != "", "advice is given only when a volume may be in use")
		})
	}
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
			started := len(m.Calls())

			require.NoError(t, remove(c), "the removal succeeds")

			calls := methods(m.Calls()[started:])
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

// A start that failed never served anything, so there is nothing to shut
// down, and a warning about lost writes would mislead.
func TestAFailedStartOnAVolumeIsRemovedWithoutAShutdown(t *testing.T) {
	m, _ := readyMock(t)
	m.ContainerStartFunc = func(context.Context, string) error { return errors.New("no") }
	logger := &levelLogger{}

	_, err := start(t, m, mongod.DefaultOptions().WithNamedVolume("cart-tests").WithLogger(logger))

	require.Error(t, err, "the start fails")
	assert.Empty(t, m.CallsTo("Exec"), "and its container is removed without a shutdown")
	assert.Empty(t, logger.at("warn"), "or a warning")
}

// When mongod, the container's first process, exits, Docker kills the
// command that asked it to: the exec ends with 137 although the shutdown
// was clean. The container having exited is what counts.
func TestAShutdownIsJudgedByTheContainer(t *testing.T) {
	m, _ := readyMock(t)
	logger := &levelLogger{}
	c := mustStart(t, m, mongod.DefaultOptions().WithNamedVolume("cart-tests").WithLogger(logger))
	m.ExecFunc = func(context.Context, string, ...string) (dockerclient.ExecResult, error) {
		m.ContainerInspectFunc = func(context.Context, string) (dockerclient.ContainerInspect, error) {
			return dockerclient.ContainerInspect{ID: containerID, State: dockerclient.ContainerState{Status: "exited"}}, nil
		}
		return dockerclient.ExecResult{ExitCode: 137}, nil
	}

	require.NoError(t, c.Stop(context.Background()), "the stop succeeds")

	assert.Empty(t, logger.at("warn"), "a shutdown that stopped the container is not a failure")
}

// The shutdown is a courtesy: a container whose mongod cannot be shut down
// is still removed, and the caller is told the last writes may be lost.
func TestAShutdownThatFailsDoesNotStopTheRemoval(t *testing.T) {
	m, _ := readyMock(t)
	logger := &levelLogger{}
	c := mustStart(t, m, mongod.DefaultOptions().WithNamedVolume("cart-tests").WithLogger(logger))
	m.ExecFunc = func(context.Context, string, ...string) (dockerclient.ExecResult, error) {
		return dockerclient.ExecResult{}, errors.New("the daemon refused")
	}

	require.NoError(t, c.Stop(context.Background()), "the stop succeeds")

	assert.Len(t, m.CallsTo("ContainerRemove"), 1, "the container is removed")
	assert.Len(t, logger.at("warn"), 1, "after a warning that the volume may lack the last writes")
}

// A shutdown that hangs must leave the removal time to run, and a Stop
// tried again must not wait for it a second time.
func TestAShutdownThatHangsDoesNotStopTheRemoval(t *testing.T) {
	m, _ := readyMock(t)
	c := mustStart(t, m, mongod.DefaultOptions().WithNamedVolume("cart-tests"))
	m.ExecFunc = func(ctx context.Context, _ string, _ ...string) (dockerclient.ExecResult, error) {
		<-ctx.Done()
		return dockerclient.ExecResult{}, ctx.Err()
	}
	// Like a daemon, the removal fails once its context has ended.
	m.ContainerRemoveFunc = func(ctx context.Context, _ string, _ dockerclient.RemoveOptions) error { return ctx.Err() }
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	require.NoError(t, c.Stop(ctx), "the stop removes the container within its deadline")
	require.NoError(t, c.Stop(ctx), "and a second stop has nothing left to do")

	assert.Len(t, m.CallsTo("Exec"), 1, "the shutdown is tried once")
}

// methods lists the methods of a mocked daemon's calls, in order.
func methods(calls []dockermock.Call) []string {
	names := make([]string, len(calls))
	for i, call := range calls {
		names[i] = call.Method
	}
	return names
}
