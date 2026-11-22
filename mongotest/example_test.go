package mongotest_test

import (
	"context"
	"log"
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

// These examples are compiled but not run, because they need a Docker
// daemon. The documentation site shows their code: a comment holding --8<--
// marks where the part it shows starts or ends.

// hero is a document the examples store.
type hero struct {
	Name string `bson:"name"`
	City string `bson:"city"`
}

// Run is all a test needs: a connected client, and a container that is
// removed when the test ends.
func ExampleRun() {
	var t *testing.T // the test's own t --8<-- [start:run]
	// Starts MongoDB in a container, connects, and removes the container
	// when the test ends, pass or fail.
	orders := mongotest.Run(t).Database("shop").Collection("orders")

	_, err := orders.InsertOne(t.Context(), bson.M{"item": "book"})
	require.NoError(t, err)
} // --8<-- [end:run]

// Options are chained onto DefaultOptions. A replica set is ready for
// transactions when Run returns.
func ExampleRunWithOptions() {
	var t *testing.T // the test's own t --8<-- [start:transaction]
	inst := mongotest.RunWithOptions(t, mongotest.DefaultOptions().WithReplicaSet("rs0"))

	ctx := t.Context()
	session, err := inst.StartSession()
	require.NoError(t, err)
	defer session.EndSession(ctx)

	_, err = session.WithTransaction(ctx, func(sc mongo.SessionContext) (any, error) {
		coll := inst.Database("bank").Collection("accounts")
		if _, err := coll.InsertOne(sc, bson.M{"owner": "a", "balance": 100}); err != nil {
			return nil, err
		}
		_, err := coll.InsertOne(sc, bson.M{"owner": "b", "balance": 0})
		return nil, err
	})
	require.NoError(t, err, "a transaction must commit on a single-member replica set")
} // --8<-- [end:transaction]

// A change stream needs a replica set too.
func ExampleRunWithOptions_changeStream() {
	var t *testing.T // the test's own t

	inst := mongotest.RunWithOptions(t, mongotest.DefaultOptions().WithReplicaSet("rs0")) // --8<-- [start:change-stream]
	// A deadline keeps the test from hanging if no event arrives.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	coll := inst.Database("app").Collection("events")
	stream, err := coll.Watch(ctx, mongo.Pipeline{})
	require.NoError(t, err)
	defer stream.Close(ctx)

	_, err = coll.InsertOne(ctx, bson.M{"kind": "created"})
	require.NoError(t, err)

	require.True(t, stream.Next(ctx), "the insert must appear on the change stream")
} // --8<-- [end:change-stream]

// A script run in the container can load the data a test starts from.
func ExampleRun_script() {
	var t *testing.T // the test's own t

	inst := mongotest.Run(t) // --8<-- [start:script]
	out, err := inst.Container.RunScript(t.Context(), `
	    const app = db.getSiblingDB('app');
	    app.roster.insertMany([
	        { name: 'Diana', city: 'Themyscira' },
	        { name: 'Bruce', city: 'Gotham' },
	    ]);
	    app.roster.countDocuments()
	`)
	require.NoError(t, err)
	require.Equal(t, "2", out)
} // --8<-- [end:script]

// The tools in the image, such as mongoimport, run in the container too. A
// command that exits non-zero is not an error, so check ExitCode.
func ExampleRun_mongoimport() {
	var t *testing.T // the test's own t

	inst := mongotest.Run(t) // --8<-- [start:mongoimport]
	docs := `{"name":"Diana","city":"Themyscira"}
	{"name":"Bruce","city":"Gotham"}`

	result, err := inst.Container.Exec(t.Context(),
		"sh", "-c", "printf '%s' '"+docs+"' | mongoimport --quiet --db app --collection roster")
	require.NoError(t, err)
	require.Zero(t, result.ExitCode, result.Stderr)
} // --8<-- [end:mongoimport]

// One server can serve every test in a package. This is the body of the
// TestMain that starts it. It returns after m.Run and does not call os.Exit,
// which would skip the deferred Stop and leave the container running.
func ExampleStart() {
	var m *testing.M // TestMain's own m --8<-- [start:test-main]
	ctx := context.Background()
	server, err := mongotest.Start(ctx) // a real TestMain assigns this to a package variable
	if err != nil {
		log.Fatal(err)
	}
	defer server.Stop(ctx)
	m.Run()
} // --8<-- [end:test-main]

// Tests that share a server each take a database of their own, which is
// dropped when the test ends.
func ExampleInstance_DatabaseFor() {
	var t *testing.T               // the test's own t
	var server *mongotest.Instance // the server the package's tests share --8<-- [start:database-for]
	orders := server.DatabaseFor(t).Collection("orders")
	_, err := orders.InsertOne(t.Context(), bson.M{"item": "book"})
	require.NoError(t, err)
} // --8<-- [end:database-for]

// In a table-driven test, each parallel case takes a database of its own
// from the one server the test started.
func ExampleInstance_DatabaseFor_table() {
	var t *testing.T // the test's own t --8<-- [start:table]
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

// A client of your own connects with URI. Disconnect it in a cleanup the
// test registers, which runs before the one that removes the container.
func ExampleInstance_URI() {
	var t *testing.T // the test's own t --8<-- [start:own-client]
	inst := mongotest.Run(t)

	client, err := mongo.Connect(t.Context(), options.Client().ApplyURI(inst.URI()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Disconnect(context.Background()) }) // runs first
} // --8<-- [end:own-client]

// Attach connects to a server that is already running, such as a CI service
// container. Its instance has no container, and Stop only disconnects.
func ExampleAttach() /* --8<-- [start:attach] */ {
	ctx := context.Background()
	inst, err := mongotest.Attach(ctx, "mongodb://localhost:27017")
	if err != nil {
		log.Fatal(err)
	}
	defer inst.Stop(ctx)
} // --8<-- [end:attach]

// startMongo is the helper the CI guide shows.
//
// --8<-- [start:ci-helper]
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
