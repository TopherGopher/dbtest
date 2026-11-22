---
title: Conformance suite
description: The shared test and benchmark suites that every dockerclient implementation runs.
status: current
---

# Conformance suite

`github.com/tophergopher/dbtest/dockerclient/dockerclienttest` holds one
test suite and one benchmark suite for implementations of
`dockerclient.Client`. The two real clients, `dockerapi` and `mobyclient`,
and the fake `dockermock.Fake` all run them, so the three are held to the
same behaviour and measured on the same scale. A new implementation of
`dockerclient.Client` is finished when it passes
`dockerclienttest.Conformance`.

## Running it against your implementation

The suite takes a factory that returns a fresh client for each case. The
client should be pointed at a fake daemon such as `dockermock.Daemon`, so the
suite runs anywhere with no daemon installed. `dockerapi` and `mobyclient`
each pass a real client that talks to a `dockermock.Daemon`. The daemon's
routes are backed by a `Fake`, so requests go over HTTP and change real
state. Below is `dockerapi`'s conformance test, which runs both
`Conformance` and `Benchmarks`. For your client, change the package and
import, and call your constructor where this calls `dockerapi.New`:

```go
--8<-- "dockerapi/conformance_test.go"
```

A client that is itself a fake can be passed straight in;
`dockermock.Fake` runs the suite that way.

## What is checked

The suite is eight groups of subtests. The image it works with is
`dockerclienttest.TestImage` (`mongo:8`), which the fake daemon already has,
so nothing is downloaded.

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
API version negotiation, is tested in each implementation's own package against
`dockermock.Daemon`, since a fake client such as `Fake` has no wire.

## What is benchmarked

The benchmark suite measures what each call costs: building the request,
encoding the body, decoding the reply. The daemon is a double in the same
process, reached over HTTP on a Unix socket, so no real Docker work enters
the numbers. The double's own time and allocations do count, and they are
the same for every client. Cases cover every method plus the hot paths
inside them (tar writing, stream demultiplexing, error construction). Run
them with:

```sh
go test ./dockerapi/ -run='^$' -bench=. -benchmem
cd mobyclient && go test -run='^$' -bench=. -benchmem  # mobyclient is its own module
```

To run it against your client, call `dockerclienttest.Benchmarks` from a
benchmark, as `BenchmarkClient` above does. To compare two implementations,
run the suite for each on the same machine, and compare the two outputs
with [benchstat](https://pkg.go.dev/golang.org/x/perf/cmd/benchstat).

Comparing `dockerapi` with `mobyclient` shows what each client costs its
caller. The two do not do identical work. The moby client sends every
request through an OpenTelemetry transport. It records nothing unless a
tracer is set up, but it still runs, and costs something, on every request.
Before a pull, it rewrites a short reference such as `mongo:8` into its full
form. `mobyclient` also converts each call's arguments and results between
the moby types and the `dockerclient` ones.
