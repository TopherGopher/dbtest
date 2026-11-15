package mongotest_test

import (
	"context"
	"errors"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest"
	"github.com/tophergopher/mongotest/mongod"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// These tests start real containers. They need a Docker daemon, and fail
// rather than skip without one.

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

func TestIntegrationRunGivesAConnectedClient(t *testing.T) {
	inst := mongotest.Run(t)

	insertAndFind(t, inst.Client)
	assert.NotNil(t, inst.Container, "the instance has the container it started")
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
	_, err = mongotest.Attach(ctx, c.URI())
	assert.NoError(t, err, "and leaves the server running")
}

func TestIntegrationAttachToNothingFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)

	_, err := mongotest.Attach(ctx, "mongodb://127.0.0.1:1/?directConnection=true&serverSelectionTimeoutMS=300")

	assert.Error(t, err, "a server that cannot be reached is reported at once, not on the first command")
}

func TestIntegrationStopRemovesTheContainerAndCanBeCalledTwice(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	inst, err := mongotest.Start(ctx)
	require.NoError(t, err, "a container starts")
	address := net.JoinHostPort(inst.Container.Host(), strconv.Itoa(inst.Container.Port()))

	require.NoError(t, inst.Stop(ctx), "stopping succeeds")
	assert.NoError(t, inst.Stop(ctx), "and a second call is harmless, as from a defer and a cleanup")
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err == nil {
		_ = conn.Close()
	}
	assert.Error(t, err, "nothing answers where the container was")
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
	var hostname string

	err := mongotest.MongoClientWithContainer(func(client *mongo.Client) error {
		insertAndFind(t, client)
		var status struct {
			Host string `bson:"host"`
		}
		err := client.Database("admin").RunCommand(context.Background(), bson.M{"serverStatus": 1}).Decode(&status)
		hostname = status.Host
		return err
	})

	require.NoError(t, err, "the function is given a working client")
	// A container's hostname is the start of its id unless one is set.
	out, err := exec.Command("docker", "ps", "-aq", "--filter", "id="+hostname).Output()
	require.NoError(t, err, "the docker CLI lists containers")
	assert.Empty(t, strings.TrimSpace(string(out)), "the container is removed once the function returns")
}

func TestIntegrationMongoClientWithContainerReturnsTheFunctionsError(t *testing.T) {
	failed := errors.New("the test's own failure")

	err := mongotest.MongoClientWithContainer(func(*mongo.Client) error { return failed })

	assert.ErrorIs(t, err, failed, "what the function returned reaches the caller")
}

func TestIntegrationKillMongoContainerTwiceIsHarmless(t *testing.T) {
	conn, err := mongotest.NewTestConnection(true)
	require.NoError(t, err, "a container starts")

	require.NoError(t, conn.KillMongoContainer(), "the container is removed")
	assert.NoError(t, conn.KillMongoContainer(), "and a second call returns nil")
}

func TestIntegrationScriptsAndCommandsRunInTheContainer(t *testing.T) {
	conn, err := mongotest.NewTestConnection(true)
	require.NoError(t, err, "a container starts")
	t.Cleanup(func() { _ = conn.KillMongoContainer() })

	out, err := conn.RunMongoScriptOnContainer("db.runCommand({ping: 1}).ok")
	require.NoError(t, err, "a script runs")
	assert.Equal(t, "1", out, "and its output is returned")

	_, err = conn.ExecCommandInMongoContainer([]string{"sh", "-c", "exit 3"})
	assert.ErrorIs(t, err, mongotest.ErrCommandFailed, "a command that exits non-zero is an error")
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

func TestGetAvailablePortReturnsAPortThatCanBeListenedOn(t *testing.T) {
	port, err := mongotest.GetAvailablePort()
	require.NoError(t, err, "a port is found")

	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))

	require.NoError(t, err, "and is free to listen on")
	_ = ln.Close()
}
