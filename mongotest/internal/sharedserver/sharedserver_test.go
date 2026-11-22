// Package sharedserver's tests share one server, which TestMain starts. The
// guides on the documentation site show this code, from the --8<-- markers.
package sharedserver

import (
	"context"
	"log"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tophergopher/dbtest/mongotest"
	"go.mongodb.org/mongo-driver/bson"
)

// --8<-- [start:test-main]

var server *mongotest.Instance

func TestMain(m *testing.M) {
	ctx := context.Background()
	var err error
	if server, err = mongotest.Start(ctx); err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := server.Stop(ctx); err != nil {
			log.Print(err)
		}
	}()
	m.Run()
}

// --8<-- [end:test-main]

func TestIntegrationATestTakesADatabaseOfItsOwn(t *testing.T) /* --8<-- [start:database-for] */ {
	t.Parallel()
	orders := server.DatabaseFor(t).Collection("orders")

	_, err := orders.InsertOne(t.Context(), bson.M{"item": "book"})
	require.NoError(t, err, "the test's own database takes a write")
} // --8<-- [end:database-for]
