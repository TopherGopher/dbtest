package mongod_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/dockerapi"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/mongod"
)

// These tests start real containers on a real Docker daemon, located through
// the normal environment. They fail, rather than skip, when no daemon is
// reachable: mongotest exists to start MongoDB containers, and a suite that
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
	require.NoError(t, err, "mongod integration: cannot configure a Docker client; set DOCKER_HOST to point at a running Docker daemon")
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	t.Cleanup(cancel)
	require.NoError(t, client.Negotiate(ctx), "mongod integration: no Docker daemon reachable at %s; set DOCKER_HOST to point at a running Docker daemon", client.Host())
	return client, ctx
}

// requireImage makes sure an image is available locally, pulling it only if
// it is not.
func requireImage(t testing.TB, ctx context.Context, docker dockerclient.Client, ref string) {
	t.Helper()
	if _, err := docker.ImageInspect(ctx, ref); err == nil {
		return
	}
	require.NoError(t, docker.ImagePull(ctx, ref), "mongod integration: cannot pull %s", ref)
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
	c, err := mongod.Start(ctx, mongod.WithLabel(key, value))
	require.NoError(t, err, "mongod integration: cannot start a container; is a Docker daemon reachable and the mongo:8 image available?")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	assert.NotEmpty(t, c.ID(), "the daemon assigned an id, and everything afterwards is addressed to it")
	assert.Greater(t, c.Port(), 0, "with no WithPort the daemon assigns the host port, and Start reads it back from inspect")
	assert.Contains(t, c.URI(), "directConnection=true", "the driver must be stopped from discovering a topology a single container does not have")

	conn, err := net.DialTimeout("tcp", net.JoinHostPort(c.Host(), strconv.Itoa(c.Port())), 10*time.Second)
	require.NoError(t, err, "the address Start resolved is the one a caller dials; if this fails the resolution is wrong for this environment, which is exactly the failure WithHostIP exists to correct")
	require.NoError(t, conn.Close(), "closing the probe connection must succeed")

	require.NoError(t, c.Stop(ctx), "stopping removes the container")
	require.NoError(t, c.Stop(ctx), "Stop is called from defers as well as explicitly, so the second call must be harmless")

	if ids, checked := containersLabelled(t, key, value); checked {
		assert.Empty(t, ids, "a stopped container is removed and not merely killed, so the daemon is left as this test found it")
	}
}

func TestIntegrationMongodIsReallyServing(t *testing.T) {
	docker, ctx := liveDocker(t)
	key, value := runLabel(t)

	c, err := mongod.Start(ctx, mongod.WithDocker(docker), mongod.WithLabel(key, value))
	require.NoError(t, err, "mongod integration: cannot start a container; is a Docker daemon reachable and the mongo:8 image available?")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	assert.Equal(t, "1", evalIn(t, ctx, docker, c, "db.runCommand({ping:1}).ok"),
		"mongod answers the first command it is sent, with no retry: Start does not return a server that is still coming up")
}

func TestIntegrationWithPortPublishesExactlyThere(t *testing.T) {
	docker, ctx := liveDocker(t)
	key, value := runLabel(t)
	port := closedPort(t)

	c, err := mongod.Start(ctx, mongod.WithDocker(docker), mongod.WithPort(port), mongod.WithLabel(key, value))
	require.NoError(t, err, "mongod integration: cannot start a container on the pinned port %d; is something else bound to it?", port)
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	assert.Equal(t, port, c.Port(), "WithPort exists for callers who need a port known in advance, so the container has to be on that port and no other")
	assert.Contains(t, c.URI(), ":"+strconv.Itoa(port)+"/", "the URI names the pinned port")

	info, err := docker.ContainerInspect(ctx, c.ID())
	require.NoError(t, err, "inspecting the container the test just started must succeed")
	assert.Equal(t, strconv.Itoa(port), info.HostPort("27017/tcp"), "the daemon's own view has to agree, or the port came from somewhere other than the container")
}

func TestIntegrationAReplicaSetAcceptsWritesWhenStartReturns(t *testing.T) {
	docker, ctx := liveDocker(t)
	key, value := runLabel(t)

	c, err := mongod.Start(ctx, mongod.WithDocker(docker), mongod.WithReplicaSet("rs0"), mongod.WithLabel(key, value))
	require.NoError(t, err, "mongod integration: cannot start a replica set container")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	assert.Equal(t, "rs0 true", evalIn(t, ctx, docker, c, `const h = db.hello(); h.setName + " " + h.isWritablePrimary`),
		"the set is initiated and its one member is primary on the first ask, with no retry: that is what transactions and change streams need")
}

func TestIntegrationParallelContainersDoNotCollide(t *testing.T) {
	docker, ctx := liveDocker(t)
	key, value := runLabel(t)
	const containers = 6

	// Make sure the image is present up front, so six parallel starts do not
	// each discover it missing and pull it at the same time. Inspect first:
	// pulling an image that is already local is a registry round trip for
	// nothing, and a rate-limited registry would fail a run that needed
	// nothing from it.
	requireImage(t, ctx, docker, mongod.ImageDockerHub.Reference())

	var mu sync.Mutex
	ports := map[int]string{}
	record := func(t testing.TB, c *mongod.Container) {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		previous, taken := ports[c.Port()]
		require.False(t, taken, "container %s was published on port %d, which container %s already has: two containers on one port means one of them is unreachable", c.ID(), c.Port(), previous)
		ports[c.Port()] = c.ID()
	}

	t.Run("subtests", func(t *testing.T) {
		for i := range containers {
			t.Run(fmt.Sprintf("container-%d", i), func(t *testing.T) {
				t.Parallel()
				c, err := mongod.Start(ctx, mongod.WithDocker(docker), mongod.WithLabel(key, value))
				require.NoError(t, err, "six containers starting at once must all come up; a daemon-assigned port is what keeps them from racing for one")
				defer func() { _ = c.Stop(context.Background()) }()
				record(t, c)

				conn, err := net.DialTimeout("tcp", net.JoinHostPort(c.Host(), strconv.Itoa(c.Port())), 10*time.Second)
				require.NoError(t, err, "each container has to be reachable at its own address")
				require.NoError(t, conn.Close(), "closing the probe connection must succeed")

				require.NoError(t, c.Stop(ctx), "each subtest removes the container it started")
			})
		}
	})

	assert.Len(t, ports, containers, "every start must have produced a container on a port of its own")
	if ids, checked := containersLabelled(t, key, value); checked {
		assert.Empty(t, ids, "every container this test started was removed, so the daemon is left as it was found")
	}
}

func TestIntegrationStartFailsWhenTheImageDoesNotExist(t *testing.T) {
	docker, ctx := liveDocker(t)

	_, err := mongod.Start(ctx,
		mongod.WithDocker(docker),
		mongod.WithImage("mongo:this-tag-does-not-exist"),
		mongod.WithStartTimeout(30*time.Second),
	)

	var failed *mongod.StartError
	require.ErrorAs(t, err, &failed, "an image that cannot be pulled cannot be run, and saying so at once beats waiting out the start timeout")
	assert.Equal(t, mongod.OpPullImage, failed.Op, "the error says the pull is what failed")
	assert.Equal(t, "mongo:this-tag-does-not-exist", failed.Image, "and names the image at fault, which is what the reader has to correct")
	assert.False(t, errors.Is(err, context.DeadlineExceeded), "the failure is a missing image, not a slow one; reporting a timeout would send the reader looking in the wrong place")
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
			requireImage(t, ctx, docker, tc.image.Reference())

			opts := []mongod.Option{mongod.WithDocker(docker), mongod.WithMongoImage(tc.image), mongod.WithLabel(key, value)}
			if tc.replicaSet != "" {
				opts = append(opts, mongod.WithReplicaSet(tc.replicaSet))
			}
			c, err := mongod.Start(ctx, opts...)
			require.NoError(t, err, "mongod integration: %s must start; a well-known image constant that does not resolve to a runnable image is worse than none", tc.image)
			t.Cleanup(func() { _ = c.Stop(context.Background()) })

			assert.Equal(t, tc.image.Reference(), c.Image(), "the container runs the image the constant names")

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
	requireImage(t, ctx, docker, mongod.ImageAtlasLocal.Reference())

	c, err := mongod.Start(ctx, mongod.WithDocker(docker), mongod.WithMongoImage(mongod.ImageAtlasLocal), mongod.WithLabel(key, value))
	require.NoError(t, err, "mongod integration: Atlas Local must start with no flags at all")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	setName := evalIn(t, ctx, docker, c, "rs.status().set")

	assert.NotEmpty(t, setName,
		"Atlas Local initiates a single-node set while starting, so rs.status() answers with a name instead of throwing NotYetInitialized")
	assert.NotEqual(t, "rs0", setName,
		"the name is generated from the container's hostname rather than being a fixed one, which is why WithReplicaSet cannot be honoured for this image and a driver layer has to read the name")
	assert.True(t, c.MongoImage().ReplicaSetPreconfigured(),
		"and the image says so in advance, which is what lets Start refuse WithReplicaSet with an explanation instead of producing a container that cannot exec")
}

// Atlas Local answers commands from a mongod it then replaces, so a Start
// that returned on the first answer would hand back a server about to go
// away. The first command through the published port has to work, and so
// does every one after it.
func TestIntegrationAtlasLocalKeepsAnsweringOnceStartReturns(t *testing.T) {
	docker, ctx := liveDocker(t)
	key, value := runLabel(t)
	requireImage(t, ctx, docker, mongod.ImageAtlasLocal.Reference())

	c, err := mongod.Start(ctx, mongod.WithDocker(docker), mongod.WithMongoImage(mongod.ImageAtlasLocal), mongod.WithLabel(key, value))
	require.NoError(t, err, "mongod integration: Atlas Local must start")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })
	address := net.JoinHostPort(c.Host(), strconv.Itoa(c.Port()))

	require.NoError(t, askHello(address), "the first command through the published port is answered, with no retry")
	for range 5 {
		time.Sleep(2 * time.Second)
		assert.NoError(t, askHello(address), "and mongod is still answering there: the one Start waited for is the one that stays")
	}
}

// Atlas Local's command is its entrypoint, so mongod flags replace the program
// rather than configuring it. Validation refuses that combination; this proves
// the refusal is warranted by showing what the container would otherwise do.
func TestIntegrationAtlasLocalWouldDieIfGivenMongodFlags(t *testing.T) {
	docker, ctx := liveDocker(t)
	key, value := runLabel(t)
	requireImage(t, ctx, docker, mongod.ImageAtlasLocal.Reference())

	// Start refuses this before the daemon is asked for anything.
	_, err := mongod.Start(ctx, mongod.WithDocker(docker), mongod.WithMongoImage(mongod.ImageAtlasLocal), mongod.WithMongodArgs("--quiet"))
	require.ErrorIs(t, err, mongod.ErrUnsupportedForImage, "the combination is refused rather than attempted")

	// What it is protecting against: the same request made directly.
	id, _, err := docker.ContainerCreate(ctx, "", dockerclient.ContainerConfig{
		Image:  mongod.ImageAtlasLocal.Reference(),
		Cmd:    []string{"--quiet"},
		Labels: map[string]string{key: value},
	})
	require.NoError(t, err, "the daemon accepts the create; nothing is wrong until the container tries to run")
	t.Cleanup(func() {
		_ = docker.ContainerRemove(context.Background(), id, dockerclient.RemoveOptions{Force: true, RemoveVolumes: true})
	})

	startErr := docker.ContainerStart(ctx, id)
	require.Error(t, startErr, "the flag has replaced the program, so there is nothing to execute")
	assert.Contains(t, startErr.Error(), "--quiet",
		"the daemon's complaint is that the flag is not an executable, which says nothing about the option that caused it; that is why validation catches it first")
}

// evalIn runs one mongosh expression inside the container, as its root user
// when it has one, and returns what it printed.
//
// It asks once. Start returns a mongod that already answers commands, so a
// retry here would pass a Start that returned too early.
func evalIn(t testing.TB, ctx context.Context, docker dockerclient.Client, c *mongod.Container, script string) string {
	t.Helper()
	cmd := []string{"mongosh", "--quiet", "--norc"}
	if c.Username() != "" {
		cmd = append(cmd, "--username", c.Username(), "--password", c.Password(), "--authenticationDatabase", "admin")
	}
	return runIn(t, ctx, docker, c, append(cmd, "--eval", script)...)
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
	require.Zero(t, result.ExitCode, "mongosh must run the expression: %s", strings.TrimSpace(result.Stdout+" "+result.Stderr))
	return strings.TrimSpace(result.Stdout)
}

// anonymousListDatabases asks mongod, without credentials, for something only
// an authenticated user may have, and returns the name of the error or "open".
const anonymousListDatabases = `try { db.getSiblingDB("admin").runCommand({listDatabases: 1}); "open" } catch (e) { e.codeName }`

// servingCommand returns the command line of the mongod that the published
// port leads to, or "" when the container is not running one.
func servingCommand(t testing.TB, ctx context.Context, docker dockerclient.Client, id string) string {
	t.Helper()
	top, err := docker.ContainerTop(ctx, id)
	require.NoError(t, err, "listing the container's processes must succeed")
	for _, process := range top.Processes {
		for _, field := range process {
			if strings.Contains(field, "mongod") && strings.Contains(field, "--bind_ip_all") {
				return field
			}
		}
	}
	return ""
}

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
			requireImage(t, ctx, docker, tc.image.Reference())

			c, err := mongod.Start(ctx, mongod.WithDocker(docker), mongod.WithMongoImage(tc.image), mongod.WithAuth("admin", "p@ss/word"), mongod.WithLabel(key, value))
			require.NoError(t, err, "mongod integration: %s must start with credentials", tc.image)
			t.Cleanup(func() { _ = c.Stop(context.Background()) })

			assert.NotEmpty(t, servingCommand(t, ctx, docker, c.ID()),
				"Start returned, so the mongod listening on every interface is the one running; the one that creates the user listens on the container's loopback only and is gone seconds later")
			assert.Equal(t, "1", evalIn(t, ctx, docker, c, "db.runCommand({ping:1}).ok"), "the root user is let in on the first ask")
			assert.Equal(t, "Unauthorized", evalAnonymously(t, ctx, docker, c, anonymousListDatabases),
				"a connection without the credentials is refused what the root user is allowed")
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
			requireImage(t, ctx, docker, tc.image.Reference())

			c, err := mongod.Start(ctx, mongod.WithDocker(docker), mongod.WithMongoImage(tc.image), mongod.WithReplicaSet("rs0"), mongod.WithAuth("admin", "s3cret"), mongod.WithLabel(key, value))
			require.NoError(t, err, "mongod integration: %s must start as an authenticated replica set member; if it exited, the key file's owner is the first thing to check", tc.image)
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

	c, err := mongod.Start(ctx, mongod.WithDocker(docker), mongod.WithHostIP(machineAddress(t)), mongod.WithLabel(key, value))
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
