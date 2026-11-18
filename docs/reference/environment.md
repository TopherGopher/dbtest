---
title: Environment variables
description: Every environment variable mongotest and its Docker clients read.
status: current
---

# Environment variables

The `mongod` package reads three variables of its own. The Docker clients
read the same ones the `docker` CLI does, so a machine where `docker version`
works is a machine where mongotest works.

## Read by the Docker clients

| Variable | Read by | Effect |
| --- | --- | --- |
| `DOCKER_HOST` | `dockerapi`, `mobyclient` | Daemon address: `unix:///path`, `tcp://host:port`, `npipe://` (`mobyclient` only). Overrides every other discovery step. |
| `DOCKER_CONTEXT` | `dockerapi`, `mobyclient` | The Docker context to use when `DOCKER_HOST` is unset. |
| `DOCKER_CONFIG` | `dockerapi`, `mobyclient` | Directory holding `config.json` and `contexts/`. Defaults to `~/.docker`. The `currentContext` key in `config.json` is honoured. |
| `DOCKER_TLS_VERIFY` | `dockerapi`, `mobyclient` | When set to a non-empty value, reach a `tcp://` daemon over TLS and verify its certificate, using the files in `DOCKER_CERT_PATH`, or in the Docker config directory (`DOCKER_CONFIG`, default `~/.docker`) when `DOCKER_CERT_PATH` is unset. |
| `DOCKER_CERT_PATH` | `dockerapi`, `mobyclient` | Directory with `ca.pem`, and `cert.pem` and `key.pem` if the daemon wants a client certificate. It has no effect without `DOCKER_TLS_VERIFY`. |
| `DOCKER_API_VERSION` | `dockerapi`, `mobyclient` | Pin the API version. Without it, both negotiate with the daemon. |
| `XDG_RUNTIME_DIR` | `dockerapi`, `mobyclient` | Where rootless Docker and Podman put their sockets. `/run/user/$UID` is used when unset. |
| `TEMP`, `LOCALAPPDATA` | `dockerapi` on Windows | Where `podman machine` creates its AF_UNIX socket, in `TEMP` or in `LOCALAPPDATA\Temp`. |

## Read by `mongod`

An option beats the variable, and the variable beats the default. See
[The mongod package](../reference/mongod.md#options). `mongotest.Run` and
`Start` read them too, because they start through `mongod`.

| Variable | Sets | Default |
| --- | --- | --- |
| `MONGOTEST_IMAGE` | The image: a version, a repository, or a full reference such as `localhost:5000/team/mongo:8` | `docker.io/library/mongo:8.0` |
| `MONGOTEST_START_TIMEOUT` | The start timeout, as a Go duration | `60s` |
| `MONGOTEST_HOST_IP` | The address containers are reachable at, when the detection is wrong | see [Remote and nested daemons](../guides/remote-daemons.md) |

## Not read

mongotest does not read `MONGO_URI`, `MONGODB_URI` or similar. The earlier
`NewTestConnection(false)` always connects to `mongodb://127.0.0.1:27017`.
To point tests at another server, pass its URI to `mongotest.Attach(ctx, uri)`.
