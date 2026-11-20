package mongod_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/dbtest/dockerapi"
	"github.com/tophergopher/dbtest/dockerclient"
	"github.com/tophergopher/dbtest/mongod"
)

// These tests start real containers on a real Docker daemon, located through
// the normal environment. They fail, rather than skip, when no daemon is
// reachable: dbtest exists to start MongoDB containers, and a suite that
// quietly skipped would hide the package being completely broken.
//
// They need the images they name, such as mongod.ImageDockerHub. Start pulls
// one the daemon does not have.

// integrationTimeout is generous on purpose. On a slow storage driver a
// container can take ten to fifteen seconds to start when several start at
// once, and the first run may also be pulling the image.
const integrationTimeout = 5 * time.Minute

// liveDocker returns a client for the real daemon, or fails with guidance.
func liveDocker(t testing.TB) (dockerclient.Client, context.Context) {
	t.Helper()
	client, err := dockerapi.FromEnv()
	require.NoError(t, err, "no Docker client; set DOCKER_HOST to a running daemon")
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	t.Cleanup(cancel)
	require.NoError(t, client.Negotiate(ctx), "no Docker daemon at %s; set DOCKER_HOST", client.Host())
	return client, ctx
}

// requireImage has the daemon fetch an image before a test needs it, the way
// a caller would in TestMain.
func requireImage(t testing.TB, ctx context.Context, docker dockerclient.Client, image mongod.MongoImage) {
	t.Helper()
	err := mongod.PullImage(ctx, mongod.DefaultOptions().WithDocker(docker).WithMongoImage(image))
	require.NoError(t, err, "the image is on the daemon")
}

// runLabel is a label unique to one test, so the check for containers left
// behind is not confused by another suite running at the same time.
func runLabel(t testing.TB) (key, value string) {
	t.Helper()
	return "mongotest.run", fmt.Sprintf("%s-%d", strings.ReplaceAll(t.Name(), "/", "_"), time.Now().UnixNano())
}

// containersLabelled asks the docker CLI what is left carrying a label. It
// returns false when the CLI is not on PATH, in which case the caller has to
// make do with what the client itself can see.
func containersLabelled(t testing.TB, key, value string) (ids []string, checked bool) {
	t.Helper()
	out, err := exec.Command("docker", "ps", "-aq", "--filter", "label="+key+"="+value).Output()
	if err != nil {
		return nil, false
	}
	return strings.Fields(string(out)), true
}

func TestIntegrationStartConnectAndStop(t *testing.T) {
	key, value := runLabel(t)
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	t.Cleanup(cancel)

	// No WithDocker: this is the default path, all the way through
	// dockerapi.FromEnv finding the daemon on its own.
	c, err := mongod.StartWithOptions(ctx, mongod.DefaultOptions().WithLabel(key, value))
	require.NoError(t, err, "the container starts")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	assert.NotEmpty(t, c.ID(), "the daemon assigned an id")
	assert.Greater(t, c.Port(), 0, "the daemon assigns the host port")
	assert.Contains(t, c.URI(), "directConnection=true", "the driver connects directly")

	// If this fails, Start resolved the wrong host here; WithHostIP corrects it.
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(c.Host(), strconv.Itoa(c.Port())), 10*time.Second)
	require.NoError(t, err, "the resolved address accepts a connection")
	require.NoError(t, conn.Close(), "closing the probe connection must succeed")

	require.NoError(t, c.Stop(ctx), "stopping removes the container")
	require.NoError(t, c.Stop(ctx), "a second Stop is harmless")

	if ids, checked := containersLabelled(t, key, value); checked {
		assert.Empty(t, ids, "the container is removed")
	}
}

// The call the package is judged by: no options, no client, nothing to read
// first.
func TestIntegrationStartNeedsNoOptions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	t.Cleanup(cancel)

	c, err := mongod.Start(ctx)
	require.NoError(t, err, "the default call works")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	assert.Equal(t, mongod.ImageDockerHub, c.MongoImage().WithVersion(""), "it runs the default image")
	assert.NotEmpty(t, c.MongoImage().Version, "at a version the start filled in")
	assert.NoError(t, askHello(net.JoinHostPort(c.Host(), strconv.Itoa(c.Port()))), "and mongod answers at the address it reports")
}

func TestIntegrationMongodIsReallyServing(t *testing.T) {
	docker, ctx := liveDocker(t)
	key, value := runLabel(t)

	c, err := mongod.StartWithOptions(ctx, mongod.DefaultOptions().WithDocker(docker).WithLabel(key, value))
	require.NoError(t, err, "the container starts")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	assert.Equal(t, "1", evalIn(t, ctx, docker, c, "db.runCommand({ping:1}).ok"),
		"mongod answers the first command")
}

func TestIntegrationWithPortPublishesExactlyThere(t *testing.T) {
	docker, ctx := liveDocker(t)
	key, value := runLabel(t)
	port := closedPort(t)

	c, err := mongod.StartWithOptions(ctx, mongod.DefaultOptions().WithDocker(docker).WithPort(port).WithLabel(key, value))
	require.NoError(t, err, "starts on the pinned port %d", port)
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	assert.Equal(t, port, c.Port(), "on the port asked for")
	assert.Contains(t, c.URI(), ":"+strconv.Itoa(port)+"/", "the URI names the pinned port")

	info, err := docker.ContainerInspect(ctx, c.ID())
	require.NoError(t, err, "inspecting the container the test just started must succeed")
	assert.Equal(t, strconv.Itoa(port), info.HostPort("27017/tcp"), "the daemon agrees")
}

func TestIntegrationAReplicaSetAcceptsWritesWhenStartReturns(t *testing.T) {
	docker, ctx := liveDocker(t)
	key, value := runLabel(t)

	c, err := mongod.StartWithOptions(ctx, mongod.DefaultOptions().WithDocker(docker).WithReplicaSet("rs0").WithLabel(key, value))
	require.NoError(t, err, "mongod integration: cannot start a replica set container")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	assert.Equal(t, "rs0 true", evalIn(t, ctx, docker, c, `const h = db.hello(); h.setName + " " + h.isWritablePrimary`),
		"the one member is primary at once")
}

// Images before MongoDB 5.0 have only the legacy mongo shell, so this is the
// one start that initiates a set with it. The image comes from public ECR
// because Docker Hub limits pulls, and requireImage fetches it on a first run.
func TestIntegrationAReplicaSetIsInitiatedWithTheLegacyShell(t *testing.T) {
	docker, ctx := liveDocker(t)
	key, value := runLabel(t)
	legacy := mongod.ImagePublicECR.WithVersion("4.4")
	requireImage(t, ctx, docker, legacy)

	c, err := mongod.StartWithOptions(ctx, mongod.DefaultOptions().WithDocker(docker).WithMongoImage(legacy).WithReplicaSet("rs0").WithLabel(key, value))
	require.NoError(t, err, "a legacy-shell image gets a replica set")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	inserted := runIn(t, ctx, docker, c, "mongo", "--quiet", "--norc", "--eval", `db.getSiblingDB("mongotest").writes.insert({ok: 1}).nInserted`)

	assert.Equal(t, "1", inserted, "the set takes a write on the first ask, with no retry")
}

// A missing shell is how Start knows to try the other one. It learns that
// from an exit code, so the code a real daemon gives a missing program has
// to be one of those Start takes to mean it.
func TestIntegrationAMissingProgramIsAnExitCodeAndNotAnError(t *testing.T) {
	docker, ctx := liveDocker(t)
	key, value := runLabel(t)

	c, err := mongod.StartWithOptions(ctx, mongod.DefaultOptions().WithDocker(docker).WithLabel(key, value))
	require.NoError(t, err, "mongod integration: cannot start a container to run a command in")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	result, err := docker.Exec(ctx, c.ID(), "mongotest-no-such-program")

	require.NoError(t, err, "a missing program is a result, not an error")
	assert.Contains(t, []int{126, 127}, result.ExitCode, "and its exit code is one Start reads as a missing program")
}

func TestIntegrationStartFailsWhenTheImageDoesNotExist(t *testing.T) {
	docker, ctx := liveDocker(t)

	_, err := mongod.StartWithOptions(ctx, mongod.DefaultOptions().
		WithDocker(docker).
		WithImage("mongo:this-tag-does-not-exist").
		WithStartTimeout(30*time.Second))

	var failed *mongod.StartError
	require.ErrorAs(t, err, &failed, "a missing image fails at once")
	assert.Equal(t, mongod.OpPullImage, failed.Op, "the error says the pull is what failed")
	assert.Equal(t, "docker.io/library/mongo:this-tag-does-not-exist", failed.Image, "and names the image at fault, which is what the reader has to correct")
	assert.False(t, errors.Is(err, context.DeadlineExceeded), "as a missing image, not a timeout")
}

// A flag mongod rejects is the commonest first failure. The error must carry
// mongod's own reason.
func TestIntegrationAFlagMongodRejectsIsReportedWithItsReason(t *testing.T) {
	docker, ctx := liveDocker(t)
	key, value := runLabel(t)

	_, err := mongod.StartWithOptions(ctx, mongod.DefaultOptions().
		WithDocker(docker).
		WithLabel(key, value).
		WithMongodArgs("--no-such-mongod-flag"))

	var exited *mongod.ContainerExitedError
	require.ErrorAs(t, err, &exited, "reported as an exit")
	assert.Contains(t, exited.Output, "no-such-mongod-flag", "the output names the flag mongod refused")
	if ids, checked := containersLabelled(t, key, value); checked {
		assert.Empty(t, ids, "and the container is removed once its output is read")
	}
}

// The well-known images are only worth having if they resolve to something
// that really exists and really runs. These start each of them for real.
func TestIntegrationWellKnownImagesStart(t *testing.T) {
	cases := []struct {
		name  string
		image mongod.MongoImage
		// replicaSet is left empty for an image that will not accept the flag.
		replicaSet string
	}{
		{name: "docker hub official", image: mongod.ImageDockerHub, replicaSet: "rs0"},
		{name: "mongodb community server", image: mongod.ImageCommunity, replicaSet: "rs0"},
		{name: "atlas local", image: mongod.ImageAtlasLocal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			docker, ctx := liveDocker(t)
			key, value := runLabel(t)
			requireImage(t, ctx, docker, tc.image)

			opts := mongod.DefaultOptions().WithDocker(docker).WithMongoImage(tc.image).WithLabel(key, value)
			if tc.replicaSet != "" {
				opts = opts.WithReplicaSet(tc.replicaSet)
			}
			c, err := mongod.StartWithOptions(ctx, opts)
			require.NoError(t, err, "%s starts", tc.image)
			t.Cleanup(func() { _ = c.Stop(context.Background()) })

			assert.Equal(t, tc.image, c.MongoImage().WithVersion(""), "the container runs the image the constant names")
			assert.NotEmpty(t, c.MongoImage().Version, "at the default version")

			assert.Equal(t, "1", evalIn(t, ctx, docker, c, "db.runCommand({ping:1}).ok"),
				"the server answers the first command it is sent, with no retry")

			require.NoError(t, c.Stop(ctx), "each case removes the container it started")
			if ids, checked := containersLabelled(t, key, value); checked {
				assert.Empty(t, ids, "nothing is left behind")
			}
		})
	}
}

// Atlas Local running a replica set of its own is why ReplicaSetPreconfigured
// exists and why WithReplicaSet is refused for it. This is where that claim is
// checked against the real image rather than taken on trust.
func TestIntegrationAtlasLocalAlreadyRunsAReplicaSet(t *testing.T) {
	docker, ctx := liveDocker(t)
	key, value := runLabel(t)
	requireImage(t, ctx, docker, mongod.ImageAtlasLocal)

	c, err := mongod.StartWithOptions(ctx, mongod.DefaultOptions().WithDocker(docker).WithMongoImage(mongod.ImageAtlasLocal).WithLabel(key, value))
	require.NoError(t, err, "mongod integration: Atlas Local must start with no flags at all")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	setName := evalIn(t, ctx, docker, c, "rs.status().set")

	assert.NotEmpty(t, setName,
		"Atlas Local initiates its own set")
	assert.NotEqual(t, "rs0", setName,
		"under a generated name")
	assert.True(t, c.MongoImage().ReplicaSetPreconfigured(),
		"which the image declares")
}

// Atlas Local answers commands from a mongod it then replaces, so a Start
// that returned on the first answer would hand back a server about to go
// away. The first command through the published port has to work, and so
// does every one after it.
func TestIntegrationAtlasLocalKeepsAnsweringOnceStartReturns(t *testing.T) {
	docker, ctx := liveDocker(t)
	key, value := runLabel(t)
	requireImage(t, ctx, docker, mongod.ImageAtlasLocal)

	c, err := mongod.StartWithOptions(ctx, mongod.DefaultOptions().WithDocker(docker).WithMongoImage(mongod.ImageAtlasLocal).WithLabel(key, value))
	require.NoError(t, err, "mongod integration: Atlas Local must start")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })
	address := net.JoinHostPort(c.Host(), strconv.Itoa(c.Port()))

	require.NoError(t, askHello(address), "the first command is answered")
	for range 5 {
		time.Sleep(2 * time.Second)
		assert.NoError(t, askHello(address), "and mongod is still answering")
	}
}

// Atlas Local's command is its entrypoint, so mongod flags replace the program
// rather than configuring it. Validation refuses that combination; this proves
// the refusal is warranted by showing what the container would otherwise do.
func TestIntegrationAtlasLocalWouldDieIfGivenMongodFlags(t *testing.T) {
	docker, ctx := liveDocker(t)
	key, value := runLabel(t)
	requireImage(t, ctx, docker, mongod.ImageAtlasLocal)

	// Start refuses this before the daemon is asked for anything.
	_, err := mongod.StartWithOptions(ctx, mongod.DefaultOptions().WithDocker(docker).WithMongoImage(mongod.ImageAtlasLocal).WithMongodArgs("--quiet"))
	require.ErrorIs(t, err, mongod.ErrUnsupportedForImage, "the combination is refused rather than attempted")

	// What it is protecting against: the same request made directly. The
	// version is the one requireImage fetched; with none, the daemon would
	// go looking for "latest".
	id, _, err := docker.ContainerCreate(ctx, "", dockerclient.ContainerConfig{
		Image:  mongod.ImageAtlasLocal.WithVersion("8.0").Reference(),
		Cmd:    []string{"--quiet"},
		Labels: map[string]string{key: value},
	})
	require.NoError(t, err, "the daemon accepts the create")
	t.Cleanup(func() {
		_ = docker.ContainerRemove(context.Background(), id, dockerclient.RemoveOptions{Force: true, RemoveVolumes: true})
	})

	startErr := docker.ContainerStart(ctx, id)
	require.Error(t, startErr, "the flag has replaced the program, so there is nothing to execute")
	info, err := docker.ContainerInspect(ctx, id)
	require.NoError(t, err, "the container is still there to inspect")
	// The daemon says only that "--quiet" is not a program. It never says
	// the flag was meant for mongod, so Start refuses it first.
	assert.Contains(t, []int{126, 127}, info.State.ExitCode, "the exit code of a program that could not be run")
}

// evalIn runs one mongosh expression inside the container, as its root user
// when it has one, and returns what it printed.
//
// It asks once. Start returns a mongod that already answers commands, so a
// retry here would pass a Start that returned too early.
func evalIn(t testing.TB, ctx context.Context, docker dockerclient.Client, c *mongod.Container, script string) string {
	t.Helper()
	return runIn(t, ctx, docker, c, mongoshCommand(c, script)...)
}

// mongoshCommand is the command that runs one expression in a container's
// shell, as its root user when it has one.
func mongoshCommand(c *mongod.Container, script string) []string {
	cmd := []string{"mongosh", "--quiet", "--norc"}
	if c.Username() != "" {
		cmd = append(cmd, "--username", c.Username(), "--password", c.Password(), "--authenticationDatabase", "admin")
	}
	return append(cmd, "--eval", script)
}

// evalAnonymously is evalIn without the container's credentials.
func evalAnonymously(t testing.TB, ctx context.Context, docker dockerclient.Client, c *mongod.Container, script string) string {
	t.Helper()
	return runIn(t, ctx, docker, c, "mongosh", "--quiet", "--norc", "--eval", script)
}

// runIn runs one command inside the container and returns its output.
func runIn(t testing.TB, ctx context.Context, docker dockerclient.Client, c *mongod.Container, cmd ...string) string {
	t.Helper()
	result, err := docker.Exec(ctx, c.ID(), cmd...)
	require.NoError(t, err, "running a command inside the container must succeed")
	require.Zero(t, result.ExitCode, "the shell must run the expression: %s", strings.TrimSpace(result.Stdout+" "+result.Stderr))
	return strings.TrimSpace(result.Stdout)
}

// anonymousListDatabases asks mongod, without credentials, for something only
// an authenticated user may have, and returns the name of the error or "open".
const anonymousListDatabases = `try { db.getSiblingDB("admin").runCommand({listDatabases: 1}); "open" } catch (e) { e.codeName }`

// The credentials have to do two things for real: let their holder in, and
// keep everyone else out. And Start has to return only once the mongod that
// enforces them is the one running, because every image creates its user with
// a first mongod that nothing outside the container can reach.
func TestIntegrationWithAuthIsEnforced(t *testing.T) {
	cases := []struct {
		name  string
		image mongod.MongoImage
	}{
		{name: "docker official", image: mongod.ImageDockerHub},
		{name: "mongodb community server", image: mongod.ImageCommunity},
		{name: "atlas local", image: mongod.ImageAtlasLocal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			docker, ctx := liveDocker(t)
			key, value := runLabel(t)
			requireImage(t, ctx, docker, tc.image)

			c, err := mongod.StartWithOptions(ctx, mongod.DefaultOptions().WithDocker(docker).WithMongoImage(tc.image).WithAuth("admin", "p@ss/word").WithLabel(key, value))
			require.NoError(t, err, "mongod integration: %s must start with credentials", tc.image)
			t.Cleanup(func() { _ = c.Stop(context.Background()) })

			assert.NoError(t, askHello(net.JoinHostPort(c.Host(), strconv.Itoa(c.Port()))),
				"Start returned, so the mongod the published port leads to is serving")
			assert.Equal(t, "1", evalIn(t, ctx, docker, c, "db.runCommand({ping:1}).ok"), "the root user is let in on the first ask")
			assert.Equal(t, "Unauthorized", evalAnonymously(t, ctx, docker, c, anonymousListDatabases),
				"a connection without credentials is refused")
			assert.Contains(t, c.URI(), "admin:p%40ss%2Fword@", "the URI carries the credentials, encoded")
		})
	}
}

// With credentials and --replSet alone the official images exit, asking for a
// key file; this is the combination Start supplies one for. Initiating the set
// as the root user proves the member came up, read its key, and enforces the
// credentials.
func TestIntegrationAReplicaSetWithAuthAcceptsWritesWhenStartReturns(t *testing.T) {
	cases := []struct {
		name  string
		image mongod.MongoImage
	}{
		{name: "docker official", image: mongod.ImageDockerHub},
		{name: "mongodb community server", image: mongod.ImageCommunity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			docker, ctx := liveDocker(t)
			key, value := runLabel(t)
			requireImage(t, ctx, docker, tc.image)

			c, err := mongod.StartWithOptions(ctx, mongod.DefaultOptions().WithDocker(docker).WithMongoImage(tc.image).WithReplicaSet("rs0").WithAuth("admin", "s3cret").WithLabel(key, value))
			require.NoError(t, err, "%s starts as an authenticated member; check the key file's owner", tc.image)
			t.Cleanup(func() { _ = c.Stop(context.Background()) })

			assert.Equal(t, "true", evalIn(t, ctx, docker, c, "db.hello().isWritablePrimary"),
				"the member accepts writes on the first ask, with no retry")
		})
	}
}

// machineAddress returns an address of this machine that is not loopback,
// which is what a caller in a sibling container or on another host dials.
func machineAddress(t testing.TB) string {
	t.Helper()
	addresses, err := net.InterfaceAddrs()
	require.NoError(t, err, "listing this machine's addresses must succeed")
	for _, address := range addresses {
		if ipNet, ok := address.(*net.IPNet); ok && ipNet.IP.To4() != nil && !ipNet.IP.IsLoopback() {
			return ipNet.IP.String()
		}
	}
	require.Fail(t, "this machine has no ipv4 address other than loopback", "a machine running a Docker daemon has at least its bridge's")
	return ""
}

// The reason credentials are generated at all: an address other than loopback
// publishes mongod on every interface, where the daemon's host is all that
// stands between it and the network.
func TestIntegrationAWidePublishIsNeverLeftOpen(t *testing.T) {
	docker, ctx := liveDocker(t)
	key, value := runLabel(t)

	c, err := mongod.StartWithOptions(ctx, mongod.DefaultOptions().WithDocker(docker).WithHostIP(machineAddress(t)).WithLabel(key, value))
	require.NoError(t, err, "mongod integration: cannot start a container published beyond loopback")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	info, err := docker.ContainerInspect(ctx, c.ID())
	require.NoError(t, err, "inspecting the container the test just started must succeed")
	require.Equal(t, "0.0.0.0", info.NetworkSettings.Ports["27017/tcp"][0].HostIP, "this start is the wide publish the test is about")

	assert.Equal(t, mongod.DefaultUsername, c.Username(), "nobody chose credentials, so they were generated")
	assert.NotEmpty(t, c.Password(), "with a password")
	assert.Equal(t, "1", evalIn(t, ctx, docker, c, "db.runCommand({ping:1}).ok"), "which lets its holder in")
	assert.Equal(t, "Unauthorized", evalAnonymously(t, ctx, docker, c, anonymousListDatabases),
		"whatever can reach the port without the password gets nothing from it")
}
