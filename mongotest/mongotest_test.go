package mongotest_test

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/dbtest/dockerapi"
	"github.com/tophergopher/dbtest/mongod"
	"github.com/tophergopher/dbtest/mongotest"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// The integration tests start real containers. They need a Docker daemon,
// and fail rather than skip without one.

// insertAndFind writes a document through a client and reads it back.
func insertAndFind(t *testing.T, client *mongo.Client) {
	t.Helper()
	ctx := context.Background()
	coll := client.Database("mongotest").Collection("people")
	_, err := coll.InsertOne(ctx, bson.M{"name": "Diana"})
	require.NoError(t, err, "the client can write")
	var found bson.M
	require.NoError(t, coll.FindOne(ctx, bson.M{"name": "Diana"}).Decode(&found), "and read back what it wrote")
}

// containerExists asks the docker CLI whether a container is still there.
func containerExists(t *testing.T, id string) bool {
	t.Helper()
	out, err := exec.Command("docker", "ps", "-aq", "--no-trunc", "--filter", "id="+id).Output()
	require.NoError(t, err, "the docker CLI lists containers")
	return strings.TrimSpace(string(out)) != ""
}

func TestIntegrationRunGivesAConnectedClient(t *testing.T) {
	inst := mongotest.Run(t)

	insertAndFind(t, inst.Client)
	assert.NotNil(t, inst.Container, "the instance has the container it started")
}

func TestIntegrationRunRemovesTheContainerWhenTheTestEnds(t *testing.T) {
	var inst *mongotest.Instance
	t.Run("a test that uses a server", func(t *testing.T) {
		inst = mongotest.Run(t)
		require.True(t, containerExists(t, inst.Container.ID()), "the container runs during the test")
	})

	assert.False(t, containerExists(t, inst.Container.ID()), "and is removed once the test has ended")
	assert.ErrorIs(t, inst.Ping(context.Background(), nil), mongo.ErrClientDisconnected, "and the client is disconnected")
}

func TestIntegrationAReplicaSetCommitsATransaction(t *testing.T) {
	inst := mongotest.RunWithOptions(t, mongotest.DefaultOptions().WithReplicaSet("rs0"))
	ctx := context.Background()
	coll := inst.Database("mongotest").Collection("orders")

	session, err := inst.StartSession()
	require.NoError(t, err, "a session starts")
	defer session.EndSession(ctx)
	_, err = session.WithTransaction(ctx, func(sc mongo.SessionContext) (any, error) {
		return coll.InsertOne(sc, bson.M{"item": "book"})
	})

	require.NoError(t, err, "a transaction commits with no step but WithReplicaSet")
	n, err := coll.CountDocuments(ctx, bson.M{})
	require.NoError(t, err, "the collection can be counted")
	assert.EqualValues(t, 1, n, "and holds what the transaction wrote")
}

func TestIntegrationATLSClientIsConfiguredForTheContainer(t *testing.T) {
	inst := mongotest.RunWithOptions(t, mongotest.DefaultOptions().WithTLS())

	insertAndFind(t, inst.Client)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := mongotest.Attach(ctx, inst.URI())
	assert.ErrorIs(t, err, mongotest.ErrNotConnected, "the URI alone cannot connect: the server's certificate is trusted only through TLSConfig")
}

func TestIntegrationAttachConnectsWithoutStartingAnything(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	c, err := mongod.Start(ctx)
	require.NoError(t, err, "a server to attach to starts")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	inst, err := mongotest.Attach(ctx, c.URI())

	require.NoError(t, err, "attaching to a running server succeeds")
	insertAndFind(t, inst.Client)
	assert.Nil(t, inst.Container, "an attached instance has no container")
	assert.Equal(t, c.URI(), inst.URI(), "and its URI is the one it was given")
	require.NoError(t, inst.Stop(ctx), "stopping it disconnects")
	again, err := mongotest.Attach(ctx, c.URI())
	require.NoError(t, err, "and leaves the server running")
	assert.NoError(t, again.Stop(ctx), "the second client disconnects too")
}

func TestAttachToNothingIsNotConnected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)

	_, err := mongotest.Attach(ctx, "mongodb://127.0.0.1:1/?directConnection=true&serverSelectionTimeoutMS=300")

	assert.ErrorIs(t, err, mongotest.ErrNotConnected, "a server that cannot be reached is reported at once, not on the first command")
}

func TestIntegrationStopCanBeCalledAgainAndAfterADisconnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	inst, err := mongotest.Start(ctx)
	require.NoError(t, err, "a container starts")
	require.NoError(t, inst.Disconnect(ctx), "the caller disconnects the promoted client first")

	require.NoError(t, inst.Stop(ctx), "stopping still succeeds")

	assert.False(t, containerExists(t, inst.Container.ID()), "and removes the container")
	assert.NoError(t, inst.Stop(ctx), "a second call is harmless, as from a defer and a cleanup")
	assert.NoError(t, inst.KillMongoContainer(), "and so is the old name for it")
}

func TestTheOldErrorValuesStillMatch(t *testing.T) {
	nowhere, err := dockerapi.New(dockerapi.WithHost("unix:///nonexistent/docker.sock"))
	require.NoError(t, err, "a client for a daemon that is not there can be made")

	inst, err := mongotest.StartWithOptions(context.Background(), mongotest.DefaultOptions().WithDocker(nowhere))
	t.Cleanup(func() { _ = inst.Stop(context.Background()) })

	assert.ErrorIs(t, err, mongotest.ErrFailedToConnectToDockerDaemon, "code that checked for an unreachable daemon still finds it")
}

func TestIntegrationAContainerNameInUseMatchesTheOldError(t *testing.T) {
	name := "mongotest-name-in-use-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	opts := mongotest.DefaultOptions().WithName(name)
	mongotest.RunWithOptions(t, opts)

	_, err := mongotest.StartWithOptions(context.Background(), opts)

	assert.ErrorIs(t, err, mongotest.ErrMongoContainerAlreadyRunning, "code that checked for a running container still finds it")
}

func TestIntegrationTheOldConstructorsStillWork(t *testing.T) {
	t.Run("NewTestConnection(true)", func(t *testing.T) {
		conn, err := mongotest.NewTestConnection(true)
		require.NoError(t, err, "a container starts")
		t.Cleanup(func() { _ = conn.KillMongoContainer() })

		insertAndFind(t, conn.Client)
		assert.NotEmpty(t, conn.MongoContainerID(), "the container's id is reported")
	})
	t.Run("NewTestConnection(false)", func(t *testing.T) {
		if ln, err := net.Listen("tcp", "127.0.0.1:27017"); err != nil {
			t.Skip("port 27017 is in use here, and NewTestConnection(false) always connects to it")
		} else {
			_ = ln.Close()
		}
		mongotest.RunWithOptions(t, mongotest.DefaultOptions().WithPort(27017))

		conn, err := mongotest.NewTestConnection(false)

		require.NoError(t, err, "it connects to the server already on port 27017")
		t.Cleanup(func() { _ = conn.KillMongoContainer() })
		insertAndFind(t, conn.Client)
		assert.Empty(t, conn.MongoContainerID(), "and started no container")
	})
	t.Run("NewReplicaSetContainer", func(t *testing.T) {
		conn, err := mongotest.NewReplicaSetContainer("rs0")
		require.NoError(t, err, "a replica set starts")
		t.Cleanup(func() { _ = conn.KillMongoContainer() })

		session, err := conn.StartSession()
		require.NoError(t, err, "a session starts")
		defer session.EndSession(context.Background())
		_, err = session.WithTransaction(context.Background(), func(sc mongo.SessionContext) (any, error) {
			return conn.Database("mongotest").Collection("orders").InsertOne(sc, bson.M{"item": "book"})
		})
		assert.NoError(t, err, "and commits a transaction")
	})
}

func TestIntegrationMongoClientWithContainerCleansUp(t *testing.T) {
	var id string
	var given *mongo.Client

	err := mongotest.MongoClientWithContainer(func(client *mongo.Client) error {
		given = client
		insertAndFind(t, client)
		var status struct {
			Host string `bson:"host"`
		}
		err := client.Database("admin").RunCommand(context.Background(), bson.M{"serverStatus": 1}).Decode(&status)
		// A container's hostname is the start of its id unless one is set.
		id = status.Host
		require.True(t, containerExists(t, id), "the server's hostname names its container")
		return err
	})

	require.NoError(t, err, "the function is given a working client")
	assert.False(t, containerExists(t, id), "the container is removed once the function returns")
	assert.ErrorIs(t, given.Ping(context.Background(), nil), mongo.ErrClientDisconnected, "and the client is disconnected")
}

func TestIntegrationMongoClientWithContainerReturnsTheFunctionsError(t *testing.T) {
	failed := errors.New("the test's own failure")

	err := mongotest.MongoClientWithContainer(func(*mongo.Client) error { return failed })

	assert.ErrorIs(t, err, failed, "what the function returned reaches the caller")
}

func TestIntegrationScriptsAndCommandsRunInTheContainer(t *testing.T) {
	conn, err := mongotest.NewTestConnection(true)
	require.NoError(t, err, "a container starts")
	t.Cleanup(func() { _ = conn.KillMongoContainer() })

	out, err := conn.RunMongoScriptOnContainer("db.runCommand({ping: 1}).ok")
	require.NoError(t, err, "a script runs")
	assert.Equal(t, "1", out, "and its output is returned")

	_, err = conn.RunMongoScriptOnContainer("throw new Error('boom')")
	assert.ErrorIs(t, err, mongotest.ErrCommandFailed, "a script that throws is a failed command")

	out, err = conn.ExecCommandInMongoContainer([]string{"sh", "-c", "echo out; echo err >&2"})
	require.NoError(t, err, "a command runs")
	assert.Equal(t, "out\nerr\n", out, "and returns standard output followed by standard error")

	out, err = conn.ExecCommandInMongoContainer([]string{"sh", "-c", "echo out; exit 3"})
	assert.ErrorIs(t, err, mongotest.ErrCommandFailed, "a command that exits non-zero is an error")
	assert.Equal(t, "out\n", out, "returned with what it wrote")
}

func TestIntegrationAnAttachedInstanceHasNoContainerToRunIn(t *testing.T) {
	inst := mongotest.Run(t)
	attached, err := mongotest.Attach(context.Background(), inst.URI())
	require.NoError(t, err, "attaching succeeds")
	t.Cleanup(func() { _ = attached.Stop(context.Background()) })

	_, err = attached.RunMongoScriptOnContainer("1")
	assert.ErrorIs(t, err, mongotest.ErrNoContainer, "a script needs a container")
	_, err = attached.ExecCommandInMongoContainer([]string{"true"})
	assert.ErrorIs(t, err, mongotest.ErrNoContainer, "and so does a command")
	assert.Empty(t, attached.MongoContainerID(), "and it reports no container id")
}

func TestANilInstanceIsHarmless(t *testing.T) {
	var inst *mongotest.Instance

	assert.Empty(t, inst.MongoContainerID(), "old code that checked the id of a failed start gets nothing")
	assert.NoError(t, inst.KillMongoContainer(), "and stopping it does nothing")
	_, err := inst.RunMongoScriptOnContainer("1")
	assert.ErrorIs(t, err, mongotest.ErrNoContainer, "and it has nothing to run a script in")
}

func TestGetAvailablePortReturnsAPortThatCanBeListenedOn(t *testing.T) {
	port, err := mongotest.GetAvailablePort()
	require.NoError(t, err, "a port is found")

	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))

	require.NoError(t, err, "and is free to listen on")
	_ = ln.Close()
}

func TestGenerateCARootMakesASelfSignedCA(t *testing.T) {
	cert, certPEM, key := mongotest.GenerateCARoot()

	require.NotNil(t, key, "a key is returned")
	assert.True(t, cert.IsCA, "the certificate is a CA")
	block, _ := pem.Decode(certPEM)
	require.NotNil(t, block, "the PEM decodes")
	assert.Equal(t, cert.Raw, block.Bytes, "to the same certificate")
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	_, err := cert.Verify(x509.VerifyOptions{Roots: roots})
	assert.NoError(t, err, "and it verifies against itself")
}
