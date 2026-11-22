// Package pgtest gives a test a PostgreSQL server in a Docker container, and
// two clients already connected to it: a pgx pool and a *sql.DB.
//
//	func TestOrders(t *testing.T) {
//		db := pgtest.Run(t).DB
//		...
//	}
//
// Run starts the container, connects, and removes the container when the
// test ends. To start a server without a driver, use
// [github.com/tophergopher/dbtest/postgres].
//
// To share one server across a package's tests, call [Start] in TestMain,
// and [Instance.DatabaseFor] in each test for a database of its own.
package pgtest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/tophergopher/dbtest/postgres"
)

// Options configures a start. Its With methods return a changed copy, as in
// pgtest.DefaultOptions().WithImage("17"). The errors a start returns are in
// the postgres package.
type Options = postgres.Options

// DefaultOptions returns the options Start uses.
func DefaultOptions() Options { return postgres.DefaultOptions() }

// Instance is a PostgreSQL server and two clients connected to it. Stop
// closes both and removes the container.
type Instance struct {
	// Pool is pgx's connection pool. It is capped at pgxpool's default
	// size, the larger of 4 and the number of CPUs; for more, open a pool
	// of your own from URI with pool_max_conns set.
	Pool *pgxpool.Pool
	// DB is database/sql through pgx's driver, not lib/pq, and draws its
	// connections from Pool. To test with lib/pq, open your own with
	// sql.Open("postgres", inst.URI()).
	DB *sql.DB
	// Container is the container the instance started, or nil for one from
	// Attach.
	Container *postgres.Container

	uri     string
	stopped atomic.Bool
}

// Start starts a PostgreSQL container with the default options and connects
// to it. Call Stop when done. In TestMain, defer Stop and return after m.Run.
// Calling os.Exit would skip the deferred Stop.
func Start(ctx context.Context) (*Instance, error) {
	return StartWithOptions(ctx, DefaultOptions())
}

// StartWithOptions starts a PostgreSQL container and connects to it. If the
// clients cannot connect, the container is removed.
func StartWithOptions(ctx context.Context, opts Options) (*Instance, error) {
	c, err := postgres.StartWithOptions(ctx, opts)
	if err != nil {
		return nil, err
	}
	inst, err := Attach(ctx, c.URI())
	if err != nil {
		return nil, errors.Join(err, c.Stop(ctx))
	}
	inst.Container = c
	return inst, nil
}

// Run starts a PostgreSQL container for a test and connects to it, and
// removes it when the test ends. Any failure, Docker being unreachable
// included, fails the test: it is never skipped. A connection the test still
// holds when it ends fails it too.
func Run(tb testing.TB) *Instance {
	tb.Helper()
	return RunWithOptions(tb, DefaultOptions())
}

// RunWithOptions is Run with options.
func RunWithOptions(tb testing.TB, opts Options) *Instance {
	tb.Helper()
	inst, err := StartWithOptions(tb.Context(), opts)
	if err != nil {
		tb.Fatalf("pgtest: cannot start PostgreSQL: %v", err)
	}
	// The test's context has ended by the time cleanup runs; Stop removes
	// the container all the same.
	tb.Cleanup(func() {
		if err := inst.Stop(context.Background()); err != nil {
			tb.Error(err)
		}
	})
	return inst
}

// Attach connects to a PostgreSQL server that is already running, such as a
// CI service container. Its instance has no container, and Stop only closes
// the clients.
func Attach(ctx context.Context, uri string) (*Instance, error) {
	if strings.TrimSpace(uri) == "" {
		return nil, ErrEmptyURI
	}
	config, err := pgxpool.ParseConfig(uri)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNotConnected, err)
	}
	pool, err := connect(ctx, config)
	if err != nil {
		return nil, err
	}
	return &Instance{Pool: pool, DB: stdlib.OpenDBFromPool(pool), uri: uri}, nil
}

// connect opens a pool and pings the server once, so a URI that leads
// nowhere is reported here rather than at the first query.
func connect(ctx context.Context, config *pgxpool.Config) (*pgxpool.Pool, error) {
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNotConnected, err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("%w: %w. Check that the server is running and reachable at the URI", ErrNotConnected, err)
	}
	return pool, nil
}

// URI returns the connection string the instance connected with. It
// includes the password, so keep it out of logs.
func (inst *Instance) URI() string { return inst.uri }

// Stop closes DB and Pool, then removes the container if the instance started
// one. It can be called again, including after a Stop that failed.
//
// Unlike mongotest's Stop, it does not wait for connections still in use. It
// returns an error that matches [ErrConnectionsHeld], leaves the pool to close
// once they come back, and removes the container all the same.
func (inst *Instance) Stop(ctx context.Context) error {
	if inst == nil {
		return nil
	}
	var err error
	if inst.stopped.CompareAndSwap(false, true) {
		err = closeClients(inst.DB, inst.Pool)
	}
	return errors.Join(err, inst.Container.Stop(ctx))
}
