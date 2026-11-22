---
title: Installation
description: Add dbtest to a Go module and check that a Docker daemon is reachable.
status: current
---

# Installation

## Add the module

Add the module that matches the driver your `go.mod` requires:

```sh
go get github.com/tophergopher/dbtest/mongotest    # if you use go.mongodb.org/mongo-driver
go get github.com/tophergopher/dbtest/mongotestv2  # if you use go.mongodb.org/mongo-driver/v2
```

Starting a new project with no driver yet? Use `mongotestv2`: driver v1 is
in maintenance.

For PostgreSQL, add [`pgtest`](../reference/pgtest.md). `pgtest.Run(t)`
returns a pgx pool and a `*sql.DB`, both connected:

```sh
go get github.com/tophergopher/dbtest/pgtest
```

To use another driver, such as lib/pq, add the root module and import its
[`postgres`](../reference/postgres.md) package instead. It returns a
connection string and adds no dependency:

```sh
go get github.com/tophergopher/dbtest
```

Each module is separate, so you download only what you use:

| Module | Use it for |
| --- | --- |
| `github.com/tophergopher/dbtest/mongotest` | Tests on driver v1, v1.17.10 or later. `mongotest.Run(t)` returns a connected client. |
| `github.com/tophergopher/dbtest/mongotestv2` | Tests on driver v2, v2.10.0 or later. Same API as `mongotest`: `mongotestv2.Run(t)` returns a connected v2 client. |
| `github.com/tophergopher/dbtest/pgtest` | Tests on PostgreSQL with pgx v5, through its pool or `database/sql`. `pgtest.Run(t)` returns both, connected. See [`pgtest`](../reference/pgtest.md). |
| `github.com/tophergopher/dbtest` | The [`mongod`](../reference/mongod.md) and [`postgres`](../reference/postgres.md) packages, which start MongoDB and PostgreSQL without importing a driver, and the Docker client packages. `mongotest`, `mongotestv2` and `pgtest` require it, so their users rarely add it. |
| `github.com/tophergopher/dbtest/mobyclient` | Code that already uses Docker's Go client, moby, and wants dbtest to share it. See [`mobyclient`](../docker-clients/mobyclient.md). |
| `github.com/tophergopher/dbtest/mongotest/exporter` | Writing a collection out as JSON or CSV. It takes a connection string, so it works with either driver. See [Exporting data](../guides/exporting-data.md). |

## Check the daemon

dbtest finds the daemon as the docker CLI does: `DOCKER_HOST`, then the
current docker context, then the usual Docker and Podman sockets. Check that
a daemon is running:

```sh
docker version   # with Podman: podman version
```

If that prints a server version, dbtest finds the same daemon, except on
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

    Start Docker Desktop. On macOS dbtest finds its socket without
    configuration.

    On Windows, dbtest's default Docker client cannot open Docker's named
    pipe. You have two choices. Turn on "Expose daemon on
    tcp://localhost:2375 without TLS" in Docker Desktop's settings, and
    dbtest finds it. This lets any program on the machine control Docker.
    Or use [`mobyclient`](../docker-clients/mobyclient.md), which opens the
    pipe.

=== "Podman"

    Podman serves the same API as Docker, and dbtest finds its socket.
    On Linux, start the socket:

    ```sh
    systemctl --user enable --now podman.socket
    ```

    On macOS and Windows, run `podman machine start`.

    If Docker also runs, dbtest finds Docker's socket first. To use
    Podman instead, set `DOCKER_HOST`:

    ```sh
    export DOCKER_HOST=unix://$XDG_RUNTIME_DIR/podman/podman.sock
    ```

    The [Podman guide](../guides/podman.md) covers rootless setups,
    `podman machine`, and how dbtest tells Podman from Docker.

## Pull the image once

This step is optional. The first test run on a machine downloads the
MongoDB image. Tests wait for the download, which does not count against
their start timeout, and fail if it fails. Pulling it first means one
download, before any test starts, rather than a first test that waits for
it. To do that, call `mongod.PullImage` from `TestMain`. The package to import
is `github.com/tophergopher/dbtest/mongod`:

```go
func TestMain(m *testing.M) {
	--8<-- "mongod/example_test.go:pull-test-main"
}
```

It pulls the image the tests would use, including one set by
`MONGOTEST_IMAGE`, and does nothing if the daemon already has it; see
[Which image](../reference/mongod.md#which-image). For PostgreSQL, call
`postgres.PullImage` the same way; see
[The postgres package](../reference/postgres.md#which-image).

## Check the installation

Run the [quickstart](quickstart.md) test.

## Troubleshooting the first run

??? failure "`cannot connect to the docker daemon at …`"
    The daemon is not running, or it is listening somewhere dbtest did not
    look. Run `docker version`; if that fails too, start the daemon. If it
    works, you are probably on Windows with the default client; see the
    Docker Desktop tab. Elsewhere, set `DOCKER_HOST` to the address the CLI
    uses:

    ```sh
    export DOCKER_HOST=$(docker context inspect --format '{{.Endpoints.docker.Host}}')
    ```

??? failure "The container starts, but `mongod` exits or is not ready in time"
    The error names the step that failed and says what to do. Test for it
    with `errors.Is` against a sentinel from the `mongod` package, such as
    `mongod.ErrNotReady` or `mongod.ErrContainerExited`. When the container
    exits, the error ends with the server's last log lines, which usually say
    why. On a slow machine, a longer start timeout is the usual fix: set
    `MONGOTEST_START_TIMEOUT` to a Go duration such as `120s`.
