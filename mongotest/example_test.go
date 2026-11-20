package mongotest_test

import (
	"context"
	"log"
	"testing"

	"github.com/tophergopher/dbtest/mongotest"
	"go.mongodb.org/mongo-driver/bson"
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
	session, _ := inst.StartSession()
	defer session.EndSession(t.Context())
}

// Outside a test, such as in TestMain, Start returns an instance its caller
// stops.
func ExampleStart() {
	ctx := context.Background()
	inst, err := mongotest.Start(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer inst.Stop(ctx)
	_ = inst.Database("shop")
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
