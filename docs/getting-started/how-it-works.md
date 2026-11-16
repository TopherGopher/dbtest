---
title: How it works
description: What mongotest does between the call and the first query, and the defaults it chooses.
status: current
---

# How it works

`mongotest.Run` returns an `*Instance`: a connected driver client and the
container behind it. This page follows that container from creation to
removal and lists the defaults. The [mongod reference](../reference/mongod.md)
has the detail of each step.

## The lifecycle

<div class="mt-steps" markdown>

1.  **Find the daemon,** as the `docker` CLI does: `DOCKER_HOST` if set,
    otherwise the default socket paths. mongotest calls the Docker Engine API
    directly; it never runs the `docker` program.

2.  **Work out the address** at which the tests can reach the container. For
    a local daemon this is loopback, 127.0.0.1;
    [Remote and nested daemons](../guides/remote-daemons.md) covers the rest.

3.  **Create the container,** with a generated `mongotest-…` name, the label
    `mongotest=regression`, and a port Docker chooses. A missing image is
    pulled first.

4.  **Wait until `mongod` answers.** For a replica set, also wait until it
    accepts writes.

5.  **Connect** a client with the container's URI, plus its TLS configuration
    when `WithTLS` is set. Ping the server once. If the connection or the ping
    fails, the container is removed.

6.  **Remove.** `Run` removes the container when the test ends. If you used
    `Start` instead, call `Stop` yourself; see
    [Starting a server](../guides/test-connection.md). A signal handler covers
    a run that is interrupted; see [Cleanup and reaping](../guides/cleanup.md).

</div>

## Why a container per test

Each test starts with an empty database, so tests cannot see each other's
data and can run in parallel. If one container per test is too slow for a
large table of cases, share one and give each case its own database; see
[Table-driven and parallel tests](../guides/parallel-tests.md).

## Defaults at a glance

| Setting | Default |
| --- | --- |
| Image | `docker.io/library/mongo:8.0`, or `MONGOTEST_IMAGE` |
| Bind address | `127.0.0.1` for a local daemon |
| Port | chosen by Docker |
| Container name | `mongotest-<random>` |
| Label | `mongotest=regression` |
| Credentials | none on loopback; a generated root user when published beyond it ([Who can connect](../reference/mongod.md#who-can-connect)) |
| Start timeout | one minute, not counting a pull |
| Pull timeout | ten minutes |

## The Docker client

mongotest uses [`dockerapi`](../docker-clients/dockerapi.md), which needs only
the standard library. To use a different Docker client, pass it with
`WithDocker`.
