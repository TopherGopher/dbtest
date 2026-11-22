---
title: dockerapi
description: A Docker Engine API client built on the Go standard library alone.
status: current
---

# dockerapi

`github.com/tophergopher/dbtest/dockerapi` is the default implementation of
[`dockerclient.Client`](index.md). It has no third-party dependencies, and
implements exactly the endpoints dbtest needs.

## Getting a client

```go
--8<-- "dockerapi/example_test.go:from-env"
```

`FromEnv` locates the daemon the way the `docker` CLI does, in this order:

1. `DOCKER_HOST`, if set.
2. `DOCKER_CONTEXT`, or the `currentContext` recorded in the Docker config
   directory (`DOCKER_CONFIG` or `~/.docker`), resolved through the context's
   `meta.json`. If the context has TLS files, they are used for TLS instead
   of `DOCKER_TLS_VERIFY` and `DOCKER_CERT_PATH`, and the daemon's
   certificate is always verified.
3. With nothing configured, a probe of the well-known sockets, taking the
   first that answers:

    ```
    $XDG_RUNTIME_DIR/docker.sock          rootless Docker
    /var/run/docker.sock                  system Docker
    $XDG_RUNTIME_DIR/podman/podman.sock   rootless Podman
    /run/podman/podman.sock               system Podman
    ```

    `/run/user/$UID` stands in when `XDG_RUNTIME_DIR` is unset. On macOS the
    `podman machine` sockets are searched too.

    On Windows the client cannot open Docker Desktop's named pipe. It tries
    `tcp://localhost:2375`, which Docker Desktop serves only if you turn it
    on, then the socket `podman machine` exposes. The
    [`mobyclient`](mobyclient.md) client can open the pipe.

The probe connects rather than checking that the file exists, so a socket
left behind by a stopped daemon never shadows a running one. When nothing
answers, `FromEnv` still returns a client pointed at the Docker default, and
the failure surfaces on the first call as a `ConnectionError` naming the
socket. Building a client never depends on a daemon being up.

## Options

`New()` with no options is `FromEnv`. Its options set the host and more:

```go title="Illustration, not compiled"
c, err := dockerapi.New(
    dockerapi.WithHost("tcp://build-host:2376"),
    dockerapi.WithTLSConfig(&tls.Config{RootCAs: pool}),
    dockerapi.WithAPIVersion("1.41"),
    dockerapi.WithUserAgent("my-tests/1.0"),
    dockerapi.WithLogger(slog.Default()),
)
```

| Option | Effect |
| --- | --- |
| `WithHost(h)` | `unix:///path`, `tcp://host:port`, `http://` or `https://`. A host without a scheme is refused before anything is dialled. Skips discovery. |
| `WithHTTPClient(hc)` | Supply your own `*http.Client`. Useful for an in-process stub in unit tests. |
| `WithTLSConfig(cfg)` | Trust a daemon's CA explicitly. Without it, the TLS files of the docker context the daemon was found through are used, or else `DOCKER_CERT_PATH` when `DOCKER_TLS_VERIFY` is set. |
| `WithAPIVersion(v)` | Pin the API version instead of negotiating. |
| `WithUserAgent(ua)` | Sent on every request. Defaults to `mongotest-dockerapi/1`. |
| `WithLogger(l)` | One `Debug` record per request. `*slog.Logger` satisfies `dockerclient.Logger`, the type it takes. |

## Version negotiation

Daemons differ in which API versions they accept, so the client negotiates.
`dockerapi` asks for its preferred version (1.44) and accepts anything down to
1.24. The first request to a daemon triggers a `GET /version`; the client then
uses the highest version both sides support. Call `Negotiate` to do it
eagerly, and `APIVersion` to read the result:

```go
--8<-- "dockerapi/example_test.go:negotiate"
```

When the two version ranges do not overlap, `Negotiate` returns an
`*APIVersionError` that names the product, Docker or Podman, and both ranges.

## Which engine answered

Once the client has negotiated, `Runtime` says which engine answered:
`RuntimeDocker`, `RuntimePodman`, or `RuntimeUnknown` for an engine that is
neither. Before then it is `RuntimeUnknown`. Use it where Docker and Podman
need different handling, or to name the right engine in a message.
`ServerProduct` is how the daemon described itself.

Below, `c` is a client pointed at a
[`dockermock.Daemon`](dockermock.md), a fake daemon for tests, that answers
as Podman does:

```go
--8<-- "dockerapi/example_test.go:runtime"
```

Podman is identified by the `Libpod-API-Version` header it sets on every
response, falling back to the component list in `GET /version`.

## Operations

All methods of the [interface](index.md#the-interface) are implemented, plus
`ExecStart`, which buffers the output that `ExecStartTo` streams:

```go
--8<-- "dockerapi/example_test.go:exec-start"
```

Behaviour to know:

- **`ImagePull`** drains the progress stream and surfaces an `error` line
  from the daemon as a `*PullError`, so a failed pull is an error and not a
  silent half-image.
- **`CopyToContainer`** streams the archive instead of building it in
  memory. It works on a container that has been created but not yet started, which is
  how the `mongod` package places its TLS files before the server starts.
- **`Exec`** captures stdout and stderr separately through the multiplexed
  stream, waits for the process to exit, and reports a non-zero exit code in
  `ExecResult.ExitCode` rather than as an error.
- **`ContainerTop`** shows whether a process is running in the container. A
  published port is bound before the process starts, so an open port proves
  nothing.

## Unit testing code that uses dockerapi

Prefer taking a `dockerclient.Client` and using
[`dockermock`](dockermock.md). When you need a `*dockerapi.Client`
specifically, point it at `dockermock.NewDaemon()` or at an in-process
`http.RoundTripper` through `WithHTTPClient`. The package's own examples do
exactly this and run with no daemon installed.
