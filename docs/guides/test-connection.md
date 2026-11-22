---
title: Starting a server
description: Run, Start and Attach, and what an instance gives you.
status: current
---

# Starting a server

`Run`, `Start` and `Attach` each return an `*Instance`. It holds a connected
client of the official driver and the container, if it started one.

## In a test

```go
func TestOrders(t *testing.T) {
	--8<-- "mongotest/guides_test.go:run"
}
```

`Run` starts a container, connects, and removes the container when the test
ends. Any failure fails the test, including an unreachable Docker daemon. The
test is never skipped.

`RunWithOptions` takes [options](../reference/mongod.md#options). Start from
`DefaultOptions()` and chain the settings you need:

```go title="Illustration, not compiled"
inst := mongotest.RunWithOptions(t, mongotest.DefaultOptions().WithReplicaSet("rs0"))
```

## Outside a test

`Start` and `StartWithOptions` do the same outside a test, and the caller
stops the instance. In `TestMain`, one server started this way can serve
every test in the package:

```go
--8<-- "mongotest/internal/sharedserver/sharedserver_test.go:test-main"
```

Let `TestMain` return. Calling `os.Exit` would skip the deferred `Stop`.
[A container per package](parallel-tests.md#a-container-per-package) says
more, and shows the tests that go with it.

## A server you already run

`Attach(ctx, uri)` connects to a server that is already running, such as a CI
service container, and starts nothing. Its instance has no container, and
`Stop` only disconnects.

```go
--8<-- "mongotest/example_test.go:attach"
```

!!! warning "Shared server, shared data"
    A server that tests share is not emptied between them. Give each test its
    own database with `DatabaseFor`, which drops it when the test ends:

    ```go
    func TestOrders(t *testing.T) {
    	--8<-- "mongotest/internal/sharedserver/sharedserver_test.go:database-for"
    }
    ```

## What an instance gives you

| Field or method | Use |
| --- | --- |
| The embedded `*mongo.Client` | Every driver method: `inst.Database("app")`, `inst.StartSession()`, … |
| `inst.Container` | The [`*mongod.Container`](../reference/mongod.md#the-container), for scripts, commands and the TLS configuration. It is nil for an instance from `Attach`. |
| `inst.DatabaseFor(t)` | A new database that only this test uses, dropped when the test ends. Use it when tests share a server. |
| `inst.URI()` | The connection string. It includes any password, so keep it out of logs. |
| `inst.Stop(ctx)` | Disconnects, then removes the container. Safe to call more than once, even after a failed `Stop`. |

`inst.Disconnect`, inherited from the client, closes the connection but leaves
the container running. Call `Stop` to remove it.

## Passing the server to code under test

Most application code takes a URI, a `*mongo.Client` or a `*mongo.Database`.

=== "URI"

    ```go title="Illustration, not compiled"
    repo, err := NewRepository(inst.URI())
    ```

    With `WithTLS`, the URI alone does not connect: the code also needs
    `inst.Container.TLSConfig()`, which trusts the container's certificate.
    Code that builds its own client must disconnect it before the container
    is removed; see [Order of cleanup](#order-of-cleanup).

=== "*mongo.Client"

    ```go title="Illustration, not compiled"
    repo := NewRepository(inst.Client)
    ```

    The instance owns the client; do not call `Disconnect` on it yourself.

=== "*mongo.Database"

    ```go title="Illustration, not compiled"
    svc := NewService(inst.Database("app"))
    ```

## Order of cleanup

`t.Cleanup` functions run last-registered first. `Run` registers the
container's removal as it starts. Cleanups the test registers later run
before it, so a client the test creates is disconnected while the container
still exists:

```go
func TestOwnClient(t *testing.T) {
	--8<-- "mongotest/guides_test.go:own-client"
}
```

## Errors

A failed start returns no instance, and leaves no container behind. Check the
error with `errors.Is` against these values:

| Error | Meaning |
| --- | --- |
| `mongotest.ErrNotConnected` | The client could not connect to the server. |
| `dockerclient.ErrConnectionFailed` (`github.com/tophergopher/dbtest/dockerclient`) | No Docker daemon answered. See [Installation](../getting-started/installation.md#check-the-daemon). |
| `mongod.ErrNotReady`, `mongod.ErrContainerExited`, … | The container did not become a usable `mongod`; see the [mongod reference](../reference/mongod.md). |
