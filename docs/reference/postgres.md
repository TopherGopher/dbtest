---
title: The postgres package
description: "Start a PostgreSQL container and get its URI, with no driver imported."
status: changing
tracks: ["#137"]
---

# The postgres package

`github.com/tophergopher/dbtest/postgres` starts a PostgreSQL container,
waits until the server accepts logins, tells you where to connect, and
removes the container if the process is interrupted. It is in the root
module and imports no driver, so it adds no dependency to yours.

```go
--8<-- "postgres/example_test.go:start"
```

`Start` runs `docker.io/library/postgres:18`, publishes the server on a host
port the daemon picks, labels the container `mongotest=regression`, and
waits up to a minute. Each start gets a fresh, empty database. When the
tests run in a container on the same daemon, nothing is published: the
server joins the network of the tests' container instead.

## Connecting

Hand `URI()` to any driver:

```go title="Illustration, not compiled"
db, err := sql.Open("pgx", c.URI())   // database/sql, with github.com/jackc/pgx/v5/stdlib imported
pool, err := pgxpool.New(ctx, c.URI()) // pgx's own pool
db, err := sql.Open("postgres", c.URI()) // lib/pq
```

The URI is `postgres://postgres:<password>@<host>:<port>/postgres?sslmode=disable`.
It spells out every part, so `PGHOST`, `PGPASSWORD`, `PGSSLMODE` and the
other variables libpq and pgx read cannot send a test somewhere else.
`sslmode=disable` is there because the image serves no TLS.

## Options

`StartWithOptions` takes an `Options` value. Begin with `DefaultOptions` and
chain the `With` methods. Each returns a copy, so a base can be shared
between tests.

| Method on `Options` | Effect |
| --- | --- |
| `WithImage(ref)` | The image; see [Which image](#which-image). |
| `WithPort(port)` | A fixed host port. Prefer the default: a port found free can be taken before the container binds it. |
| `WithHostIP(host)` | The address containers are reachable at; see [Remote and nested daemons](../guides/remote-daemons.md). |
| `WithStartTimeout(d)` | How long a start waits. The default is one minute. |
| `WithPullTimeout(d)` | How long a pull of a missing image may take. The default is ten minutes, and it does not count against the start timeout. |
| `WithName(name)`, `WithLabel(key, value)` | The container's name, and labels besides `mongotest=regression`. |
| `WithLogger(l)` | Logs the steps of a start. `*slog.Logger` works. By default nothing is logged. |
| `WithDocker(client)` | A specific Docker client, such as a [double](../docker-clients/dockermock.md). |

Three settings can come from the environment, so CI can set them for a whole
suite. An option beats the variable, and the variable beats the default.

| Variable | Sets |
| --- | --- |
| `DBTEST_POSTGRES_IMAGE` | The image, in any shape `WithImage` accepts. |
| `DBTEST_POSTGRES_START_TIMEOUT` | The start timeout, as a Go duration such as `90s`. |
| `DBTEST_HOST_IP` | The address containers are reachable at. |

Unlike `MONGOTEST_IMAGE`, `DBTEST_POSTGRES_IMAGE` is read only when no option
names an image; it never combines with one.

## Which image

`WithImage` takes two shapes:

- A lone version, one that starts with a digit, means the official image at
  that tag: `"17"` runs `docker.io/library/postgres:17`.
- Anything with a name is used exactly as written, with Docker's meaning, so
  a name with no tag means `latest`: `"postgres:17-alpine"`,
  `"pgvector/pgvector:pg18"`, `"postgis/postgis:18-3.6"`.

Nothing carries over from an earlier call. Only images built on the official
one work, such as PostGIS and pgvector, because the start sets the password
the way the official image reads it. Bitnami's image, for one, exits at
start.

A start pulls an image the daemon does not have. When Docker Hub cannot
serve an official image, because of its rate limit or an outage, the pull
tries Amazon ECR Public's copy, and the container runs under that name.
Pull the image in `TestMain`, so the download happens before any test
starts, and remove what the tests left running afterwards:

```go
func TestMain(m *testing.M) {
	--8<-- "postgres/example_test.go:test-main"
}
```

## The container

| Method | Returns |
| --- | --- |
| `URI()` | The connection string, with the password. Keep it out of logs. |
| `Host()`, `Port()` | The address to dial. The host is not always loopback. |
| `Username()`, `Password()`, `Database()` | `postgres`, the password generated for this container, and `postgres`. `URI()` already includes them. |
| `ID()`, `Name()`, `Image()` | What was started. |
| `Stop(ctx)` | Removes the container and its data. Safe to call twice. |

Printing a `Container` hides the password.

## When a start returns

A start returns once the server, at the address in the URI, asks for a
login. The image first runs a temporary server to create the database, but
that one listens on no TCP port, so whatever answers is the final server.
A start that fails removes the container it created. The error says which
step failed, and matches a sentinel such as `ErrNotReady` or
`ErrContainerExited` with `errors.Is`. When the container exits, the error
ends with the server's last log lines.

## Cleaning up

`Stop` removes the container, even when the context it is given has already
ended. Containers that are never stopped are removed when the process gets
`SIGINT` or `SIGTERM`. `ReapRunningContainers`, `DisableSignalHandler` and
`SetMaxConcurrentStarts` act on every container dbtest starts in the
process, MongoDB's included, and match the functions of the same names in
[`mongod`](mongod.md#cleaning-up). Anything that escapes carries the label,
so one command removes it:

```sh
docker rm -fv $(docker ps -aq --filter label=mongotest=regression)
```

## Not here

- **A connected client.** A module that returns a `*sql.DB` and a pgx pool
  comes next, in [#137](https://github.com/TopherGopher/dbtest/issues/137).
- **Commands and scripts.** There is no `Exec` or `RunScript`: run SQL
  through your driver. To look inside a container while a test is paused,
  run `docker exec -it <name> psql -U postgres`.
