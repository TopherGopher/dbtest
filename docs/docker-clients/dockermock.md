---
title: dockermock
description: "Test doubles for the Docker client interface: Mock, Fake and an HTTP-level Daemon."
status: current
---

# dockermock

`github.com/tophergopher/dbtest/dockermock` is the single home for test
doubles of [`dockerclient.Client`](index.md), so a project needs exactly one
of them rather than a stub per package. Three are offered, in increasing order
of how much they do for you.

| Double | Use it when |
| --- | --- |
| `Mock` | **The calls are the subject.** You want to assert which methods ran, in what order, with what arguments. |
| `Fake` | **The calls must work so something else can be tested.** You need a container lifecycle to behave, and the Docker calls are not the point. |
| `Daemon` | **The HTTP conversation is the subject.** You are testing a real client, or code that needs a `*dockerapi.Client` specifically. |

`Mock` and `Fake` satisfy `dockerclient.Client`. `Daemon` sits one level
lower: it is a fake Engine API server that a real client is pointed at.

## Mock: assert the exact calls

`Mock` has one function field per interface method and records every call.
Leave a field nil and the method succeeds with a zero result, so a test
scripts only the calls it cares about.

```go
--8<-- "dockermock/example_test.go:calls-to"
```

| Method | Returns |
| --- | --- |
| `Calls()` | Every recorded call, in order, with method name and arguments. |
| `CallsTo(method)` | The calls to one method. |
| `Methods()` | Just the method names, in order. Handy for asserting a sequence. |
| `Reset()` | Forget the recorded calls. The function fields stay. |

## Fake: a working daemon in memory

`Fake` keeps an in-memory image and container store and behaves like a daemon:
creating a container needs its image pulled first, starting one makes it
running, inspecting it reports the ports it published, removing it makes later
calls return `ErrNotFound`. Nothing is scripted.

```go
--8<-- "dockermock/example_test.go:fake"
```

`Fake` applies the same validation as a real client, so a bad image reference
or port binding fails in the test exactly as it would in production. It also
enforces the rules that trip people up on a real daemon: removing a running
container needs `Force`, `ContainerTop` needs a running container, and
`ContainerCreate` without the image returns `ErrNotFound`.

`Containers()` returns a snapshot of the store for assertions about final
state. `Fake` passes the [conformance suite](conformance.md), so it is a
faithful stand-in within the suite's coverage.

## Daemon: a fake Docker daemon over HTTP

`Daemon` is an `httptest` server that speaks the Engine API. A real client
(`dockerapi` or `mobyclient`) is pointed at it through its `WithHost` option.
It records every request, matches routes with `{id}` placeholders, and ignores
the `/v1.xx` version prefix so tests work before and after negotiation.

```go
--8<-- "dockermock/example_test.go:requests-to"
```

| Method | Purpose |
| --- | --- |
| `NewDaemon(opts...)` | Start listening on a unix socket, or on TCP with `OverTCP()`. |
| `Host()` / `URL()` | The address in `DOCKER_HOST` form, and as an HTTP URL. |
| `Handle(method, pattern, h)` | Register or replace a route. Patterns take `{id}` placeholders; `PathParam(r, "id")` reads them. |
| `ServeDefaults()` | Fixed answers for every endpoint. |
| `ServeVersion(api, min)` / `ServePodmanVersion(api, libpod)` | Script `GET /version` to look like a given Docker or Podman release. |
| `ServeFake(f)` | Back the HTTP routes with a `Fake`, so a real client drives a working in-memory daemon end to end. |
| `Requests()` / `RequestsTo(method, path)` | What arrived, including bodies. |
| `Reset()` | Forget recorded requests. Routes stay. |

Helpers for writing handlers: `JSON(w, status, v)`, `Error(w, status, msg)`
for the daemon's error envelope, and `Frame(stream, payload)` and
`Hijack(w, stream)` for producing multiplexed exec output over a hijacked
connection.

### Scripting an error

```go
--8<-- "dockermock/example_test.go:handle"
```

### Looking like Podman

```go
--8<-- "dockermock/example_test.go:podman"
```
