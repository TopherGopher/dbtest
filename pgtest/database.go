package pgtest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

// Database is a database that only one test uses, with two clients connected
// to it. DatabaseFor closes them and drops the database when the test ends.
type Database struct {
	// Pool is pgx's connection pool, sized as Instance.Pool is.
	Pool *pgxpool.Pool
	// DB is database/sql through pgx's driver, drawing on Pool.
	DB *sql.DB

	name string
}

// Name returns the database's name.
func (d *Database) Name() string { return d.name }

// DatabaseFor creates a database that only this test uses, and drops it when
// the test ends. It is for tests that share a server, such as one started in
// TestMain or one reached with Attach. Each call returns a different
// database, so call it once per test and pass the database on.
//
// The name is built from the test's name, so it can be recognised in the
// server's logs, and ends in a random part. A connection still in use when
// the test ends, or a drop that fails, fails the test.
func (inst *Instance) DatabaseFor(tb testing.TB) *Database {
	tb.Helper()
	ctx, cancel := context.WithTimeout(tb.Context(), databaseTimeout)
	defer cancel()
	d := &Database{name: databaseName(tb.Name())}
	if _, err := inst.Pool.Exec(ctx, "CREATE DATABASE "+quote(d.name)); err != nil {
		tb.Fatalf("pgtest: cannot create a database for the test: %v%s", err, createAdvice(err))
	}
	tb.Cleanup(func() {
		if err := inst.release(d); err != nil {
			tb.Error(err)
		}
	})
	pool, err := inst.connectTo(ctx, d.name)
	if err != nil {
		tb.Fatal(err)
	}
	d.Pool, d.DB = pool, stdlib.OpenDBFromPool(pool)
	return d
}

// databaseTimeout is how long creating, connecting to or dropping a test's
// database may take. Without it, a server that has gone would hold up every
// test until the go test timeout.
const databaseTimeout = 10 * time.Second

// connectTo connects to another database on the instance's server, with
// everything else as the instance's URI gives it.
func (inst *Instance) connectTo(ctx context.Context, database string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(inst.uri)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNotConnected, err)
	}
	config.ConnConfig.Database = database
	return connect(ctx, config)
}

// release closes a test's clients and drops its database. A stopped instance
// is no error: the database went with its container, or is out of reach, and
// a Stop deferred inside a test function runs before that test's cleanups.
// FORCE ends any session the test left open, so the drop does not wait on it.
func (inst *Instance) release(d *Database) error {
	var err error
	if d.Pool != nil {
		err = closeClients(d.DB, d.Pool)
	}
	if inst.stopped.Load() {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), databaseTimeout)
	defer cancel()
	if _, dropErr := inst.Pool.Exec(ctx, "DROP DATABASE IF EXISTS "+quote(d.name)+" WITH (FORCE)"); dropErr != nil {
		err = errors.Join(err, fmt.Errorf("pgtest: cannot drop the database %s: %w", d.name, dropErr))
	}
	return err
}

// insufficientPrivilege is the SQLSTATE of a statement the role may not run.
const insufficientPrivilege = "42501"

// createAdvice says what to do about a CREATE DATABASE the server refused
// for want of a privilege, which happens with Attach.
func createAdvice(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == insufficientPrivilege {
		return ". The role in the URI needs the CREATEDB privilege: grant it with ALTER ROLE <role> CREATEDB, or attach as a role that has it"
	}
	return ""
}

// quote quotes an identifier for SQL.
func quote(name string) string { return pgx.Identifier{name}.Sanitize() }

// maxDatabaseName is the longest name the server keeps, in bytes. It cuts a
// longer one short, so two tests could end up with the same name.
const maxDatabaseName = 63

// databaseName returns a database name for a test: the test's name in
// lowercase, cut short, then a random ending. The ending keeps apart two
// tests with the same name, names that differ only in characters that are
// replaced, and the same test run in another process.
func databaseName(test string) string {
	random := make([]byte, 4)
	rand.Read(random) // never fails: see crypto/rand.Read
	ending := "_" + hex.EncodeToString(random)
	readable := strings.Map(plain, strings.ToLower(test))
	return readable[:min(len(readable), maxDatabaseName-len(ending))] + ending
}

// plain returns r if it is a lowercase ASCII letter, a digit or _, and _
// otherwise. Such a name reads the same quoted or not, and every character
// is one byte long, so the cut cannot split one.
func plain(r rune) rune {
	switch {
	case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		return r
	}
	return '_'
}
