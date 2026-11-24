package mongotest_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/dbtest/dockerapi"
	"github.com/tophergopher/dbtest/dockerclient"
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
	ran := t.Run("a test that uses a server", func(t *testing.T) {
		inst = mongotest.Run(t)
		require.True(t, containerExists(t, inst.Container.ID()), "the container runs during the test")
	})
	require.True(t, ran, "the test that uses a server passes, so it has an instance")

	assert.False(t, containerExists(t, inst.Container.ID()), "and is removed once the test has ended")
	assert.ErrorIs(t, inst.Ping(context.Background(), nil), mongo.ErrClientDisconnected, "and the client is disconnected")
}

func TestIntegrationAReplicaSetCommitsATransaction(t *testing.T) /* --8<-- [start:transaction] */ {
	inst := mongotest.RunWithOptions(t, mongotest.DefaultOptions().WithReplicaSet("rs0"))
	ctx := t.Context()
	coll := inst.Database("shop").Collection("orders")

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
} // --8<-- [end:transaction]

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
	t.Cleanup(func() { _ = inst.Stop(context.Background()) })
	require.NoError(t, inst.Disconnect(ctx), "the caller disconnects the promoted client first")

	require.NoError(t, inst.Stop(ctx), "stopping still succeeds")

	assert.False(t, containerExists(t, inst.Container.ID()), "and removes the container")
	assert.NoError(t, inst.Stop(ctx), "a second call is harmless, as from a defer and a cleanup")
}

func TestAStartWithNoDaemonIsAConnectionFailure(t *testing.T) {
	nowhere, err := dockerapi.New(dockerapi.WithHost("unix:///nonexistent/docker.sock"))
	require.NoError(t, err, "a client for a daemon that is not there can be made")

	inst, err := mongotest.StartWithOptions(context.Background(), mongotest.DefaultOptions().WithDocker(nowhere))
	t.Cleanup(func() { _ = inst.Stop(context.Background()) })

	assert.ErrorIs(t, err, dockerclient.ErrConnectionFailed, "callers can detect an unreachable daemon with errors.Is")
}

func TestRunFailsAndDoesNotSkipWhenTheStartFails(t *testing.T) {
	nowhere, err := dockerapi.New(dockerapi.WithHost("unix:///nonexistent/docker.sock"))
	require.NoError(t, err, "a client for a daemon that is not there can be made")
	test := &recorder{TB: t}
	returned := false

	inGoroutine(func() {
		mongotest.RunWithOptions(test, mongotest.DefaultOptions().WithDocker(nowhere))
		returned = true
	})
	test.end()

	assert.True(t, test.Failed(), "a start that fails fails the test")
	assert.False(t, test.Skipped(), "and never skips it, so a broken setup cannot pass unnoticed")
	assert.False(t, returned, "and ends it, so no caller is handed a nil instance")
}

func TestStoppingANilInstanceDoesNothing(t *testing.T) {
	var inst *mongotest.Instance

	assert.NoError(t, inst.Stop(context.Background()), "a failed start's nil instance can be stopped from a defer")
}
