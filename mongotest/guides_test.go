package mongotest_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/dbtest/mongotest"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// The guides on the documentation site show the code of these tests, from
// the --8<-- markers, so what a guide claims is what these tests check.

// hero is a document the guides store.
type hero struct {
	Name string `bson:"name"`
	City string `bson:"city"`
}

func TestIntegrationRunIsAllATestNeeds(t *testing.T) /* --8<-- [start:run] */ {
	// Starts MongoDB in a container, connects, and removes the container
	// when the test ends, pass or fail.
	orders := mongotest.Run(t).Database("shop").Collection("orders")

	_, err := orders.InsertOne(t.Context(), bson.M{"item": "book"})
	require.NoError(t, err, "the server takes a write")
} // --8<-- [end:run]

func TestIntegrationAChangeStreamSeesAnInsert(t *testing.T) /* --8<-- [start:change-stream] */ {
	inst := mongotest.RunWithOptions(t, mongotest.DefaultOptions().WithReplicaSet("rs0"))
	// A deadline keeps the test from hanging if no event arrives.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	coll := inst.Database("app").Collection("events")
	stream, err := coll.Watch(ctx, mongo.Pipeline{})
	require.NoError(t, err, "a replica set serves change streams")
	defer stream.Close(ctx)

	_, err = coll.InsertOne(ctx, bson.M{"kind": "created"})
	require.NoError(t, err, "the insert succeeds")

	require.True(t, stream.Next(ctx), "the insert must appear on the change stream: %v", stream.Err())
	assert.Equal(t, "insert", stream.Current.Lookup("operationType").StringValue(), "the event is the insert")
} // --8<-- [end:change-stream]

func TestIntegrationAScriptReturnsItsLastValue(t *testing.T) /* --8<-- [start:script] */ {
	inst := mongotest.Run(t)

	out, err := inst.Container.RunScript(t.Context(), `
	    const app = db.getSiblingDB('app');
	    app.roster.insertMany([
	        { name: 'Diana', city: 'Themyscira' },
	        { name: 'Bruce', city: 'Gotham' },
	    ]);
	    app.roster.countDocuments()
	`)
	require.NoError(t, err, "the script runs")
	assert.Equal(t, "2", out, "RunScript returns the value of the last expression")
} // --8<-- [end:script]

func TestIntegrationMongoimportSeedsACollection(t *testing.T) /* --8<-- [start:mongoimport] */ {
	inst := mongotest.Run(t)
	docs := `{"name":"Diana","city":"Themyscira"}
	{"name":"Bruce","city":"Gotham"}`

	result, err := inst.Container.Exec(t.Context(),
		"sh", "-c", "printf '%s' '"+docs+"' | mongoimport --quiet --db app --collection roster")
	require.NoError(t, err, "the command runs")
	require.Zero(t, result.ExitCode, "mongoimport succeeds: %s", result.Stderr)

	n, err := inst.Database("app").Collection("roster").CountDocuments(t.Context(), bson.M{})
	require.NoError(t, err, "the collection can be counted")
	assert.EqualValues(t, 2, n, "every line is imported")
} // --8<-- [end:mongoimport]

func TestIntegrationEachParallelCaseHasADatabaseOfItsOwn(t *testing.T) /* --8<-- [start:table] */ {
	inst := mongotest.Run(t)

	cases := []struct {
		name   string
		seed   []hero
		filter bson.M
		want   int
	}{
		{"by city", []hero{{Name: "Diana", City: "Themyscira"}}, bson.M{"city": "Themyscira"}, 1},
		{"no match", []hero{{Name: "Diana", City: "Themyscira"}}, bson.M{"city": "Gotham"}, 0},
		{"empty collection", nil, bson.M{}, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// A database for this case alone, dropped when the case ends.
			coll := inst.DatabaseFor(t).Collection("roster")
			for _, h := range tc.seed {
				_, err := coll.InsertOne(t.Context(), h)
				require.NoError(t, err, "seeding %q must succeed", tc.name)
			}

			n, err := coll.CountDocuments(t.Context(), tc.filter)
			require.NoError(t, err, "the query must run")
			assert.EqualValues(t, tc.want, n, "the filter must match the seeded documents")
		})
	}
} // --8<-- [end:table]

func TestIntegrationAClientOfYourOwnConnectsWithURI(t *testing.T) /* --8<-- [start:own-client] */ {
	inst := mongotest.Run(t)

	client, err := mongo.Connect(t.Context(), options.Client().ApplyURI(inst.URI()))
	require.NoError(t, err, "the URI is valid")
	// Registered after Run's cleanup, so it runs before the container is
	// removed.
	t.Cleanup(func() { _ = client.Disconnect(context.Background()) })

	assert.NoError(t, client.Ping(t.Context(), nil), "the client reaches the server")
} // --8<-- [end:own-client]

// --8<-- [start:ci-helper]
// startMongo connects to the server MONGODB_URI names, such as a CI service
// container, and starts one in a container when the variable is unset.
func startMongo(t *testing.T) *mongotest.Instance {
	t.Helper()
	uri := os.Getenv("MONGODB_URI")
	if uri == "" {
		return mongotest.Run(t)
	}
	inst, err := mongotest.Attach(t.Context(), uri)
	require.NoError(t, err, "the MongoDB service must be reachable")
	t.Cleanup(func() { _ = inst.Stop(context.Background()) })
	return inst
}

// --8<-- [end:ci-helper]

func TestIntegrationStartMongoStartsAServerWithoutMONGODB_URI(t *testing.T) {
	t.Setenv("MONGODB_URI", "")

	inst := startMongo(t)

	assert.NotNil(t, inst.Container, "with no URI to attach to, a container is started")
	assert.NoError(t, inst.Ping(t.Context(), nil), "and the instance is connected")
}

func TestIntegrationStartMongoAttachesToMONGODB_URI(t *testing.T) {
	service := mongotest.Run(t)
	t.Setenv("MONGODB_URI", service.URI())

	inst := startMongo(t)

	assert.Nil(t, inst.Container, "the server MONGODB_URI names is used, and nothing is started")
	assert.NoError(t, inst.Ping(t.Context(), nil), "and the instance is connected to it")
}
