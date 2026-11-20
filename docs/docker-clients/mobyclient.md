---
title: mobyclient
description: A dockerclient.Client backed by the official moby client, in its own module.
status: current
---

# mobyclient

`github.com/tophergopher/mongotest/mobyclient` wraps the official
`github.com/moby/moby/client` so it satisfies
[`dockerclient.Client`](index.md). Use it when your project already depends on
the official client and you want one Docker connection pool, one set of
options and one TLS configuration across your code and mongotest.

It is a **separate Go module**, so its dependency tree only reaches builds
that import it:

```sh
go get github.com/tophergopher/mongotest/mobyclient
```

## Getting a client

```go
c, err := mobyclient.New() // find the daemon as the docker CLI does
```

```go
c, err := mobyclient.New( // or give the address
    mobyclient.WithHost("unix:///var/run/docker.sock"),
    mobyclient.WithLogger(slog.Default()),
)
```

With no `WithHost` or `WithMobyClient`, the client finds the daemon as the
docker CLI does: `DOCKER_HOST`, then the docker context, then the Docker and
Podman sockets (see `dockerclient.ResolveHost` for the list). `dockerapi`
finds the same daemon, except on Windows, where it cannot open Docker's
named pipe and looks for another endpoint. `FromEnv()` is the same as
`New()`; use `New()`.

`DOCKER_TLS_VERIFY` turns on TLS for a `tcp://` daemon. The certificates
come from `DOCKER_CERT_PATH`, or from the Docker config directory when it is
unset. `DOCKER_API_VERSION` pins the API version. Both apply whether the
address comes from `WithHost` or is found.

To use the moby client's own discovery instead, which reads `DOCKER_HOST`
alone with no docker context and no socket search, build the client with
`client.New(client.FromEnv)` and pass it to `WithMobyClient`.

| Option | Effect |
| --- | --- |
| `WithHost(h)` | Daemon address in `DOCKER_HOST` form. Skips discovery. |
| `WithMobyClient(cli)` | Wrap a `*client.Client` you already built, for options this package does not expose (custom `*tls.Config`, extra headers). `WithHost` and `WithLogger` are ignored when this is set, since that client is already configured. |
| `WithLogger(l)` | One `Debug` record per request through the moby client's response hooks. |

## Error mapping

The moby client has its own error types. `mobyclient` maps them onto the
`dockerclient` sentinels so that calling code does not care which
implementation it got:

| moby condition | reported as |
| --- | --- |
| not found | `dockerclient.ErrNotFound` (a `*StatusError`) |
| conflict | `dockerclient.ErrConflict` |
| unauthorized or forbidden | `dockerclient.ErrUnauthorized` |
| connection failure | `dockerclient.ErrConnectionFailed` (a `*ConnectionError`) |
| anything else from the daemon | `dockerclient.ErrDaemonResponse` (a `*ResponseError`) |

Context cancellation and deadline errors pass through untouched.

## Same behaviour, verified

`mobyclient` runs the same [conformance suite](conformance.md) as
`dockerapi`, against the same `dockermock.Daemon`, so the two are
interchangeable in every respect the suite covers: request shape, error
classification, streaming, exec demultiplexing and validation.

## Choosing between dockerapi and mobyclient

| | `dockerapi` | `mobyclient` |
| --- | --- | --- |
| Third-party modules linked | none | the official client and what it needs |
| Features beyond the interface | version negotiation, Docker or Podman detection | everything the official client offers, via `WithMobyClient` |
| Pick it when | you want the smallest footprint, or need to know whether Docker or Podman answered | you already use the official client and want to share its configuration |
