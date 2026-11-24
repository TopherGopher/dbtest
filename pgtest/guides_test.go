package pgtest_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver with database/sql
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/dbtest/pgtest"
	"github.com/tophergopher/dbtest/postgres"
)

// The documentation site shows the code of these tests, from the --8<--
// markers, so what a page claims is what these tests check.

func TestIntegrationRunIsAllATestNeeds(t *testing.T) /* --8<-- [start:run] */ {
	// Starts PostgreSQL in a container, connects, and removes the container
	// when the test ends, pass or fail.
	db := pgtest.Run(t).DB

	_, err := db.ExecContext(t.Context(), "CREATE TABLE orders (item text)")
	require.NoError(t, err, "the server takes a table")
	_, err = db.ExecContext(t.Context(), "INSERT INTO orders VALUES ($1)", "book")
	require.NoError(t, err, "and a row in it")
} // --8<-- [end:run]

func TestIntegrationThePoolTakesPgxQueries(t *testing.T) /* --8<-- [start:pool] */ {
	pool := pgtest.Run(t).Pool

	var sum int
	err := pool.QueryRow(t.Context(), "SELECT $1::int + $2::int", 2, 3).Scan(&sum)

	require.NoError(t, err, "the pool runs a query")
	assert.Equal(t, 5, sum, "and returns its result")
} // --8<-- [end:pool]

// The postgres package returns a URI and imports no driver. This is what a
// caller writes to hand that URI to pgx without pgtest.
func TestIntegrationPgxTakesAPostgresContainersURI(t *testing.T) {
	ctx := context.Background()
	c, err := postgres.Start(ctx)
	require.NoError(t, err, "a container starts")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	// --8<-- [start:drivers]
	// database/sql, with _ "github.com/jackc/pgx/v5/stdlib" imported
	db, err := sql.Open("pgx", c.URI())
	require.NoError(t, err)
	defer db.Close()

	// pgx's own pool
	pool, err := pgxpool.New(ctx, c.URI())
	require.NoError(t, err)
	defer pool.Close()
	// --8<-- [end:drivers]

	assert.NoError(t, db.PingContext(ctx), "database/sql reaches the server at the URI")
	assert.NoError(t, pool.Ping(ctx), "and so does pgx's pool")
}
