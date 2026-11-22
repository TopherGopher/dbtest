// Package sharedserver's tests share one server, which TestMain starts. The
// documentation site shows this code, from the --8<-- markers.
package sharedserver

import (
	"context"
	"log"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tophergopher/dbtest/pgtest"
)

// --8<-- [start:test-main]

var server *pgtest.Instance

func TestMain(m *testing.M) {
	ctx := context.Background()
	var err error
	if server, err = pgtest.Start(ctx); err != nil {
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
	db := server.DatabaseFor(t).DB

	_, err := db.ExecContext(t.Context(), "CREATE TABLE orders (item text)")
	require.NoError(t, err, "the test's own database takes a table")
} // --8<-- [end:database-for]

func TestIntegrationAnotherTestDoesNotSeeIt(t *testing.T) {
	t.Parallel()
	db := server.DatabaseFor(t).DB

	_, err := db.ExecContext(t.Context(), "CREATE TABLE orders (item text)")
	require.NoError(t, err, "a table of the same name is made in another test's database")
}
