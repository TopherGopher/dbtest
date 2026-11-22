---
title: mobyclient
description: A dockerclient.Client backed by the official moby client, in its own module.
status: current
---

# mobyclient

`github.com/tophergopher/dbtest/mobyclient` wraps the official
`github.com/moby/moby/client` so it satisfies
[`dockerclient.Client`](index.md). Use it when your project already depends on
the official client and you want one Docker connection pool, one set of
options and one TLS configuration across your code and dbtest.

It is a **separate Go module**, so only projects that import it download
and link the official client:

```sh
go get github.com/tophergopher/dbtest/mobyclient
```

## Getting a client

`mobyclient.New()` finds the daemon as the docker CLI does: `DOCKER_HOST`,
then the docker context, then the Docker and Podman sockets.
`dockerclient.ResolveDaemon` lists them. `FromEnv()` does the same; prefer
`New()`.

To give the address yourself, use `WithHost`:

```go
--8<-- "mobyclient/example_test.go:with-host"
```

`DOCKER_TLS_VERIFY` turns on TLS for a `tcp://` daemon. The certificates
come from `DOCKER_CERT_PATH`, or from the Docker config directory when it is
unset. `DOCKER_API_VERSION` pins the API version. These apply whether the
address comes from `WithHost` or is found.

When the daemon is found through a docker context that has TLS files, those
files are used instead of `DOCKER_TLS_VERIFY` and `DOCKER_CERT_PATH`, as
with the `docker` CLI.
With `WithHost` no context is used. The daemon's certificate is always
verified, even when the context was made with `skip-tls-verify=true`.

To use the moby client's own discovery instead, which reads `DOCKER_HOST`
alone with no docker context and no socket search, build the client with
`client.New(client.FromEnv)` and pass it to `WithMobyClient`.

| Option | Effect |
| --- | --- |
| `WithHost(h)` | Daemon address in `DOCKER_HOST` form. Skips discovery. |
| `WithMobyClient(cli)` | Wrap a `*client.Client` you already built, for options this package does not expose (custom `*tls.Config`, extra headers). `WithHost` and `WithLogger` are ignored when this is set, since that client is already configured. |
| `WithLogger(l)` | One `Debug` record per request, so set the logger to Debug to see them. It takes a `dockerclient.Logger`, such as a `*slog.Logger`; zap, zerolog and logrus work [through their slog handlers](../reference/mongod.md#logging-with-zap-zerolog-or-logrus). By default nothing is logged. Ignored with `WithMobyClient`. |

## Using it with dbtest

`mongod` and `mongotest` take the client through `WithDocker`. For a test,
that is `mongotest.RunWithOptions(t, mongotest.DefaultOptions().WithDocker(docker))`.
With `mongod`:

```go
--8<-- "mobyclient/example_test.go:with-mongod"
```

## Error mapping

The moby client has its own error types. `mobyclient` maps them onto the
`dockerclient` sentinels so that calling code does not care which
implementation it got. The type in parentheses is what `errors.As`
retrieves, for details such as the status code.

| moby condition | reported as |
| --- | --- |
| not found | `dockerclient.ErrNotFound` (`*StatusError`) |
| conflict | `dockerclient.ErrConflict` (`*StatusError`) |
| unauthorized or forbidden | `dockerclient.ErrUnauthorized` (`*StatusError`) |
| a registry's rate limit | `dockerclient.ErrRateLimited` (`*StatusError`) |
| another status, such as 500 or 503 | no sentinel (`*StatusError`) |
| connection failure | `dockerclient.ErrConnectionFailed` (`*ConnectionError`) |
| anything else it cannot classify | `dockerclient.ErrDaemonResponse` (`*ResponseError`) |

Context cancellation and deadline errors pass through untouched.

## Same behaviour, verified

`mobyclient` runs the same [conformance suite](conformance.md) as
`dockerapi`, against the same `dockermock.Daemon`, so the two behave the same
in what the suite checks: input validation, error classification, the
container lifecycle, file copies, exec, image users, health checks and logs.

## Choosing between dockerapi and mobyclient

| | `dockerapi` | `mobyclient` |
| --- | --- | --- |
| Third-party modules linked | none | the official client and what it needs |
| Docker Desktop on Windows | needs its TCP endpoint turned on | opens the named pipe |
| Features beyond the interface | version negotiation, Docker or Podman detection | everything the official client offers, via `WithMobyClient` |
| Pick it when | you want the smallest footprint, or need to know whether Docker or Podman answered | you already use the official client and want to share its configuration |
