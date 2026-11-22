---
title: Podman
description: Running dbtest against Podman's Docker-compatible socket.
status: current
tracks: ["#51"]
---

# Podman

Podman serves the Docker Engine API, so dbtest works against it unchanged
once it can find the socket. dbtest talks to the socket directly, so you do
not need the `docker` or `podman` command installed.

## Start the socket

=== "Rootless (recommended)"

    ```sh
    systemctl --user enable --now podman.socket
    ls "$XDG_RUNTIME_DIR/podman/podman.sock"
    ```

=== "System-wide"

    ```sh
    sudo systemctl enable --now podman.socket
    ls /run/podman/podman.sock
    ```

=== "podman machine (macOS, Windows)"

    ```sh
    podman machine init
    podman machine start
    ```

    On macOS, `podman machine` forwards the API to the Docker default
    socket, which dbtest finds. On Windows, Podman 5.3 or newer exposes a
    socket under `%TEMP%`, and dbtest finds that too.

## Point dbtest at it

dbtest finds Podman's socket on its own. Its
[`dockerapi`](../docker-clients/dockerapi.md) client tries rootless Docker,
system Docker, rootless Podman, then system Podman, and uses the first that
answers. If Docker also runs, set `DOCKER_HOST` to choose Podman:

```sh
export DOCKER_HOST=unix://$XDG_RUNTIME_DIR/podman/podman.sock
go test ./...
```

## Which engine answered

`dockerapi` reports which engine answered, once it has negotiated, so code
can tell Podman from Docker where they behave differently. Here `c` is a
client pointed at a `dockermock.Daemon`, a fake daemon, that answers as
Podman does:

```go
--8<-- "dockerapi/example_test.go:runtime"
```

Podman identifies itself through the `Libpod-API-Version` header. Version
errors name the product, so a Podman user is never told to upgrade a
Docker daemon they do not have.

## Troubleshooting

??? failure "`cannot connect to the docker daemon at …`"
    The socket is not running or `DOCKER_HOST` points at the wrong path. Check
    with `CONTAINER_HOST=$DOCKER_HOST podman --remote info`, or with
    `curl --unix-socket "$XDG_RUNTIME_DIR/podman/podman.sock" http://d/_ping`,
    which prints `OK` when the socket works.
