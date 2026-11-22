---
title: The postgres package
description: "Start a PostgreSQL container and get its URI, with no driver imported."
status: current
---

# The postgres package

`github.com/tophergopher/dbtest/postgres` starts a PostgreSQL container,
waits until the server accepts logins, tells you where to connect, and
removes the container if the process is interrupted. It is in the root
module and imports no driver, so it adds no dependency to yours. For a test
that wants a client already connected, use [pgtest](pgtest.md) instead.

```go
--8<-- "postgres/example_test.go:start"
```

`Start` runs `docker.io/library/postgres:18`, publishes the server on a host
port the daemon picks, and waits up to a minute. It labels the container
`mongotest=regression`. Every dbtest container carries this label,
PostgreSQL's included, so one command finds strays (see
[Cleaning up](#cleaning-up)). Each start gets a fresh,
empty database.

If your tests themselves run in a container on the same Docker daemon, the
server joins that container's network instead of taking a host port.
`URI()` is correct either way.

## Connecting

Hand `URI()` to any driver. With pgx:

```go
--8<-- "pgtest/guides_test.go:drivers"
```

lib/pq takes it too, with `sql.Open("postgres", c.URI())`.

The URI is `postgres://postgres:<password>@<host>:<port>/postgres?sslmode=disable`.
Because it sets every part, the `PGHOST`, `PGPASSWORD`, `PGSSLMODE` and
other variables that pgx and lib/pq read cannot point a test at another
server. `sslmode=disable` is there because the image serves no TLS, so the
connection is not encrypted: with a remote daemon, data crosses the network
in clear.

## Options

`StartWithOptions` takes an `Options` value. Begin with `DefaultOptions` and
chain the `With` methods. Each returns a copy, so a base can be shared
between tests.

| Method on `Options` | Effect |
| --- | --- |
| `WithImage(ref)` | The image; see [Which image](#which-image). |
| `WithPort(port)` | A fixed host port. Prefer the default: another process can take a port you found free before the container binds it. |
| `WithHostIP(host)` | The address the tests reach containers at, for a remote or nested daemon; see [Remote and nested daemons](../guides/remote-daemons.md). |
| `WithStartTimeout(d)` | How long a start waits. The default is one minute, and zero means the default. |
| `WithPullTimeout(d)` | How long a pull of a missing image may take. The default is ten minutes, and it does not count against the start timeout. |
| `WithName(name)`, `WithLabel(key, value)` | The container's name, and labels besides `mongotest=regression`. |
| `WithLogger(l)` | Logs the steps of a start. `*slog.Logger` works. By default nothing is logged, so a first pull, which can take minutes, is silent. |
| `WithDocker(client)` | A specific Docker client, such as a [double](../docker-clients/dockermock.md). |

Three settings can come from the environment, so CI can set them for a whole
suite. An option beats the variable, and the variable beats the default.

| Variable | Sets |
| --- | --- |
| `DBTEST_POSTGRES_IMAGE` | The image, in any shape `WithImage` accepts. |
| `DBTEST_POSTGRES_START_TIMEOUT` | The start timeout, as a Go duration such as `90s`. |
| `DBTEST_HOST_IP` | The address the tests reach containers at, for a remote or nested daemon. |

## Which image

`WithImage` takes two shapes:

- A lone version, one that starts with a digit, means the official image at
  that tag: `"17"` runs `docker.io/library/postgres:17`.
- Anything else is used as written, as Docker reads it, so a name with no
  tag means `latest`: `"postgres:17-alpine"`,
  `"pgvector/pgvector:pg18"`, `"postgis/postgis:18-3.6"`.

A second `WithImage` replaces the first. Only images built on the official
one work, such as PostGIS and pgvector, because the start sets the password
through the official image's `POSTGRES_PASSWORD` variable. Other images,
such as Bitnami's, exit at start.

A start pulls an image the daemon does not have. When Docker Hub cannot
serve an official image, because of its rate limit or an outage, the pull
tries Amazon ECR Public's copy, and `Image()` returns the ECR name.

Call `PullImage` in `TestMain`, so the download happens once, before any
test starts. Call `ReapRunningContainers` after `m.Run`; see
[Cleaning up](#cleaning-up).

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
| `Username()`, `Password()`, `Database()` | The user `postgres`, a password generated for this container, and the database `postgres`. `URI()` already includes them. |
| `ID()`, `Name()`, `Image()` | What was started. |
| `Stop(ctx)` | Removes the container and its data. Safe to call twice. |

Printing a `Container` hides the password.

## When a start returns

A start returns once the server at the URI's address has finished starting
up, so your first query does not race the image's setup. A start that fails removes the container it created. The
error says which step failed, and matches a sentinel such as `ErrNotReady`
or `ErrContainerExited` with `errors.Is`. When the container exits, the
error ends with the server's last log lines.

## Cleaning up

`Stop` removes the container, even when the context it is given has already
ended. Containers that are never stopped are removed when the process gets
`SIGINT` or `SIGTERM`. Three functions act on every container dbtest starts
in the process, MongoDB's and PostgreSQL's alike:

- `ReapRunningContainers` removes those still running: call it after
  `m.Run`.
- `DisableSignalHandler` stops dbtest from removing containers on `SIGINT`
  and `SIGTERM`, for a program with its own shutdown.
- `SetMaxConcurrentStarts` limits how many containers start at once.

A container left behind, for example after `SIGKILL`, carries the label, so
one command removes it:

```sh
docker ps -aq --filter label=mongotest=regression | xargs -r docker rm -fv
```

## Not here

- **A connected client.** [pgtest](pgtest.md) returns a `*sql.DB` and a pgx
  pool, already connected, and gives each test a database of its own.
- **Commands and scripts.** There is no `Exec` or `RunScript`: run SQL
  through your driver. To look inside a container while a test is paused,
  run `docker exec -it <name> psql -U postgres`.
