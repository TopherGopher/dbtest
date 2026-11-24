---
title: The pgtest package
description: "A PostgreSQL server for a test, with a pgx pool and a *sql.DB already connected."
status: current
---

# The pgtest package

`github.com/tophergopher/dbtest/pgtest` starts PostgreSQL in a container
for a test and hands it two clients, already connected: a pgx pool and a
`*sql.DB`. It removes the container when the test ends.

```sh
go get github.com/tophergopher/dbtest/pgtest
```

It is a module of its own, so only its callers download
[pgx](https://github.com/jackc/pgx), the PostgreSQL driver both clients
come from.

## In a test

```go
func TestOrders(t *testing.T) {
	--8<-- "pgtest/guides_test.go:run"
}
```

`Run` starts the container through the [postgres package](postgres.md),
with its defaults: the `docker.io/library/postgres:18` image, on a port the
daemon picks. Any failure fails the test, including an unreachable Docker
daemon. The test is never skipped.

Use `Pool` for pgx's own API:

```go
func TestSum(t *testing.T) {
	--8<-- "pgtest/guides_test.go:pool"
}
```

To use both clients, keep the instance: `inst := pgtest.Run(t)`, then
`inst.DB` and `inst.Pool`. Each call to `Run` starts another container.

`RunWithOptions` takes the same [options](postgres.md#options) as the
postgres package. Start from `DefaultOptions()` and chain the ones you need,
such as `pgtest.DefaultOptions().WithImage("17")`.

## What an instance gives you

`inst` is the `*Instance` that `Run`, `Start` or `Attach` returns.

| Field or method | Use |
| --- | --- |
| `inst.Pool` | pgx's `*pgxpool.Pool`. |
| `inst.DB` | A `*sql.DB` that uses pgx's `database/sql` driver, not lib/pq. To test with lib/pq, open your own with `sql.Open("postgres", inst.URI())`. |
| `inst.Container` | The [`*postgres.Container`](postgres.md#the-container). It is nil for an instance from `Attach`. |
| `inst.DatabaseFor(t)` | A `*Database`, with its own `Pool` and `DB`, that only this test uses. It is dropped when the test ends. |
| `inst.URI()` | The connection string. It includes the password, so keep it out of logs. |
| `inst.Stop(ctx)` | Closes both clients, then removes the container. Safe to call more than once. `Run` calls it for you. |

`DB` and `Pool` reach the server's default database, which every test on
the instance shares. When tests share an instance, give each its own
database with `DatabaseFor`.

`DB` and `Pool` also share one pool of connections, capped at pgxpool's
default: 4, or the number of CPUs if that is more. For more, parse
`inst.URI()` with `pgxpool.ParseConfig`, set `MaxConns`, and open your own
pool with `pgxpool.NewWithConfig`.

## A connection still in use

When the clients close, every connection should have come back to the
pool. If one has not, `Stop` returns an error that matches
`ErrConnectionsHeld` and gives the count. Under `Run` or `DatabaseFor`, the
test fails with it. The usual causes are a `Rows` never closed, a
`QueryRow` never scanned, a `Tx` never committed or rolled back, and a
`Conn` never released. `Stop` does not wait for the connection, and removes
the container all the same.

## Outside a test

`Start` and `StartWithOptions` start and connect in the same way without a
test. You call `Stop` when you are done. In `TestMain`, one server started
this way can serve every test in the package:

```go
--8<-- "pgtest/internal/sharedserver/sharedserver_test.go:test-main"
```

Let `TestMain` return. Calling `os.Exit` would skip the deferred `Stop`.

Tests that share a server should each take a database of their own with
`DatabaseFor`:

```go
func TestOrders(t *testing.T) {
	--8<-- "pgtest/internal/sharedserver/sharedserver_test.go:database-for"
}
```

`DatabaseFor` runs `CREATE DATABASE` and returns a `*Database` whose `Pool`
and `DB` are connected to the new database. The database is named after the
test, with a random ending, and its `Name()` returns the name. Each call
creates another database, so call it once per test and keep what it
returns. When the test ends, `DatabaseFor` closes the clients, checking for
connections still in use as above, and drops the database.

## A server you already run

`Attach(ctx, uri)` connects to a server that is already running, such as a
CI service container, and starts nothing. Its instance has no container, and
`Stop` only closes the clients.

```go
--8<-- "pgtest/example_test.go:attach"
```

An empty URI, or one pgx cannot read, is refused with `ErrInvalidURI`. pgx
would otherwise fill an empty one in from the `PG*` environment variables,
so an unset `DATABASE_URL` would point the tests at whatever server those
name. pgx still fills in anything a URI leaves out, such as the password,
from those variables, `~/.pgpass` and `pg_service.conf`, as libpq does.

For `DatabaseFor`, the role in the URI needs the `CREATEDB` privilege, and
the server must be PostgreSQL 13 or later.

## Errors

| Identifier | Meaning |
| --- | --- |
| `ErrNotConnected` | A client could not connect to the server. |
| `ErrInvalidURI` | `Attach` was given a URI that is empty or that pgx cannot read. |
| `ErrConnectionsHeld` | Connections were still in use when the clients were closed. |

`Start` and `StartWithOptions` also return the
[postgres package's errors](postgres.md#when-a-start-returns), such as
`postgres.ErrNotReady`, and the Docker client's, such as
`dockerclient.ErrConnectionFailed`. Under `Run` they fail the test. Match
them all with `errors.Is`.
