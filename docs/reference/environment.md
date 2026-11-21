---
title: Environment variables
description: Every environment variable dbtest and its Docker clients read.
status: current
---

# Environment variables

The `mongod` package reads three variables of its own. The Docker clients
read the same ones the `docker` CLI does, so they find the same daemon; on
Windows, see [Installation](../getting-started/installation.md).

## Read by the Docker clients

| Variable | Read by | Effect |
| --- | --- | --- |
| `DOCKER_HOST` | `dockerapi`, `mobyclient` | Daemon address: `unix:///path`, `tcp://host:port`, `npipe://` (`mobyclient` only). Overrides every other discovery step. |
| `DOCKER_CONTEXT` | `dockerapi`, `mobyclient` | The Docker context to use when `DOCKER_HOST` is unset. |
| `DOCKER_CONFIG` | `dockerapi`, `mobyclient` | Directory holding `config.json` and `contexts/`. Defaults to `~/.docker`. The `currentContext` key in `config.json` is honoured. |

However the context is chosen, if it has TLS files, its daemon is reached
over TLS with them, as with the `docker` CLI, and `DOCKER_TLS_VERIFY` and
`DOCKER_CERT_PATH` are ignored. A context has TLS files when it was made
with, for example, `docker context create --docker "host=…,ca=…,cert=…,key=…"`.
The daemon's certificate is always verified, even when the context was
made with `skip-tls-verify=true`, and a context made that way with no TLS
files is refused rather than sent plain HTTP. A client given an address with
`WithHost` uses no context.

| Variable | Read by | Effect |
| --- | --- | --- |
| `DOCKER_TLS_VERIFY` | `dockerapi`, `mobyclient` | When set, reach a `tcp://` daemon over TLS and verify its certificate. The certificates come from `DOCKER_CERT_PATH`, or from the Docker config directory (`DOCKER_CONFIG`, default `~/.docker`) when it is unset. Ignored when the docker context in use has TLS files. |
| `DOCKER_CERT_PATH` | `dockerapi`, `mobyclient` | Directory with `ca.pem`, and `cert.pem` and `key.pem` if the daemon wants a client certificate. It has no effect without `DOCKER_TLS_VERIFY`. Ignored when the docker context in use has TLS files. |
| `DOCKER_API_VERSION` | `dockerapi`, `mobyclient` | Pin the API version. Without it, both negotiate with the daemon. |
| `XDG_RUNTIME_DIR` | `dockerapi`, `mobyclient` | Where rootless Docker and Podman put their sockets. `/run/user/$UID` is used when unset. |
| `TEMP`, `LOCALAPPDATA` | `dockerapi` on Windows | Where `podman machine` puts its socket on Windows: `TEMP`, or `LOCALAPPDATA\Temp`. Only `dockerapi` reads them, since `mobyclient` opens the named pipe. |

## Read by `mongod`

An option beats the variable, and the variable beats the default. See
[The mongod package](../reference/mongod.md#options). `mongotest.Run` and
`Start` read them too, because they start through `mongod`.

| Variable | Sets | Default |
| --- | --- | --- |
| `MONGOTEST_IMAGE` | The image: a version, a repository, or a full reference such as `localhost:5000/team/mongo:8` | `docker.io/library/mongo:8.0` |
| `MONGOTEST_START_TIMEOUT` | The start timeout, as a Go duration | `60s` |
| `DBTEST_HOST_IP` | The address containers are reachable at, when the detection is wrong | see [Remote and nested daemons](../guides/remote-daemons.md) |

## Not read

dbtest does not read `MONGO_URI`, `MONGODB_URI` or similar. To point tests at another server, pass its URI to `mongotest.Attach(ctx, uri)`.
