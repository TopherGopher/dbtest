---
title: Installation
description: Add mongotest to a Go module and check that a Docker daemon is reachable.
status: current
---

# Installation

## Add the module

```sh
go get github.com/tophergopher/mongotest
```

## Check the daemon

mongotest finds the daemon as the docker CLI does: `DOCKER_HOST`, then the
current docker context, then the usual Docker and Podman sockets. Check that
a daemon is running:

```sh
docker version
```

If that prints a server version, mongotest finds the same daemon, except on
Windows; see the Docker Desktop tab.

=== "Docker Engine (Linux)"

    Install from [docs.docker.com](https://docs.docker.com/engine/install/)
    and make sure your user can reach the socket without `sudo`:

    ```sh
    sudo usermod -aG docker "$USER"
    newgrp docker
    docker version
    ```

=== "Docker Desktop (macOS, Windows)"

    Start Docker Desktop. On macOS mongotest finds its socket without
    configuration.

    On Windows, mongotest's default Docker client cannot open Docker's named
    pipe. You have two choices. Turn on "Expose daemon on
    tcp://localhost:2375 without TLS" in Docker Desktop's settings, and
    mongotest finds it. This lets any program on the machine control Docker.
    Or use the [`mobyclient`](../docker-clients/mobyclient.md) client, which
    opens the pipe.

=== "Podman"

    Podman serves the same API as Docker, and mongotest finds its socket.
    On Linux, start the socket:

    ```sh
    systemctl --user enable --now podman.socket
    ```

    On macOS and Windows, run `podman machine start`.

    If Docker also runs, mongotest finds Docker's socket first. To use
    Podman instead, set `DOCKER_HOST`:

    ```sh
    export DOCKER_HOST=unix://$XDG_RUNTIME_DIR/podman/podman.sock
    ```

    The [Podman guide](../guides/podman.md) covers rootless setups,
    `podman machine`, and the API version differences.

## Pull the image once

The first start on a machine pulls the image. To keep that download out of
the tests, call `mongod.PullImage` in `TestMain`. It pulls the image a start
would run, `MONGOTEST_IMAGE` included, and leaves one the daemon already has
alone; see [Which image](../reference/mongod.md#which-image).

## Check the installation

Run the [quickstart](quickstart.md) test.

## Troubleshooting the first run

??? failure "`cannot connect to the docker daemon at …`"
    The daemon is not running, or it is listening somewhere mongotest did not
    look. Run `docker version`; if that fails too, start the daemon. If it
    works, you are probably on Windows with the default client; see the
    Docker Desktop tab. Elsewhere, set `DOCKER_HOST` to the address the CLI
    uses:

    ```sh
    export DOCKER_HOST=$(docker context inspect --format '{{.Endpoints.docker.Host}}')
    ```

??? failure "The container starts, but `mongod` exits or is not ready in time"
    The error names the step that failed and matches a `mongod` sentinel,
    such as `mongod.ErrNotReady` or `mongod.ErrContainerExited`, and says
    what to do. When the container exits, the error ends with what `mongod`
    wrote last, which usually says why. If it shows nothing, run the image by
    hand with the same flags.
