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

// TestIntegrationReach starts mongod with no options wherever the tests
// run, talks to it, and checks that stopping it leaves nothing behind.
func TestIntegrationReach(t *testing.T) {
	// Where containers are reached is the point here, so the address is
	// left to the start, or to whatever the runner set.
	t.Setenv(mongod.EnvHostIP, callerHostIP)
	docker, ctx := liveDocker(t)
	setup := os.Getenv(EnvReachSetup)

	c := startAndTalk(t, ctx, mongod.DefaultOptions())
	published := publishedPort(t, ctx, docker, c)
	switch setup {
	case "":
	case "sibling":
		assert.False(t, published, "a test in a container on the same daemon reaches mongod on its network")
		assert.Equal(t, 27017, c.Port(), "at mongod's own port")
		assert.Empty(t, c.Username(), "a user-defined network needs no password")
	case "host-network":
		assert.Equal(t, "127.0.0.1", c.Host(), "a test on the host's network reaches a port published on loopback")
	case "dind", "tcp":
		assert.Equal(t, daemonHostname(t, docker), c.Host(), "a test reaches a port published on the daemon's own host")
	default:
		t.Fatalf("%s=%q is not a setup this test knows: use sibling, host-network, dind or tcp", EnvReachSetup, setup)
	}
	stopAndCheckGone(t, ctx, docker, c)

	if setup == "sibling" {
		// WithTLS keeps the published path, so a sibling reaches it at the
		// default gateway, the one rule no other setup uses.
		tlsContainer := startAndTalk(t, ctx, mongod.DefaultOptions().WithTLS())
		assert.True(t, publishedPort(t, ctx, docker, tlsContainer), "with TLS the port is published")
		assert.False(t, net.ParseIP(tlsContainer.Host()).IsLoopback(), "and reached through the gateway, not loopback")
		stopAndCheckGone(t, ctx, docker, tlsContainer)
	}
}

// startAndTalk starts mongod and proves it answers where Container says.
// With TLS the start itself has had mongod answer there, over TLS.
func startAndTalk(t *testing.T, ctx context.Context, opts mongod.Options) *mongod.Container {
	t.Helper()
	c, err := mongod.StartWithOptions(ctx, opts)
	require.NoError(t, err, "the start works in this setup with no address given")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })
	if c.TLSConfig() == nil {
		require.NoError(t, askHello(net.JoinHostPort(c.Host(), strconv.Itoa(c.Port()))), "mongod answers at Host and Port")
	}
	return c
}

// publishedPort reports whether mongod's port is published on the daemon's
// host.
func publishedPort(t *testing.T, ctx context.Context, docker dockerclient.Client, c *mongod.Container) bool {
	t.Helper()
	info, err := docker.ContainerInspect(ctx, c.ID())
	require.NoError(t, err, "the running container can be inspected")
	return info.HostPort("27017/tcp") != ""
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
