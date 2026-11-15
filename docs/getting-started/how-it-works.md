---
title: How it works
description: What mongotest does between the call and the first query, and the defaults it chooses.
status: current
---

# How it works

Each instance owns one container. This page follows one from creation to
removal and lists the defaults. The [mongod reference](../reference/mongod.md)
has the detail of each step.

## The lifecycle

<div class="mt-steps" markdown>

1.  **Find the daemon,** as the `docker` CLI does: `DOCKER_HOST` if set,
    otherwise the usual sockets. mongotest speaks the Engine API itself and
    never runs the `docker` binary.

2.  **Work out the address** this process can reach the container at. It is
    loopback for a local daemon; [Remote and nested daemons](../guides/remote-daemons.md)
    covers the rest.

3.  **Create the container,** with a generated `mongotest-…` name, the label
    `mongotest=regression`, and a port Docker chooses. A missing image is
    pulled first.

4.  **Wait until `mongod` answers,** and with a replica set until it accepts
    writes.

5.  **Connect** with the container's URI, and with `WithTLS` its TLS
    configuration, then ping once. If that fails, the container is removed.

6.  **Remove.** `Run` removes the container when the test ends; a caller of
    `Start` calls `Stop`. A signal handler covers a run that is interrupted;
    see [Cleanup and reaping](../guides/cleanup.md).

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
| Credentials | none on loopback |
| Start timeout | one minute |

## The Docker client

mongotest uses [`dockerapi`](../docker-clients/dockerapi.md), which needs only
the standard library. `WithDocker` chooses another client.
