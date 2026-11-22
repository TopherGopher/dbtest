package pgtest_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"testing"

	"github.com/tophergopher/dbtest/pgtest"
)

// Most of these examples are compiled but not run, because they need a
// Docker daemon or a server.

// Run is all a test needs: a pgx pool and a *sql.DB, both connected, and a
// container that is removed when the test ends.
func ExampleRun() {
	var t *testing.T // the test's own t
	inst := pgtest.Run(t)
	_, _ = inst.DB.ExecContext(t.Context(), "CREATE TABLE orders (item text)")
	_, _ = inst.Pool.Exec(t.Context(), "INSERT INTO orders VALUES ($1)", "book")
}

// Options are chained onto DefaultOptions. A lone version runs the official
// image at that tag.
func ExampleRunWithOptions() {
	var t *testing.T // the test's own t
	inst := pgtest.RunWithOptions(t, pgtest.DefaultOptions().WithImage("17"))
	_, _ = inst.DB.ExecContext(t.Context(), "CREATE EXTENSION IF NOT EXISTS pgcrypto")
}

// One server can serve every test in a package. This is the body of the
// TestMain that starts it. It returns after m.Run and does not call os.Exit,
// which would skip the deferred Stop and leave the container running.
func ExampleStart() {
	var m *testing.M // TestMain's own m
	ctx := context.Background()
	server, err := pgtest.Start(ctx) // a real TestMain assigns this to a package variable
	if err != nil {
		log.Fatal(err)
	}
	defer server.Stop(ctx)
	m.Run()
}

// Tests that share a server each take a database of their own, which is
// dropped when the test ends.
func ExampleInstance_DatabaseFor() {
	var t *testing.T            // the test's own t
	var server *pgtest.Instance // the server the package's tests share
	db := server.DatabaseFor(t).DB
	_, _ = db.ExecContext(t.Context(), "CREATE TABLE orders (item text)")
}

// Attach connects to a server that is already running, such as a CI service
// container. Its instance has no container, and Stop only closes the
// clients.
func ExampleAttach() /* --8<-- [start:attach] */ {
	ctx := context.Background()
	inst, err := pgtest.Attach(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	defer inst.Stop(ctx)
} // --8<-- [end:attach]

// An empty connection string is refused, rather than filled in from the PG*
// environment variables, so an unset DATABASE_URL cannot point a test at
// some other server.
func ExampleAttach_emptyURI() {
	_, err := pgtest.Attach(context.Background(), "")
	fmt.Println(errors.Is(err, pgtest.ErrEmptyURI))
	// Output: true
}

// A connection the code under test never gave back is reported when the
// instance stops, rather than making Stop wait for it.
func ExampleErrConnectionsHeld() {
	var t *testing.T            // the test's own t
	var server *pgtest.Instance // started in TestMain
	rows, _ := server.Pool.Query(t.Context(), "SELECT 1")
	_ = rows // never closed: rows.Close() was forgotten
	if err := server.Stop(context.Background()); errors.Is(err, pgtest.ErrConnectionsHeld) {
		log.Print(err)
	}
}
