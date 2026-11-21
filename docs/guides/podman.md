---
title: Podman
description: Running dbtest against Podman's Docker-compatible socket.
status: current
tracks: ["#51"]
---

# Podman

Podman serves the Docker Engine API, so dbtest works against it unchanged
once it can find the socket. Nothing in dbtest shells out to a `docker` or
`podman` binary.

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

    `podman machine` forwards the API to the Docker default socket on macOS,
    so no further configuration is usually needed. On Windows it exposes an
    AF_UNIX socket under `%TEMP%` since Podman 5.3, which dbtest can dial.

## Point dbtest at it

Set `DOCKER_HOST` the same way you would for the `docker` CLI:

```sh
export DOCKER_HOST=unix://$XDG_RUNTIME_DIR/podman/podman.sock
go test ./...
```

Even this is optional. When nothing is configured, dbtest's
[`dockerapi`](../docker-clients/dockerapi.md) client probes, in order,
rootless Docker, system Docker, rootless Podman and system Podman, and uses
the first socket that answers.

## API versions

`dockerapi` works with every supported Podman.

`dockerapi` also reports which engine answered:

```go
c, _ := dockerapi.FromEnv()
if c.Runtime() == dockerapi.RuntimePodman {
    // Podman identifies itself through the Libpod-API-Version header.
}
```

Version errors name the product, so a Podman user is never told to upgrade a
Docker daemon they do not have.

## Rootless networking

Rootless Podman forwards published ports with pasta, its user-mode network
tool. pasta can deliver a published port to a program that listens only on
the container's 127.0.0.1, which Docker never does. It does so when:

- pasta is a release from before 30 October 2024, or
- `pasta_options` in `containers.conf` includes `--host-lo-to-ns-lo`.

So a test that passes `WithMongodArgs("--bind_ip", "127.0.0.1")` can pass on
such a Podman and time out on Docker. Leave `--bind_ip` out, and mongod
listens where every runtime can reach it.

For the same reason, a container that does not enforce its credentials
fails with `ErrAuthNotEnforced` only when the start timeout ends, not at
once. While an image creates its root user, it runs a temporary mongod on the
container's 127.0.0.1 with no authentication, and pasta can let dbtest reach
it. dbtest cannot tell that mongod from the final one, so it keeps trying
until the timeout.

### Checking your setup

To see which pasta Podman runs:

```sh
podman info --format '{{.Host.Pasta.Version}}'
```

The version holds the release date: `2024_10_30` from the pasta project,
`0^20241030` in Fedora's package. Where it says `unknown version`, ask the
package manager instead, such as `dpkg -s passt` or `rpm -q passt`.

The option is set in `~/.config/containers/containers.conf`, or in
`/etc/containers/containers.conf` for every user:

```toml
[network]
pasta_options = ["--host-lo-to-ns-lo"]
```

## Troubleshooting

??? failure "`cannot connect to the docker daemon at …`"
    The socket is not running or `DOCKER_HOST` points at the wrong path. Check
    by running `podman --remote info` with `CONTAINER_HOST` set to your
    `DOCKER_HOST` value, or
    `curl --unix-socket "$XDG_RUNTIME_DIR/podman/podman.sock" http://d/_ping`.
