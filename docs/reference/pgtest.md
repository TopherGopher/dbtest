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

`Run` starts the container with the [postgres package](postgres.md), its
defaults included: `docker.io/library/postgres:18` on a port the daemon
picks. Any failure fails the test, an unreachable Docker daemon included.
The test is never skipped.

Use `Pool` for pgx's own API:

```go
func TestSum(t *testing.T) {
	--8<-- "pgtest/guides_test.go:pool"
}
```

`RunWithOptions` takes the same [options](postgres.md#options) as the
postgres package. Start from `DefaultOptions()` and chain the ones you need,
such as `pgtest.DefaultOptions().WithImage("17")`.

## What an instance gives you

| Field or method | Use |
| --- | --- |
| `inst.Pool` | pgx's `*pgxpool.Pool`. |
| `inst.DB` | A `*sql.DB` that uses pgx's `database/sql` driver, not lib/pq. To test with lib/pq, open your own with `sql.Open("postgres", inst.URI())`. |
| `inst.Container` | The [`*postgres.Container`](postgres.md#the-container). It is nil for an instance from `Attach`. |
| `inst.DatabaseFor(t)` | A new database that only this test uses, dropped when the test ends. Use it when tests share a server. |
| `inst.URI()` | The connection string. It includes the password, so keep it out of logs. |
| `inst.Stop(ctx)` | Closes both clients, then removes the container. Safe to call more than once. `Run` calls it for you. |

`DB` and `Pool` share one pool of connections, capped at pgxpool's default:
4, or the number of CPUs if that is more. For more, open a pool of your own
from `inst.URI()` with `pool_max_conns` added, such as
`pgxpool.New(ctx, inst.URI()+"&pool_max_conns=20")`.

## A connection still in use

When the test ends, or `Stop` is called, every connection should have come
back to the pool. If one has not, the test fails with an error that matches
`ErrConnectionsHeld` and gives the count. The usual causes are a `Rows`
never closed, a `QueryRow` never scanned, and a `Tx` never committed or
rolled back. The container is removed all the same, and `Stop` does not wait
for the connection. `mongotest`'s `Stop` differs here: it does not check.

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

`DatabaseFor` runs `CREATE DATABASE`, named after the test with a random
ending, and connects a new `Pool` and `DB` to it. `Name()` returns the name.
When the test ends it closes them, checking for connections still in use as
above, and drops the database. Creating a database is much quicker than
starting a container.

## A server you already run

`Attach(ctx, uri)` connects to a server that is already running, such as a
CI service container, and starts nothing. Its instance has no container, and
`Stop` only closes the clients.

```go
--8<-- "pgtest/example_test.go:attach"
```

An empty URI is refused with `ErrEmptyURI`. pgx would otherwise fill it in
from the `PG*` environment variables, so an unset `DATABASE_URL` would point
the tests at whatever server those name. For `DatabaseFor`, the role in the
URI needs the `CREATEDB` privilege.

## Errors

| Identifier | Meaning |
| --- | --- |
| `ErrNotConnected` | A client could not connect to the server. |
| `ErrEmptyURI` | `Attach` was given an empty connection string. |
| `ErrConnectionsHeld` | Connections were still in use when the clients were closed. |

`Run` and `Start` also return the [postgres package's errors](postgres.md#when-a-start-returns),
such as `postgres.ErrNotReady`, and the Docker client's, such as
`dockerclient.ErrConnectionFailed`. Match them all with `errors.Is`.
