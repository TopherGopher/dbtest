package postgres_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/dbtest/dockerapi"
	"github.com/tophergopher/dbtest/dockerclient"
	"github.com/tophergopher/dbtest/postgres"
)

// These tests start real containers on a real Docker daemon, located through
// the normal environment. They fail, rather than skip, when no daemon is
// reachable: a suite that quietly skipped would hide the package being
// completely broken. Start pulls the default image if the daemon lacks it.

// integrationTimeout is generous on purpose: the first run may be pulling
// the image, and a slow storage driver slows every start.
const integrationTimeout = 5 * time.Minute

// liveDocker returns a client for the real daemon, or fails with guidance.
// It puts back the DBTEST_HOST_IP the runner set, which TestMain pinned to
// loopback for the mocked tests.
func liveDocker(t *testing.T) (dockerclient.Client, context.Context) {
	t.Helper()
	t.Setenv(postgres.EnvHostIP, callerHostIP)
	if callerHostIP == "" {
		require.NoError(t, os.Unsetenv(postgres.EnvHostIP), "the address is worked out as a caller's would be")
	}
	client, err := dockerapi.FromEnv()
	require.NoError(t, err, "no Docker client; set DOCKER_HOST to a running daemon")
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	t.Cleanup(cancel)
	require.NoError(t, client.Negotiate(ctx), "no Docker daemon at %s; set DOCKER_HOST", client.Host())
	return client, ctx
}

// runLabel is a label unique to one test, so the check for containers left
// behind is not confused by another suite running at the same time.
func runLabel(t *testing.T) (key, value string) {
	t.Helper()
	return "dbtest.run", fmt.Sprintf("%s-%d", strings.ReplaceAll(t.Name(), "/", "_"), time.Now().UnixNano())
}

// containersLabelled asks the daemon which containers carry a label.
func containersLabelled(t *testing.T, key, value string) []string {
	t.Helper()
	out, err := exec.Command("docker", "ps", "-aq", "--filter", "label="+key+"="+value).Output()
	require.NoError(t, err, "the docker CLI lists containers")
	return strings.Fields(string(out))
}

// psql runs a query in the container through psql, connecting over TCP
// with a connection string, so that the password is checked as a driver's
// would be.
func psql(ctx context.Context, docker dockerclient.Client, c *postgres.Container, uri, query string) (dockerclient.ExecResult, error) {
	return docker.Exec(ctx, c.ID(), "psql", uri, "--no-psqlrc", "--tuples-only", "--no-align", "--command", query)
}

// insideURI is the container's URI as seen from inside it: the same user,
// password and database, at its address on its network. Not loopback: the
// image trusts every login from there, so a password would prove nothing.
func insideURI(t *testing.T, ctx context.Context, docker dockerclient.Client, c *postgres.Container, password string) string {
	t.Helper()
	info, err := docker.ContainerInspect(ctx, c.ID())
	require.NoError(t, err, "the container can be inspected")
	address := ""
	for _, network := range info.NetworkSettings.Networks {
		address = network.IPAddress
	}
	require.NotEmpty(t, address, "the container has an address on its network")
	return "postgres://" + c.Username() + ":" + password + "@" + address + ":5432/" + c.Database() + "?sslmode=disable"
}

// The call the package is judged by: no options, no client, nothing to read
// first. The server must take a login with the credentials the URI holds,
// and refuse others.
func TestIntegrationStartNeedsNoOptions(t *testing.T) {
	docker, ctx := liveDocker(t)

	c, err := postgres.Start(ctx)
	require.NoError(t, err, "the default call works")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	assert.Equal(t, postgres.DefaultImage, c.Image(), "it runs the default image")
	result, err := psql(ctx, docker, c, insideURI(t, ctx, docker, c, c.Password()), "SELECT current_user || ' ' || current_database()")
	require.NoError(t, err, "psql runs in the container")
	assert.Equal(t, 0, result.ExitCode, "the URI's credentials log in: %s", result.Stderr)
	assert.Equal(t, c.Username()+" "+c.Database(), strings.TrimSpace(result.Stdout), "as the superuser, to the database the URI names")

	result, err = psql(ctx, docker, c, insideURI(t, ctx, docker, c, "wrong"), "SELECT 1")
	require.NoError(t, err, "psql runs in the container")
	assert.NotEqual(t, 0, result.ExitCode, "a wrong password is refused, so the password is really in force")
}

func TestIntegrationEachStartGetsAnEmptyDatabase(t *testing.T) {
	docker, ctx := liveDocker(t)
	opts := postgres.DefaultOptions().WithDocker(docker)

	first, err := postgres.StartWithOptions(ctx, opts)
	require.NoError(t, err, "the first container starts")
	t.Cleanup(func() { _ = first.Stop(context.Background()) })
	result, err := psql(ctx, docker, first, insideURI(t, ctx, docker, first, first.Password()), "CREATE TABLE orders (id int)")
	require.NoError(t, err, "psql runs in the first container")
	require.Equal(t, 0, result.ExitCode, "a table is created: %s", result.Stderr)

	second, err := postgres.StartWithOptions(ctx, opts)
	require.NoError(t, err, "the second container starts")
	t.Cleanup(func() { _ = second.Stop(context.Background()) })
	result, err = psql(ctx, docker, second, insideURI(t, ctx, docker, second, second.Password()), "SELECT count(*) FROM pg_tables WHERE tablename = 'orders'")
	require.NoError(t, err, "psql runs in the second container")
	assert.Equal(t, "0", strings.TrimSpace(result.Stdout), "nothing one test creates is seen by the next")
}

func TestIntegrationStopAndReapLeaveNothingBehind(t *testing.T) {
	docker, ctx := liveDocker(t)
	key, value := runLabel(t)
	opts := postgres.DefaultOptions().WithDocker(docker).WithLabel(key, value)

	stopped, err := postgres.StartWithOptions(ctx, opts)
	require.NoError(t, err, "a container starts")
	require.NoError(t, stopped.Stop(ctx), "and is stopped")
	_, err = postgres.StartWithOptions(ctx, opts)
	require.NoError(t, err, "another starts and is left running")

	require.NoError(t, postgres.ReapRunningContainers(ctx), "reaping removes what is left")
	assert.Empty(t, containersLabelled(t, key, value), "no container is left behind")
}
