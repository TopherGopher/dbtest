package mongod_test

import (
	"context"
	"net"
	"net/url"
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/dbtest/dockerclient"
	"github.com/tophergopher/dbtest/mongod"
)

// EnvReachSetup names the setup a CI job runs this test in: sibling,
// host-network, dind or tcp. The test then checks that a start took the
// path that setup needs, so a missing or broken setup fails it. Unset, it
// checks only that mongod answers and that nothing is left behind.
const EnvReachSetup = "DBTEST_REACH_SETUP"

// EnvReachGateway is, in the sibling setup, the gateway of the test
// container's network, which a start with WithTLS must dial. The script
// that makes the setup works it out from the daemon.
const EnvReachGateway = "DBTEST_REACH_GATEWAY"

// TestIntegrationReach starts mongod with no options wherever the tests
// run, talks to it, and checks that stopping it leaves nothing behind.
func TestIntegrationReach(t *testing.T) {
	setup := os.Getenv(EnvReachSetup)
	require.Contains(t, []string{"", "sibling", "host-network", "dind", "tcp"}, setup,
		"%s names a setup this test knows: sibling, host-network, dind or tcp", EnvReachSetup)
	// Where containers are reached is the point here, so the address is
	// left to the start, or to whatever the runner set.
	t.Setenv(mongod.EnvHostIP, callerHostIP)
	docker, ctx := liveDocker(t)
	key, value := runLabel(t)
	opts := mongod.DefaultOptions().WithLabel(key, value)

	c := startAndTalk(t, ctx, opts)
	info, err := docker.ContainerInspect(ctx, c.ID())
	require.NoError(t, err, "the running container can be inspected")
	published := info.HostPort("27017/tcp") != ""
	switch setup {
	case "sibling":
		assert.False(t, published, "a test in a container on the same daemon reaches mongod on its network")
		assert.Contains(t, networkAddresses(info), c.Host(), "at mongod's address on that network")
		assert.Equal(t, 27017, c.Port(), "on mongod's own port")
		assert.Equal(t, mongod.DefaultUsername, c.Username(), "other containers on the network can connect, so it has a password")
	case "host-network":
		assert.True(t, published, "a test on the host's network reaches a published port")
		assert.Equal(t, "127.0.0.1", c.Host(), "on loopback")
	case "dind", "tcp":
		assert.True(t, published, "a test reaches a published port")
		assert.Equal(t, daemonHostname(t, docker), c.Host(), "on the daemon's own host")
	}
	stopAndCheckGone(t, ctx, docker, c)

	if setup == "sibling" {
		checkSiblingWithTLS(t, ctx, docker, opts)
	}
	if ids, checked := containersLabelled(t, key, value); checked {
		assert.Empty(t, ids, "no container this test started is left")
	}
}

// networkAddresses returns a container's addresses on its networks.
func networkAddresses(info dockerclient.ContainerInspect) []string {
	var addresses []string
	for _, endpoint := range info.NetworkSettings.Networks {
		addresses = append(addresses, endpoint.IPAddress)
	}
	return addresses
}

// checkSiblingWithTLS starts mongod with TLS from a sibling container.
// WithTLS keeps the published path, so the start reaches it at the default
// gateway, the one rule no other setup uses.
func checkSiblingWithTLS(t *testing.T, ctx context.Context, docker dockerclient.Client, opts mongod.Options) {
	t.Helper()
	c := startAndTalk(t, ctx, opts.WithTLS())
	info, err := docker.ContainerInspect(ctx, c.ID())
	require.NoError(t, err, "the running container can be inspected")
	published := info.HostPort("27017/tcp")
	require.NotEmpty(t, published, "with TLS the port is published")
	assert.Equal(t, published, strconv.Itoa(c.Port()), "and mongod is reached on the published port")
	if gateway := os.Getenv(EnvReachGateway); gateway != "" {
		assert.Equal(t, gateway, c.Host(), "at the default gateway")
	} else {
		assert.False(t, net.ParseIP(c.Host()).IsLoopback(), "at the default gateway, which is not loopback")
	}
	stopAndCheckGone(t, ctx, docker, c)
}

// startAndTalk starts mongod and proves it answers where Container says.
func startAndTalk(t *testing.T, ctx context.Context, opts mongod.Options) *mongod.Container {
	t.Helper()
	c, err := mongod.StartWithOptions(ctx, opts)
	require.NoError(t, err, "the start works in this setup with no address given")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })
	address := net.JoinHostPort(c.Host(), strconv.Itoa(c.Port()))
	if c.TLSConfig() != nil {
		require.NoError(t, askHelloTLS(address, c.TLSConfig()), "mongod answers over TLS at Host and Port")
		return c
	}
	require.NoError(t, askHello(address), "mongod answers at Host and Port")
	return c
}

// stopAndCheckGone stops the container and checks the daemon no longer has
// it.
func stopAndCheckGone(t *testing.T, ctx context.Context, docker dockerclient.Client, c *mongod.Container) {
	t.Helper()
	require.NoError(t, c.Stop(ctx), "stopping removes the container")
	_, err := docker.ContainerInspect(ctx, c.ID())
	assert.ErrorIs(t, err, dockerclient.ErrNotFound, "nothing is left behind")
}

// daemonHostname returns the host of a tcp:// daemon address.
func daemonHostname(t *testing.T, docker dockerclient.Client) string {
	t.Helper()
	parsed, err := url.Parse(docker.Host())
	require.NoError(t, err, "the daemon address parses")
	require.Equal(t, "tcp", parsed.Scheme, "this setup reaches the daemon over tcp")
	return parsed.Hostname()
}
