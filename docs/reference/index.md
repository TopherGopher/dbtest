---
title: Reference
description: Where to find the generated API documentation, and a map of the exported surface.
status: current
---

# Reference

The authoritative API reference is generated from the source by pkg.go.dev.
This section is a map, not a copy: it tells you what exists and where it is
explained, and links out for signatures.

<div class="grid cards" markdown>

-   :fontawesome-brands-golang:{ .lg .middle } **[mongotest on pkg.go.dev](https://pkg.go.dev/github.com/tophergopher/mongotest)**

    ---

    The root package: `Run`, `Start`, `Attach`, `Instance`, and the earlier
    names kept as wrappers.

-   :material-code-braces:{ .lg .middle } **[dockerclient](https://pkg.go.dev/github.com/tophergopher/mongotest/dockerclient)** · **[dockerapi](https://pkg.go.dev/github.com/tophergopher/mongotest/dockerapi)** · **[dockermock](https://pkg.go.dev/github.com/tophergopher/mongotest/dockermock)** · **[mobyclient](https://pkg.go.dev/github.com/tophergopher/mongotest/mobyclient)**

    ---

    The Docker client interface, its implementations and the test doubles.

</div>

## Root package surface

### Entry points

| Identifier | Explained in |
| --- | --- |
| `Run(tb)`, `RunWithOptions(tb, opts)` | [Starting a server](../guides/test-connection.md#in-a-test) |
| `Start(ctx)`, `StartWithOptions(ctx, opts)` | [Starting a server](../guides/test-connection.md#outside-a-test) |
| `Attach(ctx, uri)` | [Starting a server](../guides/test-connection.md#a-server-you-already-run) |
| `Options` (`mongod.Options`), `DefaultOptions()` | [The mongod package](mongod.md#options) |

### `Instance`

| Field or method | Explained in |
| --- | --- |
| The embedded `*mongo.Client`, `Container`, `URI()`, `Stop(ctx)` | [Starting a server](../guides/test-connection.md#what-an-instance-gives-you) |

### Errors

| Identifier | Meaning |
| --- | --- |
| `ErrNotConnected` | The client could not connect to the server. |
| `ErrNoContainer` | An instance from `Attach` was asked to run a script or command. |
| `ErrCommandFailed` | A command or script run with the earlier methods exited non-zero. |

### Earlier names

Code written against the earlier API still compiles. Each name below is a thin
wrapper; new code uses the right-hand column.

| Earlier name | New code uses |
| --- | --- |
| `TestConnection` | `Instance` |
| `NewTestConnection(true)` | `Run` or `Start` |
| `NewTestConnection(false)` | `Attach` |
| `NewReplicaSetContainer(name)` | `DefaultOptions().WithReplicaSet(name)` |
| `MongoClientWithContainer(f)` | `Run(t).Client` |
| `KillMongoContainer()` | `Stop(ctx)` |
| `MongoContainerID()` | `Container.ID()` |
| `RunMongoScriptOnContainer(js)` | `Container.RunScript` |
| `ExecCommandInMongoContainer(cmd)` | `Container.Exec` |
| `GetAvailablePort()` | Leave the port to Docker. |
| `ReapRunningContainers()` | `mongod.ReapRunningContainers`, which returns its error |
| `GenerateCARoot()` | `WithTLS`, which makes a container's certificates itself |
| `ErrFailedToConnectToDockerDaemon` | `dockerclient.ErrConnectionFailed`, the same value |
| `ErrMongoContainerAlreadyRunning` | `dockerclient.ErrConflict`, the same value |
| `ErrNoAvailablePorts` | Returned only by `GetAvailablePort`. |

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
- [Environment variables](environment.md) that mongotest and its Docker
  clients read.
- [Writing a new page](writing-docs.md) for this site.
