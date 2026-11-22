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
// Its name is the test's name with a random ending. A connection still in use
// when the test ends, or a failed drop, fails the test. With Attach, the role
// in the URI needs the CREATEDB privilege, and the server PostgreSQL 13 or
// later.
func (inst *Instance) DatabaseFor(tb testing.TB) *Database {
	tb.Helper()
	if inst.stopped.Load() {
		tb.Fatal("pgtest: the instance is stopped, so it cannot create a database. Call DatabaseFor before Stop")
	}
	ctx, cancel := context.WithTimeout(tb.Context(), databaseTimeout)
	defer cancel()
	d := &Database{name: databaseName(tb.Name())}
	if _, err := inst.Pool.Exec(ctx, "CREATE DATABASE "+quote(d.name)); err != nil {
		tb.Fatalf("pgtest: cannot create a database for the test: %v%s", err, createAdvice(err))
	}
	tb.Cleanup(func() {
		tb.Helper()
		if err := inst.release(d); err != nil {
			tb.Error(err)
		}
	})
	config := inst.config.Copy()
	config.ConnConfig.Database = d.name
	pool, err := connect(ctx, config)
	if err != nil {
		tb.Fatalf("pgtest: cannot connect to the test's database %s: %v", d.name, err)
	}
	d.Pool, d.DB = pool, stdlib.OpenDBFromPool(pool)
	return d
}

// databaseTimeout is how long creating, connecting to or dropping a test's
// database may take. Without it, a server that has gone would hold up every
// test until the go test timeout.
const databaseTimeout = 10 * time.Second

// release closes a test's clients and drops its database.
func (inst *Instance) release(d *Database) error {
	var err error
	if d.Pool != nil {
		err = closeClients(d.DB, d.Pool)
	}
	return errors.Join(err, inst.drop(d.name))
}

// drop drops a test's database. FORCE ends any session the test left open,
// so the drop does not wait on it. Once the instance is stopped its pool is
// closed: a container's database went with it, and an attached server's is
// dropped over a connection of its own. That is the usual order, because a
// Stop deferred in a test runs before the test's cleanups.
func (inst *Instance) drop(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), databaseTimeout)
	defer cancel()
	statement := "DROP DATABASE IF EXISTS " + quote(name) + " WITH (FORCE)"
	var err error
	switch {
	case !inst.stopped.Load():
		_, err = inst.Pool.Exec(ctx, statement)
	case inst.Container != nil:
		return nil
	default:
		err = inst.execOnce(ctx, statement)
	}
	if err != nil {
		return fmt.Errorf("pgtest: cannot drop the database %s: %w. It is left on the server: drop it with DROP DATABASE %s", name, err, quote(name))
	}
	return nil
}

// execOnce runs a statement over a connection of its own.
func (inst *Instance) execOnce(ctx context.Context, statement string) error {
	conn, err := pgx.ConnectConfig(ctx, inst.config.ConnConfig.Copy())
	if err != nil {
		return err
	}
	_, err = conn.Exec(ctx, statement)
	return errors.Join(err, conn.Close(ctx))
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

// maxDatabaseName is the longest name the server keeps, in bytes. It would
// cut a longer one short, and with it the random ending.
const maxDatabaseName = 63

// databaseName returns a database name for a test: the test's name in
// lowercase, then a random ending. The ending keeps apart two tests with the
// same name, names that differ only in characters that are replaced, and the
// same test run in another process. A name too long to keep whole keeps its
// start and its end, which is where a subtest's own name is.
func databaseName(test string) string {
	random := make([]byte, 4)
	rand.Read(random) // never fails: see crypto/rand.Read
	ending := "_" + hex.EncodeToString(random)
	return shorten(strings.Map(plain, strings.ToLower(test)), maxDatabaseName-len(ending)) + ending
}

// shorten cuts s to at most n bytes by taking out its middle, which it marks
// with _. Every character of s must be one byte long.
func shorten(s string, n int) string {
	if len(s) <= n {
		return s
	}
	head := n / 2
	return s[:head] + "_" + s[len(s)-(n-head-1):]
}

// plain returns r if it is a lowercase ASCII letter or a digit, and _
// otherwise. Every character it keeps is one byte, so the cut cannot split
// one.
func plain(r rune) rune {
	switch {
	case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		return r
	}
	return '_'
}
