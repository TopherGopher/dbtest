package postgres_test

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/dbtest/dockerclient"
	"github.com/tophergopher/dbtest/internal/reaper"
	"github.com/tophergopher/dbtest/postgres"
)

func TestStartWithNoOptions(t *testing.T) {
	m, port := readyMock(t)

	c := mustStart(t, m, postgres.DefaultOptions())

	name, request := created(t, m)
	binding := request.HostConfig.PortBindings["5432/tcp"][0]
	assert.Equal(t, "docker.io/library/postgres:18", request.Image, "the default image, under its full name")
	assert.Equal(t, request.Image, c.Image(), "which the container reports")
	assert.Equal(t, "127.0.0.1", binding.HostIP, "a local container is not published to the network")
	assert.Empty(t, binding.HostPort, "the daemon chooses the port, so parallel starts cannot collide")
	assert.Equal(t, port, c.Port(), "and the container reports the one it chose")
	assert.Equal(t, "127.0.0.1", c.Host(), "where the port is published")
	assert.Equal(t, "regression", request.Labels["mongotest"], "every container can be found by the label the README names")
	assert.Equal(t, name, c.Name(), "the container reports its name")
	assert.True(t, strings.HasPrefix(name, "dbtest-postgres-"), "a generated name says what made the container")
	assert.Equal(t, containerID, c.ID(), "and the id the daemon gave it")
	assert.Contains(t, request.Env, "POSTGRES_PASSWORD="+c.Password(), "the image creates the superuser with the password the container reports")
	assert.Empty(t, request.Cmd, "the image's own command runs, so an image built on it keeps its settings")
}

func TestEachStartHasItsOwnNameAndPassword(t *testing.T) {
	first, _ := readyMock(t)
	second, _ := readyMock(t)
	a := mustStart(t, first, postgres.DefaultOptions())
	b := mustStart(t, second, postgres.DefaultOptions())

	assert.NotEqual(t, a.Name(), b.Name(), "parallel starts must not ask for the same name")
	assert.NotEmpty(t, a.Password(), "the image refuses to start without a password")
	assert.NotEqual(t, a.Password(), b.Password(), "a password is generated for each container")
}

// A driver given the URI must connect as the container's superuser, to its
// database, without TLS, which the image does not serve.
func TestTheURIConnectsAsTheSuperuser(t *testing.T) {
	m, port := readyMock(t)
	c := mustStart(t, m, postgres.DefaultOptions())

	u, err := url.Parse(c.URI())
	require.NoError(t, err, "the URI parses")
	password, _ := u.User.Password()
	assert.Equal(t, "postgres", u.Scheme, "a scheme every PostgreSQL driver accepts")
	assert.Equal(t, c.Username(), u.User.Username(), "as the superuser")
	assert.Equal(t, c.Password(), password, "with its password")
	assert.Equal(t, net.JoinHostPort("127.0.0.1", fmt.Sprint(port)), u.Host, "at the address the server answered on")
	assert.Equal(t, "/"+c.Database(), u.Path, "to the database that exists")
	assert.Equal(t, "disable", u.Query().Get("sslmode"), "without TLS, which lib/pq would otherwise insist on")
}

// libpq and pgx fill in from PGHOST, PGPASSWORD, PGSSLMODE and the rest any
// part a connection string leaves out, so a shell's leftovers could point a
// test at another server. The URI must leave out none of them.
func TestTheURISpellsOutWhatPGVariablesWouldFillIn(t *testing.T) {
	for _, name := range []string{"PGHOST", "PGPORT", "PGUSER", "PGPASSWORD", "PGDATABASE", "PGSSLMODE"} {
		t.Setenv(name, "from-the-shell")
	}
	m, _ := readyMock(t)
	c := mustStart(t, m, postgres.DefaultOptions())

	u, err := url.Parse(c.URI())
	require.NoError(t, err, "the URI parses")
	password, hasPassword := u.User.Password()
	assert.NotEmpty(t, u.Hostname(), "the host is given, not left to PGHOST")
	assert.NotEmpty(t, u.Port(), "the port is given, not left to PGPORT")
	assert.NotEmpty(t, u.User.Username(), "the user is given, not left to PGUSER")
	assert.True(t, hasPassword && password != "", "the password is given, not left to PGPASSWORD")
	assert.NotEqual(t, "/", u.Path, "the database is given, not left to PGDATABASE")
	assert.NotEmpty(t, u.Query().Get("sslmode"), "the TLS mode is given, not left to PGSSLMODE")
	assert.NotContains(t, c.URI(), "from-the-shell", "and nothing comes from the environment")
}

// The server answers "starting up" before it accepts connections; a start
// that returned then would hand the caller a server that refuses them.
func TestStartWaitsWhileTheServerIsStartingUp(t *testing.T) {
	var connections atomic.Int32
	m, _ := servingMock(t, func(conn net.Conn) {
		if connections.Add(1) <= 3 {
			startingUp(conn)
			return
		}
		askPassword(conn)
	})

	_, err := start(t, m, postgres.DefaultOptions())

	require.NoError(t, err, "the start succeeds once the server accepts connections")
	assert.Greater(t, connections.Load(), int32(3), "and not before")
}

func TestStartTimesOutWhenNothingAnswers(t *testing.T) {
	m, _ := servingMock(t, hangUp)

	_, err := start(t, m, postgres.DefaultOptions().WithStartTimeout(300*time.Millisecond))

	assert.ErrorIs(t, err, postgres.ErrNotReady, "a server that never answers is not ready")
	assert.ErrorIs(t, err, context.DeadlineExceeded, "because the start timeout ended")
	assert.ErrorIs(t, err, postgres.ErrNoReply, "and the error says nothing answered at the address")
	var notReady *postgres.NotReadyError
	require.ErrorAs(t, err, &notReady, "the details are a NotReadyError")
	assert.False(t, notReady.ContextEnded, "the start timeout ended it, not the caller")
	assert.Equal(t, 300*time.Millisecond, notReady.Timeout, "and it says how long it waited")
}

func TestATimeoutWhileStartingUpKeepsTheServersReason(t *testing.T) {
	m, _ := servingMock(t, startingUp)

	_, err := start(t, m, postgres.DefaultOptions().WithStartTimeout(300*time.Millisecond))

	assert.ErrorIs(t, err, postgres.ErrNotReady, "a server that never finishes starting is not ready")
	assert.ErrorIs(t, err, postgres.ErrStartingUp, "and the error says it was still starting up")
}

func TestStartStopsWaitingWhenTheCallerCancels(t *testing.T) {
	m, _ := servingMock(t, hangUp)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := postgres.StartWithOptions(ctx, postgres.DefaultOptions().WithDocker(m))

	var notReady *postgres.NotReadyError
	require.ErrorAs(t, err, &notReady, "the start ends not ready")
	assert.True(t, notReady.ContextEnded, "and says the caller's context ended it")
}

func TestAFailedStartRemovesItsContainer(t *testing.T) {
	m, _ := servingMock(t, hangUp)

	_, err := start(t, m, postgres.DefaultOptions().WithStartTimeout(100*time.Millisecond))

	require.Error(t, err, "the start fails")
	name, _ := created(t, m)
	assert.Len(t, m.CallsTo("ContainerRemove"), 1, "and leaves nothing behind")
	assert.NotContains(t, reaper.Names(), name, "nor anything registered for reaping")
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
	logger := &levelLogger{}

	_, err := start(t, m, postgres.DefaultOptions().WithLogger(logger))

	require.NoError(t, err, "a missing image is pulled, not reported")
	require.Len(t, m.CallsTo("ImagePull"), 1, "once")
	assert.Equal(t, "docker.io/library/postgres:18", m.CallsTo("ImagePull")[0].Args[0], "the image that was asked for")
	assert.NotEmpty(t, logger.at("info"), "a pull, which can take minutes, is announced where a first-time user sees it")
}

func TestStopRemovesTheContainerAndItsVolumes(t *testing.T) {
	m, _ := readyMock(t)
	c := mustStart(t, m, postgres.DefaultOptions())

	require.NoError(t, c.Stop(context.Background()), "stopping succeeds")
	require.NoError(t, c.Stop(context.Background()), "a second Stop is harmless")

	removes := m.CallsTo("ContainerRemove")
	require.Len(t, removes, 1, "the daemon is asked once")
	opts, _ := removes[0].Args[1].(dockerclient.RemoveOptions)
	assert.True(t, opts.Force, "a running container is removed")
	assert.True(t, opts.RemoveVolumes, "with the anonymous volume that holds its data")
}

func TestStopOnANilContainerDoesNothing(t *testing.T) {
	var c *postgres.Container
	assert.NoError(t, c.Stop(context.Background()), "a cleanup can run before the start succeeded")
}

func TestReapRunningContainersRemovesWhatIsStillUp(t *testing.T) {
	m, _ := readyMock(t)
	c := mustStart(t, m, postgres.DefaultOptions())
	require.Contains(t, reaper.Names(), c.Name(), "a running container is registered")

	require.NoError(t, postgres.ReapRunningContainers(context.Background()), "reaping succeeds")

	assert.Len(t, m.CallsTo("ContainerRemove"), 1, "the container is removed")
	assert.NotContains(t, reaper.Names(), c.Name(), "and no longer registered")
}

func TestPrintingAContainerHidesThePassword(t *testing.T) {
	m, _ := readyMock(t)
	c := mustStart(t, m, postgres.DefaultOptions())

	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		printed := fmt.Sprintf(verb, c)
		assert.NotContains(t, printed, c.Password(), "logging a container with %s must not leak its password", verb)
		assert.Contains(t, printed, c.Name(), "but it says which container it is")
	}
	assert.NotContains(t, fmt.Sprint(nil, (*postgres.Container)(nil)), c.Password(), "a nil container prints too")
}

func TestTheReadyLineHidesThePassword(t *testing.T) {
	m, _ := readyMock(t)
	logger := &levelLogger{}
	c := mustStart(t, m, postgres.DefaultOptions().WithLogger(logger))

	for _, line := range logger.at("info") {
		for _, value := range line.attrs {
			assert.NotContains(t, fmt.Sprint(value), c.Password(), "logs are shared more widely than the daemon")
		}
	}
}

// Inside a container on the same daemon, the server joins this process's
// network and is dialled there, with nothing published.
func TestAShareableNetworkIsJoined(t *testing.T) {
	port := serve(t, askPassword)
	m, _ := readyMock(t)
	m.ContainerInspectFunc = func(context.Context, string) (dockerclient.ContainerInspect, error) {
		info := running(0)
		info.NetworkSettings.Networks = map[string]dockerclient.NetworkEndpoint{"dbtest-net": {IPAddress: "127.0.0.1"}}
		return info, nil
	}
	t.Setenv(postgres.EnvHostIP, "")
	opts := postgres.DefaultOptions().
		WithNetworkLookup(func(context.Context, dockerclient.Client) (string, error) { return "dbtest-net", nil }).
		WithNetworkPort(port)

	c := mustStart(t, m, opts)

	_, request := created(t, m)
	assert.Equal(t, "dbtest-net", request.HostConfig.NetworkMode, "the container joins the network")
	assert.Empty(t, request.HostConfig.PortBindings, "and nothing is published")
	assert.Equal(t, port, c.Port(), "it is dialled at its address there")
}

// WithPort chooses a host port, which only publishing can honour.
func TestWithPortPublishesEvenWhereANetworkIsShared(t *testing.T) {
	m, _ := readyMock(t)
	opts := postgres.DefaultOptions().WithPort(15432).
		WithNetworkLookup(func(context.Context, dockerclient.Client) (string, error) { return "dbtest-net", nil })

	_, err := start(t, m, opts)

	require.NoError(t, err, "the start succeeds")
	_, request := created(t, m)
	assert.Empty(t, request.HostConfig.NetworkMode, "no network is joined")
	assert.Equal(t, "15432", request.HostConfig.PortBindings["5432/tcp"][0].HostPort, "the port is published where asked")
}
