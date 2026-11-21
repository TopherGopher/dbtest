package mongotest_test

import (
	"context"
	"log"
	"testing"

	"github.com/tophergopher/dbtest/mongotest"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// These examples are compiled but not run, because they need a Docker
// daemon.

// Run is all a test needs: a connected client, and a container that is
// removed when the test ends.
func ExampleRun() {
	var t *testing.T // the test's own t
	orders := mongotest.Run(t).Database("shop").Collection("orders")
	_, _ = orders.InsertOne(t.Context(), bson.M{"item": "book"})
}

// Options are chained onto DefaultOptions. A replica set is ready for
// transactions when Run returns.
func ExampleRunWithOptions() {
	var t *testing.T // the test's own t
	inst := mongotest.RunWithOptions(t, mongotest.DefaultOptions().WithReplicaSet("rs0"))
	orders := inst.Database("shop").Collection("orders")
	session, err := inst.StartSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.EndSession(t.Context())
	_, err = session.WithTransaction(t.Context(), func(ctx mongo.SessionContext) (any, error) {
		return orders.InsertOne(ctx, bson.M{"item": "book"})
	})
	if err != nil {
		t.Fatal(err)
	}
}

// One server can serve every test in a package. This is the body of the
// TestMain that starts it. It returns after m.Run and does not call os.Exit,
// which would skip the deferred Stop and leave the container running.
func ExampleStart() {
	var m *testing.M // TestMain's own m
	ctx := context.Background()
	server, err := mongotest.Start(ctx) // a real TestMain assigns this to a package variable
	if err != nil {
		log.Fatal(err)
	}
	defer server.Stop(ctx)
	m.Run()
}

// Tests that share a server each take a database of their own, which is
// dropped when the test ends.
func ExampleInstance_DatabaseFor() {
	var t *testing.T               // the test's own t
	var server *mongotest.Instance // the server the package's tests share
	orders := server.DatabaseFor(t).Collection("orders")
	_, _ = orders.InsertOne(t.Context(), bson.M{"item": "book"})
}

// Attach connects to a server that is already running, such as a CI service
// container. Its instance has no container, and Stop only disconnects.
func ExampleAttach() {
	ctx := context.Background()
	inst, err := mongotest.Attach(ctx, "mongodb://localhost:27017")
	if err != nil {
		log.Fatal(err)
	}
	defer inst.Stop(ctx)
}
