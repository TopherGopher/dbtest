package mongod_test

import (
	"context"
	"errors"
	"fmt"
	"net"
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

// nextLoopback hands each test its own loopback address, so that each can
// listen on port 27017, the port a container on a shared network is
// reached at. Linux answers on all of 127.0.0.0/8.
var nextLoopback atomic.Int32

// onSharedNetwork returns a mocked daemon whose container is running on
// network with the given stand-in for mongod at its address there.
func onSharedNetwork(t *testing.T, network string, standIn func(net.Conn)) (*dockermock.Mock, string) {
	t.Helper()
	address := fmt.Sprintf("127.0.81.%d", 1+nextLoopback.Add(1)%250)
	ln, err := net.Listen("tcp", net.JoinHostPort(address, "27017"))
	require.NoError(t, err, "the container's address on the network is where mongod answers")
	t.Cleanup(func() { _ = ln.Close() })
	go acceptAll(ln, standIn)
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
	}, address
}

// sharing is a network lookup that finds network, and counts its calls.
func sharing(network hostaddr.SharedNetwork, calls *atomic.Int32) mongod.NetworkLookup {
	return func(context.Context, dockerclient.Client) (hostaddr.SharedNetwork, error) {
		calls.Add(1)
		return network, nil
	}
}

func TestAStartInAContainerJoinsItsUserDefinedNetwork(t *testing.T) {
	t.Setenv(mongod.EnvHostIP, "")
	m, address := onSharedNetwork(t, "ci-net", openMongod)
	var lookups atomic.Int32
	opts := mongod.DefaultOptions().WithNetworkLookup(sharing(hostaddr.SharedNetwork{Name: "ci-net", UserDefined: true}, &lookups))

	c := mustStart(t, m, opts)

	request := created(t, m)
	require.NotNil(t, request.HostConfig, "the container is put on a network")
	assert.Equal(t, "ci-net", request.HostConfig.NetworkMode, "mongod joins the network this process is on")
	assert.Empty(t, request.HostConfig.PortBindings, "nothing is published on the Docker host")
	assert.Equal(t, address, c.Host(), "mongod is reached at its address on that network")
	assert.Equal(t, 27017, c.Port(), "on its own port")
	assert.Equal(t, net.JoinHostPort(address, "27017"), parseURI(t, c).Host, "the URI points there too")
	assert.Empty(t, c.Username(), "only containers put on a user-defined network can connect, so no password is needed")
}

func TestAStartOnTheDefaultBridgeStillGetsAPassword(t *testing.T) {
	t.Setenv(mongod.EnvHostIP, "")
	m, address := onSharedNetwork(t, "bridge", enforcingMongod)
	var lookups atomic.Int32
	opts := mongod.DefaultOptions().WithNetworkLookup(sharing(hostaddr.SharedNetwork{Name: "bridge"}, &lookups))

	c := mustStart(t, m, opts)

	assert.Equal(t, "bridge", created(t, m).HostConfig.NetworkMode, "mongod joins the default bridge")
	assert.Equal(t, address, c.Host(), "and is reached at its address there")
	assert.Equal(t, mongod.DefaultUsername, c.Username(), "every container on the default bridge can connect, so a password is generated")
}

func TestAStartThatFindsNoSharedNetworkPublishesAPort(t *testing.T) {
	t.Setenv(mongod.EnvHostIP, "")
	logger := &levelLogger{}
	noNetwork := func(context.Context, dockerclient.Client) (hostaddr.SharedNetwork, error) {
		return hostaddr.SharedNetwork{}, fmt.Errorf("%w: not in a container", hostaddr.ErrNoSharedNetwork)
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
			var lookups atomic.Int32
			opts := tc.opts.WithNetworkLookup(sharing(hostaddr.SharedNetwork{Name: "ci-net", UserDefined: true}, &lookups))

			request := requestFor(t, opts)

			assert.Zero(t, lookups.Load(), "a caller who chose an address, a host port or TLS keeps the published path")
			require.NotNil(t, request.HostConfig, "the port is published")
			assert.Empty(t, request.HostConfig.NetworkMode, "the container is not moved to another network")
			assert.NotEmpty(t, request.HostConfig.PortBindings, "its port is published")
		})
	}
}

func TestAStartOnASharedNetworkWaitsAtTheContainersAddress(t *testing.T) {
	t.Setenv(mongod.EnvHostIP, "")
	m, address := onSharedNetwork(t, "ci-net", func(conn net.Conn) { _ = conn.Close() })
	var lookups atomic.Int32
	opts := mongod.DefaultOptions().WithStartTimeout(200 * time.Millisecond).
		WithNetworkLookup(sharing(hostaddr.SharedNetwork{Name: "ci-net", UserDefined: true}, &lookups))

	_, err := start(t, m, opts)

	var notReady *mongod.NotReadyError
	require.ErrorAs(t, err, &notReady, "nothing answers, so the start runs out of time")
	assert.Equal(t, address, notReady.Host, "the error names the address that was dialled")
	assert.Equal(t, 27017, notReady.Port, "and the container's own port")
}
