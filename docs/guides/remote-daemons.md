---
title: Remote and nested daemons
description: How the address to dial is worked out when the Docker daemon is not on loopback, and how each setup is tested in CI.
status: current
tracks: ["#52", "#81"]
---

# Remote and nested daemons

`mongotest.Run` and `mongod.Start` work out an address that reaches the
container. Loopback works only when the test process and the daemon share a
network.

## The four setups

| Setup | How it looks | Where the port really is |
| --- | --- | --- |
| **Socket bind-mount** (sibling containers) | Tests run in a container started with `-v /var/run/docker.sock:/var/run/docker.sock` | Not published: mongod joins the test container's network and is dialled at its address there, on 27017 |
| **True docker-in-docker** | A `docker:dind` service, `DOCKER_HOST=tcp://docker:2375` | On the dind container, reachable as `docker:<port>` |
| **Socket proxy** | `tecnativa/docker-socket-proxy` in front of the socket | Wherever the daemon behind the proxy puts it. The proxy may also refuse calls dbtest makes. |
| **Remote daemon** | `DOCKER_HOST=tcp://build-host:2376` | On `build-host` |

The socket bind-mount is the most common in CI. It is often called
docker-in-docker, though nothing is nested.

## How the address is worked out

`mongod.Start` works the address out before it creates anything, and uses
the first of these that applies. For rules 5 to 7, a process counts as
containerised when `/.dockerenv` or `/run/.containerenv` exists, which
Docker and Podman create.

| # | Condition | Address |
| --- | --- | --- |
| 1 | `Options.WithHostIP` was given | that value |
| 2 | `DBTEST_HOST_IP` is set | that value |
| 3 | This process is in a container on the same daemon, not on the host network, and neither `WithPort` nor `WithTLS` was given | mongod's address on that network, port 27017 |
| 4 | The daemon is `tcp://`, `http://`, `https://` or `ssh://` | the daemon's hostname |
| 5 | Unix socket or named pipe, and this process is not containerised | `127.0.0.1` |
| 6 | Unix socket, this process is containerised but shares the host network (`--network host`, `hostNetwork` pods) | `127.0.0.1` |
| 7 | Unix socket, this process is containerised, not on the host network, and rule 3 did not apply (Podman, `WithPort`, `WithTLS`) | the default route's gateway, from `/proc/net/route` |

Use the first two when the detection is wrong. `DBTEST_HOST_IP` is for CI
configuration that cannot pass an option.

Rule 3 publishes no port: mongod is created on the test container's own
network. To find the test's own container, a start reads the
`/etc/hostname` mount in `/proc/self/mountinfo`. It then asks the daemon
about that container, and uses a network only if the container's address on
it belongs to this process. When the daemon
does not know this process's container, as with a `docker:dind` service,
the next rule applies. Rule 3 recognises Docker containers only; under
Podman the later rules apply.

If the test container is on several networks, mongod joins the
user-defined one whose name sorts first. A user-defined network is one made
with `docker network create`, as Docker Compose and a GitHub Actions
`container:` job do; the default bridge is used only when there is no
other.

`WithTLS` turns rule 3 off because the certificate must name mongod's
address before the container has one.

Where containers on a network cannot reach each other, as on a bridge with
`icc=false`, Docker's setting that stops containers on a network talking to
each other, rule 3 fails: the start times out with `ErrNotReady`. Its error
names the address to set instead, such as `172.17.0.1`; set it with
`WithHostIP` or `DBTEST_HOST_IP`, as under [Using it](#using-it).

To reach mongod from outside the test container, for example from the
laptop running a devcontainer, set `WithPort`: the port is then published
on the Docker host.

Whether mongod gets a password depends on who else can reach it:

- Published on `127.0.0.1`: no password.
- Published on every interface, or on a network shared with the test
  container: a root user, `mongotest`, with a random password, which
  `URI()` includes. Other containers on a shared network can connect, and
  some networks, such as macvlan and overlay networks, reach other machines
  too. See [Who can connect](../reference/mongod.md#who-can-connect).

## Using it

With `mongotest.Run` or `mongod.Start` these setups need no configuration, apart from a socket
proxy that refuses a call. Start the proxy with `CONTAINERS=1`, `IMAGES=1`,
`EXEC=1` and `POST=1`. A request it refuses matches
`dockerclient.ErrUnauthorized` with `StatusError.FromProxy` true, and the
message names the settings that allow it.

If the address is wrong, set it:

```sh
DBTEST_HOST_IP=172.17.0.1 go test ./...
```

or in code, `mongotest.DefaultOptions().WithHostIP("172.17.0.1")`.

## Testing each setup

dbtest's own CI tests these setups: a sibling container on a user-defined
network, a sibling container with `--network host`, a `docker:dind`
service, and a daemon reached over `tcp://`, which stands in for a remote
daemon. The socket proxy is not tested. To run one from a clone of the
repository, from its root:

```sh
.github/reach-setups.sh sibling
```
