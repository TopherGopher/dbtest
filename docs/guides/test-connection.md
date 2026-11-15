---
title: Starting a server
description: Run, Start and Attach, and what an instance gives you.
status: current
---

# Starting a server

`Run`, `Start` and `Attach` each return an `*Instance`. It holds a connected
client of the official driver and the container it started.

## In a test

```go
inst := mongotest.Run(t)
db := inst.Database("app")
```

`Run` starts a container, connects, and removes the container when the test
ends. Any failure fails the test, including an unreachable Docker daemon. The
test is never skipped.

`RunWithOptions` takes [options](../reference/mongod.md#options). Start from
`DefaultOptions()` and chain the settings you need:

```go
inst := mongotest.RunWithOptions(t, mongotest.DefaultOptions().WithReplicaSet("rs0"))
```

## Outside a test

`Start` and `StartWithOptions` do the same where there is no test, such as in
`TestMain`. The caller stops the instance:

```go
inst, err := mongotest.Start(ctx)
if err != nil {
    return err
}
defer inst.Stop(ctx)
```

## A server you already run

`Attach(ctx, uri)` connects to a server that is already running, such as a CI
service container, and starts nothing. Its instance has no container, and
`Stop` only disconnects.

```go
inst, err := mongotest.Attach(ctx, "mongodb://localhost:27017")
```

!!! warning "Shared server, shared data"
    A server you attach to is not emptied between tests. Give each test its
    own database and drop it in cleanup. Use a fresh context there:
    `t.Context()` has already ended when cleanups run.

    ```go
    // Database names cannot hold the "/" a subtest name has.
    db := inst.Database("test_" + strings.ReplaceAll(t.Name(), "/", "_"))
    t.Cleanup(func() { _ = db.Drop(context.Background()) })
    ```

## What an instance gives you

| Field or method | Use |
| --- | --- |
| The embedded `*mongo.Client` | Every driver method: `inst.Database("app")`, `inst.StartSession()`, … |
| `inst.Container` | The [`*mongod.Container`](../reference/mongod.md#the-container), for scripts, commands and the TLS configuration. It is nil for an instance from `Attach`. |
| `inst.URI()` | The connection string. It includes any password, so keep it out of logs. |
| `inst.Stop(ctx)` | Disconnects, then removes the container. Safe to call more than once, even after a failed `Stop`. |

`inst.Disconnect`, inherited from the client, closes the connection but leaves
the container running. Call `Stop` to remove it.

## Passing the server to code under test

Most application code takes a URI, a `*mongo.Client` or a `*mongo.Database`.

=== "URI"

    ```go
    repo, err := NewRepository(inst.URI())
    ```

    With `WithTLS`, the URI alone does not connect: the code also needs
    `inst.Container.TLSConfig()`, which trusts the container's certificate.
    Code that builds its own client must disconnect it before the container
    is removed; see [Order of cleanup](#order-of-cleanup).

=== "*mongo.Client"

    ```go
    repo := NewRepository(inst.Client)
    ```

    The instance owns the client; do not call `Disconnect` on it yourself.

=== "*mongo.Database"

    ```go
    svc := NewService(inst.Database("app"))
    ```

## Order of cleanup

`t.Cleanup` functions run last-registered first. `Run` registers the
container's removal as it starts. Cleanups the test registers later run
before it, so a client the test creates is disconnected while the container
still exists:

```go
inst := mongotest.Run(t)

client, err := mongo.Connect(t.Context(), options.Client().ApplyURI(inst.URI()))
require.NoError(t, err)
t.Cleanup(func() { _ = client.Disconnect(context.Background()) }) // runs first
```

## Errors

A failed start returns no instance, and leaves no container behind. Check the
error with `errors.Is` against these values:

| Error | Meaning |
| --- | --- |
| `mongotest.ErrNotConnected` | The client could not connect to the server. |
| `dockerclient.ErrConnectionFailed` (`github.com/tophergopher/mongotest/dockerclient`) | No Docker daemon answered. See [Installation](../getting-started/installation.md#check-the-daemon). |
| `mongod.ErrNotReady`, `mongod.ErrContainerExited`, … | The container did not become a usable `mongod`; see the [mongod reference](../reference/mongod.md). |
| `mongotest.ErrNoContainer` | Returned by the earlier script and command methods for an instance from `Attach`. |
