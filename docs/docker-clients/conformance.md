---
title: Conformance suite
description: The shared test and benchmark suites that every dockerclient implementation runs.
status: current
---

# Conformance suite

`github.com/tophergopher/dbtest/dockerclient/dockerclienttest` holds one
test suite and one benchmark suite. `dockerapi`, `mobyclient` and
`dockermock.Fake` all run them, so the three are held to the same behaviour
and measured on the same scale. A new implementation of the interface is
finished when it passes `Conformance`.

## Running it against your implementation

The suite takes a factory that returns a fresh client for each case. The
client should be pointed at a double, so the suite runs anywhere with no
daemon installed. The pattern `dockerapi` and `mobyclient` use is a real
client talking to a `dockermock.Daemon` whose routes are backed by a `Fake`,
so the whole HTTP path is exercised against state that actually changes.
This is `dockerapi`'s own conformance test. For your client, call its
constructor where this calls `dockerapi.New`:

```go
--8<-- "dockerapi/conformance_test.go"
```

A `dockerclient.Client` that is itself a double can be passed straight in;
`dockermock.Fake` runs the suite that way.

## What is checked

The suite is eight groups of subtests, each assertion carrying a message that
says what the expectation protects. The image it works with is
`dockerclienttest.TestImage` (`mongo:8`); nothing is downloaded.

| Group | What it proves |
| --- | --- |
| **Rejects bad input before reaching the daemon** | Every method returns `ErrInvalidArgument` for empty or malformed IDs, bad image references, relative destination directories, invalid file modes and port bindings without a protocol, without sending a request. |
| **Reports a missing object as `ErrNotFound`** | Inspecting, starting, removing or exec-ing against an unknown container, and creating from an image that is not local, all classify as `ErrNotFound`. |
| **Runs a container lifecycle** | Pull, create, start, inspect (running, with a published host port), top, remove, and remove-again-is-not-found, in order. |
| **Copies files into a container** | `CopyToContainer` and `CopyArchiveToContainer` place the given files with their modes, including into a container that is created but not yet started. |
| **Runs a command and reports its output** | `Exec` returns stdout and stderr separately and a non-zero exit code as data; `ExecCreate`, `ExecStartTo` and `ExecInspect` compose to the same result. |
| **Reports the user an image declares** | `ImageInspect` passes on the user the image runs as. |
| **Reports the health check of a running container** | `ContainerInspect` passes on the health a running container reports. |
| **Reads what a container wrote** | `ContainerLogsTo` returns what the container wrote to stdout and to stderr, each to its own writer. |

Behaviour that depends on the wire, such as the exact request shape, pull
progress parsing, exec frame demultiplexing across split reads and the
negotiation window, is tested in each implementation's own package against
`dockermock.Daemon`, since a double like `Fake` has no wire.

## What is benchmarked

The benchmark suite measures the client's own cost: building the request,
encoding the body, decoding the reply. The daemon is in-process, so daemon and
network time never enter the numbers. Cases cover every method plus the
hot paths inside them (tar writing, stream demultiplexing, error
construction). Run them with:

```sh
go test ./dockerapi/ -run='^$' -bench=. -benchmem
cd mobyclient && go test -run='^$' -bench=. -benchmem
```

Comparing two implementations is a matter of running the same suite with the
same daemon, which is how the
[dockerapi against mobyclient numbers](mobyclient.md#choosing-between-dockerapi-and-mobyclient)
were produced.
