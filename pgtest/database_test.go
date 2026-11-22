package pgtest_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/dbtest/pgtest"
)

// createIn creates a table in a test's database and writes a row to it.
func createIn(t *testing.T, d *pgtest.Database) {
	t.Helper()
	ctx := context.Background()
	_, err := d.DB.ExecContext(ctx, "CREATE TABLE people (name text)")
	require.NoError(t, err, "the test's database takes a table")
	_, err = d.Pool.Exec(ctx, "INSERT INTO people VALUES ('Diana')")
	require.NoError(t, err, "and a row in it")
}

func TestIntegrationDatabaseForGivesEachCallItsOwn(t *testing.T) {
	inst := pgtest.Run(t)

	first, second := inst.DatabaseFor(t), inst.DatabaseFor(t)
	createIn(t, first)

	assert.NotEqual(t, first.Name(), second.Name(), "two calls for the same test get different databases")
	_, err := second.DB.ExecContext(context.Background(), "SELECT * FROM people")
	assert.Error(t, err, "and the second does not see a table made in the first")
}

func TestIntegrationDatabaseForConnectsBothClientsToTheTestsDatabase(t *testing.T) {
	inst := pgtest.Run(t)
	d := inst.DatabaseFor(t)

	var fromDB, fromPool string
	require.NoError(t, d.DB.QueryRowContext(context.Background(), "SELECT current_database()").Scan(&fromDB), "database/sql can query")
	require.NoError(t, d.Pool.QueryRow(context.Background(), "SELECT current_database()").Scan(&fromPool), "the pool can query")

	assert.Equal(t, d.Name(), fromDB, "database/sql is connected to the test's database")
	assert.Equal(t, d.Name(), fromPool, "and so is the pool")
}

func TestIntegrationDatabaseForTakesAnyTestName(t *testing.T) {
	inst := pgtest.Run(t)

	// Each case, as a database name, needs quoting, is mixed case, or is
	// longer than the server's 63 bytes. None relies on a space, because
	// t.Run turns spaces into underscores.
	for _, name := range []string{
		"v1.5",
		"Orders",
		`a_"quoted"_word`,
		"drop_table;--",
		"a name that is a whole sentence, as long as people really write them in a table of cases",
		strings.Repeat("é", 40),
	} {
		t.Run(name, func(t *testing.T) {
			createIn(t, inst.DatabaseFor(t))
		})
	}
}

func TestIntegrationDatabaseForServesParallelTests(t *testing.T) {
	inst := pgtest.Run(t)

	for i := range 8 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			createIn(t, inst.DatabaseFor(t))
		})
	}
}

func TestIntegrationDatabaseForDropsTheDatabaseWhenTheTestEnds(t *testing.T) {
	inst := pgtest.Run(t)

	var name string
	ran := t.Run("a test that writes", func(t *testing.T) {
		d := inst.DatabaseFor(t)
		name = d.Name()
		createIn(t, d)
		require.True(t, databaseExists(t, inst.Pool, name), "the database is there during the test")
	})
	require.True(t, ran, "the test that writes passes, so its database was there")

	assert.False(t, databaseExists(t, inst.Pool, name), "and is dropped once the test has ended")
}

func TestIntegrationDatabaseForReportsAHeldConnectionAndStillDrops(t *testing.T) {
	inst := pgtest.Run(t)
	test := &recorder{TB: t}
	d := inst.DatabaseFor(test)
	_, err := d.Pool.Query(context.Background(), "SELECT 1")
	require.NoError(t, err, "the query runs")

	test.end()

	assert.True(t, test.Failed(), "a connection held past the test's end fails the test")
	assert.False(t, databaseExists(t, inst.Pool, d.Name()), "and the database is dropped all the same")
}

func TestIntegrationDatabaseForReportsNothingOnceTheInstanceIsStopped(t *testing.T) {
	inst := pgtest.Run(t)
	test := &recorder{TB: t}
	createIn(t, inst.DatabaseFor(test))

	// A deferred Stop runs before a test's cleanups, so this is the order a
	// test that starts and stops its own server ends in.
	require.NoError(t, inst.Stop(context.Background()), "the instance stops")
	test.end()

	assert.False(t, test.Failed(), "a database that went with its server is not a failed drop: %v", test.failures)
}

func TestIntegrationDatabaseForSaysWhichPrivilegeIsMissing(t *testing.T) {
	ctx := context.Background()
	inst := pgtest.Run(t)
	_, err := inst.Pool.Exec(ctx, "CREATE ROLE reader LOGIN PASSWORD 'reader' NOCREATEDB")
	require.NoError(t, err, "a role that cannot create databases is made")
	cfg, err := pgx.ParseConfig(inst.URI())
	require.NoError(t, err, "the instance's URI parses")
	reader, err := pgtest.Attach(ctx, fmt.Sprintf("postgres://reader:reader@%s:%d/postgres?sslmode=disable", cfg.Host, cfg.Port))
	require.NoError(t, err, "the role attaches")
	t.Cleanup(func() { _ = reader.Stop(context.Background()) })
	test := &recorder{TB: t}
	returned := false

	inGoroutine(func() {
		reader.DatabaseFor(test)
		returned = true
	})
	test.end()

	require.True(t, test.Failed(), "a database the role may not create fails the test")
	assert.False(t, returned, "and ends it, so no caller is handed a database that is not there")
	assert.Contains(t, strings.Join(test.failures, "\n"), "CREATEDB", "and names the privilege the role needs")
}
