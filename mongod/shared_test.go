package mongod_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/dbtest/dockerclient"
	"github.com/tophergopher/dbtest/dockermock"
	"github.com/tophergopher/dbtest/internal/hostaddr"
	"github.com/tophergopher/dbtest/mongod"
)

// These tests describe a start made from inside a container. TestMain pins
// the dial address, which keeps the published path, so each test unsets it.

// sharedDaemon is a mocked daemon whose container runs on a network this
// process shares, with a stand-in for mongod at its address there.
type sharedDaemon struct {
	mock    *dockermock.Mock
	network string
	address string
	// port is where the stand-in listens. A real container is reached on
	// 27017, which a test cannot rely on having free.
	port    int
	lookups atomic.Int32
}

// onSharedNetwork starts a stand-in for mongod on loopback and returns a
// daemon that reports it as the container's address on network.
func onSharedNetwork(t *testing.T, network string, standIn func(net.Conn)) *sharedDaemon {
	t.Helper()
	d := &sharedDaemon{network: network, address: "127.0.0.1", port: serve(t, standIn)}
	d.mock = runningOn(network, d.address)
	return d
}

// options makes a start find d's network and dial the stand-in's port.
func (d *sharedDaemon) options(base mongod.Options) mongod.Options {
	return base.WithNetworkLookup(sharing(d.network, &d.lookups)).WithNetworkPort(d.port)
}

// runningOn is a mocked daemon whose container is running on network at
// address, with nothing published.
func runningOn(network, address string) *dockermock.Mock {
	return &dockermock.Mock{
		ContainerCreateFunc: func(context.Context, string, dockerclient.ContainerConfig) (string, []string, error) {
			return containerID, nil, nil
		},
		ContainerInspectFunc: func(context.Context, string) (dockerclient.ContainerInspect, error) {
			return dockerclient.ContainerInspect{
				ID:    containerID,
				State: dockerclient.ContainerState{Status: "running", Running: true},
				NetworkSettings: dockerclient.NetworkSettings{Networks: map[string]dockerclient.NetworkEndpoint{
					network: {IPAddress: address},
				}},
			}, nil
		},
	}
}

// sharing is a network lookup that finds network, and counts its calls.
func sharing(network string, calls *atomic.Int32) mongod.NetworkLookup {
	return func(context.Context, dockerclient.Client) (string, error) {
		calls.Add(1)
		return network, nil
	}
}

// unroutable is an address reserved for documentation, so nothing answers
// there.
const unroutable = "192.0.2.10"

func TestAStartInAContainerJoinsItsNetwork(t *testing.T) {
	// Even a user-defined network may be a macvlan or overlay that other
	// machines reach, so both get a password.
	for _, network := range []string{"ci-net", "bridge"} {
		t.Run(network, func(t *testing.T) {
			t.Setenv(mongod.EnvHostIP, "")
			d := onSharedNetwork(t, network, enforcingMongod)

			c := mustStart(t, d.mock, d.options(mongod.DefaultOptions()))

			request := created(t, d.mock)
			require.NotNil(t, request.HostConfig, "the container is put on a network")
			assert.Equal(t, network, request.HostConfig.NetworkMode, "mongod joins the network this process is on")
			assert.Empty(t, request.HostConfig.PortBindings, "nothing is published on the Docker host")
			assert.Equal(t, d.address, c.Host(), "mongod is reached at its address on that network")
			assert.Equal(t, d.port, c.Port(), "on the port it listens on there")
			assert.Equal(t, net.JoinHostPort(d.address, strconv.Itoa(d.port)), parseURI(t, c).Host, "the URI points there too")
			assert.Equal(t, mongod.DefaultUsername, c.Username(), "other containers can connect, so a password is generated")
			assert.NotEmpty(t, c.Password(), "with a password")
		})
	}
}

func TestCredentialOptionsOnASharedNetwork(t *testing.T) {
	cases := []struct {
		name    string
		opts    mongod.Options
		standIn func(net.Conn)
		want    string
	}{
		{"WithAuth is kept", mongod.DefaultOptions().WithAuth("admin", "s3cret"), enforcingMongod, "admin"},
		{"WithoutAuth leaves it open", mongod.DefaultOptions().WithoutAuth(), openMongod, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(mongod.EnvHostIP, "")
			d := onSharedNetwork(t, "ci-net", tc.standIn)

			c := mustStart(t, d.mock, d.options(tc.opts))

			assert.Equal(t, tc.want, c.Username(), "the caller's choice stands, and nothing is generated over it")
			assert.Equal(t, tc.want, parseURI(t, c).User.Username(), "and the URI carries it")
		})
	}
}

func TestWhoCanConnectIsLogged(t *testing.T) {
	t.Run("a shared network, with a password", func(t *testing.T) {
		t.Setenv(mongod.EnvHostIP, "")
		logger := &levelLogger{}
		d := onSharedNetwork(t, "ci-net", enforcingMongod)

		mustStart(t, d.mock, d.options(mongod.DefaultOptions().WithLogger(logger)))

		lines := logger.about("info", "network", "ci-net")
		require.Len(t, lines, 1, "one line says who else can connect, naming the network")
		assert.Equal(t, mongod.DefaultUsername, lines[0].attrs["username"], "and the user they need")
	})
	t.Run("a shared network, without one", func(t *testing.T) {
		t.Setenv(mongod.EnvHostIP, "")
		logger := &levelLogger{}
		d := onSharedNetwork(t, "ci-net", openMongod)

		mustStart(t, d.mock, d.options(mongod.DefaultOptions().WithoutAuth().WithLogger(logger)))

		assert.Len(t, logger.about("warn", "network", "ci-net"), 1, "a warning names the network anyone on it can use")
	})
	t.Run("every interface, without a password", func(t *testing.T) {
		logger := &levelLogger{}

		_ = requestFor(t, mongod.DefaultOptions().WithHostIP(siblingGateway).WithoutAuth().WithLogger(logger).WithName("orders-db"))

		lines := logger.about("warn", "container", "orders-db")
		require.Len(t, lines, 1, "a warning says the open port is reachable")
		assert.NotContains(t, lines[0].attrs, "network", "and names no network, since none is shared")
	})
}

func TestReadinessDialsTheContainersAddressOnTheNetwork(t *testing.T) {
	t.Setenv(mongod.EnvHostIP, "")
	// The port is closed, so each attempt fails at the dial, which names
	// the address dialled.
	m := runningOn("ci-net", "127.0.0.1")
	var lookups atomic.Int32
	opts := mongod.DefaultOptions().WithStartTimeout(300 * time.Millisecond).
		WithNetworkLookup(sharing("ci-net", &lookups)).WithNetworkPort(closedPort(t))

	_, err := start(t, m, opts)

	var dialErr *net.OpError
	require.ErrorAs(t, err, &dialErr, "the last failure is the refused dial")
	addr, ok := dialErr.Addr.(*net.TCPAddr)
	require.True(t, ok, "the dial was over TCP")
	assert.Equal(t, "127.0.0.1", addr.IP.String(), "readiness dials the container's address on the network")
}

func TestAStartOnASharedNetworkNeedsNoPublishedAddress(t *testing.T) {
	t.Setenv(mongod.EnvHostIP, "")
	// As in a container on an internal network, which has no default route.
	unresolved := func(string, string) (hostaddr.Address, error) {
		return hostaddr.Address{}, fmt.Errorf("%w: no default route", hostaddr.ErrUnresolved)
	}
	d := onSharedNetwork(t, "ci-net", enforcingMongod)

	c := mustStart(t, d.mock, d.options(mongod.DefaultOptions()).WithHostResolver(unresolved))

	assert.Equal(t, d.address, c.Host(), "mongod is reached on the network, where no published address is needed")
}

func TestAStartWithNoSharedNetworkNeedsAPublishedAddress(t *testing.T) {
	t.Setenv(mongod.EnvHostIP, "")
	unresolved := func(string, string) (hostaddr.Address, error) {
		return hostaddr.Address{}, fmt.Errorf("%w: no default route", hostaddr.ErrUnresolved)
	}
	noNetwork := func(context.Context, dockerclient.Client) (string, error) {
		return "", hostaddr.ErrNoSharedNetwork
	}
	m, _ := readyMock(t)

	_, err := start(t, m, mongod.DefaultOptions().WithNetworkLookup(noNetwork).WithHostResolver(unresolved))

	assert.ErrorIs(t, err, mongod.ErrUnresolvedHost, "with nothing shared, the address to publish on is needed")
	assert.Empty(t, m.CallsTo("ContainerCreate"), "and nothing is created without it")
}

func TestALoopbackContainerIsNotAnnounced(t *testing.T) {
	logger := &levelLogger{}

	_ = requestFor(t, mongod.DefaultOptions().WithoutAuth().WithLogger(logger).WithName("orders-db"))

	assert.Empty(t, logger.about("warn", "container", "orders-db"), "only this machine can connect, so there is nothing to warn about")
	assert.Empty(t, logger.about("info", "container", "orders-db"), "or to announce")
}

func TestAStartThatFindsNoSharedNetworkPublishesAPort(t *testing.T) {
	t.Setenv(mongod.EnvHostIP, "")
	logger := &levelLogger{}
	noNetwork := func(context.Context, dockerclient.Client) (string, error) {
		return "", fmt.Errorf("%w: not in a container", hostaddr.ErrNoSharedNetwork)
	}

	request := requestFor(t, mongod.DefaultOptions().WithNetworkLookup(noNetwork).WithLogger(logger))

	require.NotNil(t, request.HostConfig, "the port is published")
	assert.Empty(t, request.HostConfig.NetworkMode, "the container stays on the daemon's default network")
	assert.NotEmpty(t, request.HostConfig.PortBindings, "and its port is published, as before")
	var why []logLine
	for _, line := range logger.lines {
		if err, ok := line.attrs["reason"].(error); ok && errors.Is(err, hostaddr.ErrNoSharedNetwork) {
			why = append(why, line)
		}
	}
	require.Len(t, why, 1, "one line says why the port is published")
	assert.Equal(t, "debug", why[0].level, "a fallback is not a problem, so it is logged at debug")
}

func TestOptionsThatNeedAPublishedPortSkipTheSharedNetwork(t *testing.T) {
	cases := map[string]struct {
		opts mongod.Options
		env  string
	}{
		"WithHostIP":     {opts: mongod.DefaultOptions().WithHostIP("127.0.0.1")},
		"DBTEST_HOST_IP": {opts: mongod.DefaultOptions(), env: "127.0.0.1"},
		"WithPort":       {opts: mongod.DefaultOptions().WithPort(closedPort(t))},
		// The certificate must name the address before the container has one.
		"WithTLS": {opts: mongod.DefaultOptions().WithTLS()},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv(mongod.EnvHostIP, tc.env)
			logger := &levelLogger{}
			var lookups atomic.Int32
			opts := tc.opts.WithNetworkLookup(sharing("ci-net", &lookups)).WithLogger(logger)

			request := requestFor(t, opts)

			assert.Zero(t, lookups.Load(), "a caller who chose an address, a host port or TLS keeps the published path")
			require.NotNil(t, request.HostConfig, "the port is published")
			assert.Empty(t, request.HostConfig.NetworkMode, "the container is not moved to another network")
			assert.NotEmpty(t, request.HostConfig.PortBindings, "its port is published")
			assert.Len(t, logger.about("debug", "option", name), 1, "a debug line names the option that kept the port published")
		})
	}
}

func TestAStartOnASharedNetworkWaitsAtTheContainersAddress(t *testing.T) {
	t.Setenv(mongod.EnvHostIP, "")
	m := runningOn("ci-net", unroutable)
	var lookups atomic.Int32
	opts := mongod.DefaultOptions().WithStartTimeout(200 * time.Millisecond).WithNetworkLookup(sharing("ci-net", &lookups))

	_, err := start(t, m, opts)

	var notReady *mongod.NotReadyError
	require.ErrorAs(t, err, &notReady, "nothing answers, so the start runs out of time")
	assert.Equal(t, unroutable, notReady.Host, "the error names the address that was dialled")
	assert.Equal(t, 27017, notReady.Port, "and mongod's own port, since nothing is published")
}

func TestATimeoutOnASharedNetworkNamesTheAddressToPublishOn(t *testing.T) {
	t.Setenv(mongod.EnvHostIP, "")
	// Nothing listens there, as when the network drops traffic.
	m := runningOn("ci-net", "127.0.0.1")
	// Without the shared network, a port would be published and dialled on
	// the daemon's host.
	m.HostFunc = func() string { return "tcp://build-host:2376" }
	var lookups atomic.Int32
	opts := mongod.DefaultOptions().WithStartTimeout(300 * time.Millisecond).
		WithNetworkLookup(sharing("ci-net", &lookups)).WithNetworkPort(closedPort(t))

	_, err := start(t, m, opts)

	require.ErrorIs(t, err, mongod.ErrNotReady, "nothing answers, so the start runs out of time")
	assert.Contains(t, err.Error(), "build-host", "the advice gives the address WithHostIP would need to publish the port instead")
}

func TestAFailedStartOnASharedNetworkIsNotRetriedElsewhere(t *testing.T) {
	t.Setenv(mongod.EnvHostIP, "")
	m := runningOn("ci-net", unroutable)
	refused := errors.New("network ci-net not found")
	m.ContainerStartFunc = func(context.Context, string) error { return refused }
	var lookups atomic.Int32

	_, err := start(t, m, mongod.DefaultOptions().WithNetworkLookup(sharing("ci-net", &lookups)))

	assert.ErrorIs(t, err, refused, "the daemon's refusal is the start's error")
	assert.Equal(t, "ci-net", created(t, m).HostConfig.NetworkMode, "only the container on the shared network was created")
	assert.Equal(t, int32(1), lookups.Load(), "and the network was looked up once")
}
