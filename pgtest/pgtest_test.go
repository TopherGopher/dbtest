package pgtest_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/dbtest/dockerapi"
	"github.com/tophergopher/dbtest/dockerclient"
	"github.com/tophergopher/dbtest/pgtest"
	"github.com/tophergopher/dbtest/postgres"
)

// writeAndRead creates a table through db, writes a row through pool, and
// reads it back through db, so both clients are shown to reach one server.
func writeAndRead(t *testing.T, db *sql.DB, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	_, err := db.ExecContext(ctx, "CREATE TABLE people (name text)")
	require.NoError(t, err, "database/sql can create a table")
	_, err = pool.Exec(ctx, "INSERT INTO people VALUES ($1)", "Diana")
	require.NoError(t, err, "the pool can write to it")
	var name string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT name FROM people").Scan(&name), "database/sql reads what the pool wrote")
	assert.Equal(t, "Diana", name, "and both reach the same database")
}

func TestIntegrationRunGivesAConnectedPoolAndDB(t *testing.T) {
	inst := pgtest.Run(t)

	writeAndRead(t, inst.DB, inst.Pool)
	assert.NotNil(t, inst.Container, "the instance has the container it started")
}

func TestIntegrationRunRemovesTheContainerWhenTheTestEnds(t *testing.T) {
	var inst *pgtest.Instance
	ran := t.Run("a test that uses a server", func(t *testing.T) {
		inst = pgtest.Run(t)
		require.True(t, containerExists(t, inst.Container.ID()), "the container runs during the test")
	})
	require.True(t, ran, "the test that uses a server passes, so it has an instance")

	assert.False(t, containerExists(t, inst.Container.ID()), "the container is removed once the test has ended")
	assert.Error(t, inst.Pool.Ping(context.Background()), "and the pool is closed")
	assert.Error(t, inst.DB.Ping(), "and so is the database/sql handle")
}

func TestIntegrationAttachConnectsWithoutStartingAnything(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	c, err := postgres.Start(ctx)
	require.NoError(t, err, "a server to attach to starts")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	inst, err := pgtest.Attach(ctx, c.URI())

	require.NoError(t, err, "attaching to a running server succeeds")
	writeAndRead(t, inst.DB, inst.Pool)
	assert.Nil(t, inst.Container, "an attached instance has no container")
	assert.Equal(t, c.URI(), inst.URI(), "and its URI is the one it was given")
	require.NoError(t, inst.Stop(ctx), "stopping it closes the clients")
	again, err := pgtest.Attach(ctx, c.URI())
	require.NoError(t, err, "and leaves the server running")
	assert.NoError(t, again.Stop(ctx), "the second instance stops too")
}

// leaks are the usual ways a test keeps a connection past its end.
var leaks = map[string]func(t *testing.T, inst *pgtest.Instance){
	"pgx rows never closed": func(t *testing.T, inst *pgtest.Instance) {
		_, err := inst.Pool.Query(context.Background(), "SELECT 1")
		require.NoError(t, err, "the query runs")
	},
	"database/sql rows never closed": func(t *testing.T, inst *pgtest.Instance) {
		_, err := inst.DB.QueryContext(context.Background(), "SELECT 1")
		require.NoError(t, err, "the query runs")
	},
	"a transaction never ended": func(t *testing.T, inst *pgtest.Instance) {
		_, err := inst.Pool.Begin(context.Background())
		require.NoError(t, err, "the transaction begins")
	},
}

func TestIntegrationStopReportsAConnectionStillInUse(t *testing.T) {
	server := pgtest.Run(t)
	for name, leak := range leaks {
		t.Run(name, func(t *testing.T) {
			inst, err := pgtest.Attach(context.Background(), server.URI())
			require.NoError(t, err, "a second instance attaches to the server")
			leak(t, inst)

			// A TestMain stops with a context that never ends, so Stop must
			// not wait on the held connection.
			stopped := make(chan error, 1)
			go func() { stopped <- inst.Stop(context.Background()) }()
			select {
			case err := <-stopped:
				assert.ErrorIs(t, err, pgtest.ErrConnectionsHeld, "a connection still held is reported, so the leak can be found")
			case <-time.After(10 * time.Second):
				require.FailNow(t, "Stop waits for a connection the test will never release")
			}
			assert.NoError(t, inst.Stop(context.Background()), "a second Stop returns at once, without the error again")
		})
	}
}

func TestIntegrationStopReportsAHeldConnectionAndStillRemovesTheContainer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	inst, err := pgtest.Start(ctx)
	require.NoError(t, err, "a container starts")
	t.Cleanup(func() { _ = inst.Stop(context.Background()) })
	_, err = inst.Pool.Query(ctx, "SELECT 1")
	require.NoError(t, err, "the query runs")

	err = inst.Stop(ctx)

	assert.ErrorIs(t, err, pgtest.ErrConnectionsHeld, "the held connection is reported")
	assert.False(t, containerExists(t, inst.Container.ID()), "and the container is removed all the same")
}

func TestIntegrationRunFailsTheTestThatHoldsAConnection(t *testing.T) {
	test := &recorder{TB: t}
	inst := pgtest.Run(test)
	_, err := inst.Pool.Query(context.Background(), "SELECT 1")
	require.NoError(t, err, "the query runs")

	test.end()

	assert.True(t, test.Failed(), "a connection held past the test's end fails the test")
	assert.False(t, containerExists(t, inst.Container.ID()), "and the container is removed all the same")
}

func TestIntegrationStopCanBeCalledAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	inst, err := pgtest.Start(ctx)
	require.NoError(t, err, "a container starts")
	t.Cleanup(func() { _ = inst.Stop(context.Background()) })

	require.NoError(t, inst.Stop(ctx), "stopping succeeds")

	assert.False(t, containerExists(t, inst.Container.ID()), "and removes the container")
	assert.NoError(t, inst.Stop(ctx), "a second call is harmless, as from a defer and a cleanup")
}

func TestAttachRefusesAnEmptyURI(t *testing.T) {
	// pgx would fill an empty string in from PG* variables and defaults, and
	// connect to whatever server those name.
	t.Setenv("PGHOST", "127.0.0.1")

	_, err := pgtest.Attach(context.Background(), "")

	assert.ErrorIs(t, err, pgtest.ErrEmptyURI, "an unset DATABASE_URL is reported, not filled in from the environment")
}

func TestAttachToNothingIsNotConnected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)

	_, err := pgtest.Attach(ctx, "postgres://someone:hunter2@127.0.0.1:1/app?connect_timeout=2")

	require.ErrorIs(t, err, pgtest.ErrNotConnected, "a server that cannot be reached is reported at once, not on the first query")
	assert.NotContains(t, err.Error(), "hunter2", "and the error never holds the password, so it is safe to log")
}

func TestAttachToAMalformedURIIsNotConnected(t *testing.T) {
	_, err := pgtest.Attach(context.Background(), "postgres://someone:hunter2@127.0.0.1:notaport/app")

	require.ErrorIs(t, err, pgtest.ErrNotConnected, "a URI pgx cannot read is reported with the same sentinel")
	assert.NotContains(t, err.Error(), "hunter2", "and the error never holds the password, so it is safe to log")
}

func TestAStartWithNoDaemonIsAConnectionFailure(t *testing.T) {
	nowhere, err := dockerapi.New(dockerapi.WithHost("unix:///nonexistent/docker.sock"))
	require.NoError(t, err, "a client for a daemon that is not there can be made")

	inst, err := pgtest.StartWithOptions(context.Background(), pgtest.DefaultOptions().WithDocker(nowhere))
	t.Cleanup(func() { _ = inst.Stop(context.Background()) })

	assert.ErrorIs(t, err, dockerclient.ErrConnectionFailed, "callers can detect an unreachable daemon with errors.Is")
}

func TestRunFailsAndDoesNotSkipWhenTheStartFails(t *testing.T) {
	nowhere, err := dockerapi.New(dockerapi.WithHost("unix:///nonexistent/docker.sock"))
	require.NoError(t, err, "a client for a daemon that is not there can be made")
	test := &recorder{TB: t}
	returned := false

	inGoroutine(func() {
		pgtest.RunWithOptions(test, pgtest.DefaultOptions().WithDocker(nowhere))
		returned = true
	})
	test.end()

	assert.True(t, test.Failed(), "a start that fails fails the test")
	assert.False(t, test.Skipped(), "and never skips it, so a broken setup cannot pass unnoticed")
	assert.False(t, returned, "and ends it, so no caller is handed a nil instance")
}

func TestStoppingANilInstanceDoesNothing(t *testing.T) {
	var inst *pgtest.Instance

	assert.NoError(t, inst.Stop(context.Background()), "a failed start's nil instance can be stopped from a defer")
}
