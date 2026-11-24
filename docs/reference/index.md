---
title: Reference
description: Where to find the generated API documentation, and a map of the exported surface.
status: current
---

# Reference

pkg.go.dev has the full API, generated from the source. This page lists
what exists and links to where each part is explained.

<div class="grid cards" markdown>

-   :fontawesome-brands-golang:{ .lg .middle } **[mongotest](https://pkg.go.dev/github.com/tophergopher/dbtest/mongotest)** · **[mongotestv2](https://pkg.go.dev/github.com/tophergopher/dbtest/mongotestv2)**

    ---

    `Run`, `Start`, `Attach` and `Instance`, for driver v1 and driver v2.

-   :fontawesome-brands-golang:{ .lg .middle } **[pgtest](https://pkg.go.dev/github.com/tophergopher/dbtest/pgtest)**

    ---

    `Run`, `Start`, `Attach` and `Instance`, with a pgx pool and a
    `*sql.DB`; see [The pgtest package](pgtest.md).

-   :material-database-export:{ .lg .middle } **[exporter on pkg.go.dev](https://pkg.go.dev/github.com/tophergopher/dbtest/mongotest/exporter)**

    ---

    Writes a collection out as JSON or CSV; see
    [Exporting data](../guides/exporting-data.md).

-   :material-code-braces:{ .lg .middle } **[dockerclient](https://pkg.go.dev/github.com/tophergopher/dbtest/dockerclient)** · **[dockerapi](https://pkg.go.dev/github.com/tophergopher/dbtest/dockerapi)** · **[dockermock](https://pkg.go.dev/github.com/tophergopher/dbtest/dockermock)** · **[mobyclient](https://pkg.go.dev/github.com/tophergopher/dbtest/mobyclient)**

    ---

    The Docker client interface, its implementations and the test doubles.

</div>

## mongotest and mongotestv2 surface

The two packages export the same names. Only the driver behind them differs.

### Entry points

| Identifier | Explained in |
| --- | --- |
| `Run(tb)`, `RunWithOptions(tb, opts)` | [Starting a server](../guides/test-connection.md#in-a-test) |
| `Start(ctx)`, `StartWithOptions(ctx, opts)` | [Starting a server](../guides/test-connection.md#outside-a-test) |
| `Attach(ctx, uri)` | [Starting a server](../guides/test-connection.md#a-server-you-already-run) |
| `Options`, an alias of `mongod.Options`; `DefaultOptions()` | [The mongod package](mongod.md#options) |

### `Instance`

| Field or method | Explained in |
| --- | --- |
| The embedded `*mongo.Client`, `Container`, `DatabaseFor(t)`, `URI()`, `Stop(ctx)` | [Starting a server](../guides/test-connection.md#what-an-instance-gives-you) |

### Errors

| Identifier | Meaning |
| --- | --- |
| `ErrNotConnected` | The client could not connect to the server. |

## Docker client surface

| Package | Key identifiers | Explained in |
| --- | --- | --- |
| `dockerclient` | `Client`, `ContainerConfig`, `HostConfig`, `File`, `ExecResult`, `Logger`, `NopLogger`, the `Err*` sentinels | [Docker clients](../docker-clients/index.md), [Errors](../docker-clients/errors.md) |
| `dockerapi` | `New`, `FromEnv`, `With*` options, `Client.Negotiate`, `APIVersion`, `Runtime`, `ServerProduct` | [dockerapi](../docker-clients/dockerapi.md) |
| `mobyclient` | `New`, `FromEnv`, `WithHost`, `WithMobyClient`, `WithLogger` | [mobyclient](../docker-clients/mobyclient.md) |
| `dockermock` | `Mock`, `NewFake`, `NewDaemon`, `Daemon.Handle`, `Daemon.ServeDefaults`, `Daemon.ServeFake` | [dockermock](../docker-clients/dockermock.md) |
| `dockerclienttest` | `Conformance`, `Benchmarks` | [Conformance suite](../docker-clients/conformance.md) |

## Also in this section

- [The mongod package](mongod.md), which starts a container and returns its
  URI without importing a MongoDB driver.
- [The postgres package](postgres.md), which does the same for PostgreSQL.
- [The pgtest package](pgtest.md), which gives a test a PostgreSQL server
  and a pgx pool and `*sql.DB` connected to it.
- [Environment variables](environment.md) that dbtest and its Docker
  clients read.
- [Writing a new page](writing-docs.md) for this site.
