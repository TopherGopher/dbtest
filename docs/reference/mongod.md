---
title: The mongod package
description: "Start a MongoDB container and get its URI, with no MongoDB driver imported."
status: current
tracks: ["#32", "#33"]
---

# The mongod package

`github.com/tophergopher/dbtest/mongod` starts a MongoDB container, waits
until it answers, tells you where to connect, and removes it if the process
is interrupted. It imports no MongoDB driver, so any driver version works
with it.

Reach for `mongod` when you want the container without a driver. For a
connection with the driver already set up, use
[`mongotest.Run`](../guides/test-connection.md).

```go
--8<-- "mongod/example_test.go:start"
```

`Start` runs `docker.io/library/mongo:8.0`, publishes `mongod` on a host port
the daemon picks, labels the container `mongotest=regression`, and waits up
to a minute. When the tests run in a container on the same daemon, nothing
is published: `mongod` joins the network of the tests' container instead.

## Options

`StartWithOptions` takes an `Options` value. Begin with `DefaultOptions` and
chain the `With` methods:

```go title="Illustration, not compiled"
base := mongod.DefaultOptions().WithImage("7.0")
c, err := mongod.StartWithOptions(ctx, base.WithReplicaSet("rs0"))
```

Each method returns a copy, so `base` is unchanged and can be shared between
tests. `Start(ctx)` is `StartWithOptions(ctx, mongod.DefaultOptions())`.

| Method on `Options` | Effect |
| --- | --- |
| `WithImage(ref)` | The image. A version (`"7.0"`) changes only the tag, a repository changes only the repository, and a full reference replaces the image. |
| `WithMongoImage(img)` | An image value, such as one of the [known images](#which-image). Only the parts the value names change, so a version named earlier is kept unless the value names one. |
| `WithReplicaSet(name)` | A single-member [replica set](#replica-sets), ready for writes. |
| `WithAuth(user, password)`, `WithoutAuth()` | See [Who can connect](#who-can-connect). |
| `WithPort(port)` | A fixed host port. Prefer the default: a port found free can be taken before the container binds it. |
| `WithHostIP(host)` | The address containers are reachable at; see [Remote and nested daemons](../guides/remote-daemons.md). |
| `WithStartTimeout(d)` | How long a start waits, replica set initiation included. The default is one minute. |
| `WithMongodArgs(args...)` | Extra flags for `mongod`. |
| `WithTLS()` | Accept only TLS connections; see [TLS](#tls). |
| `WithNamedVolume(name)` | Keep the data in a Docker volume that a later start finds again; see [Keeping data between runs](#keeping-data-between-runs). |
| `WithName(name)`, `WithLabel(key, value)` | The container's name, and labels besides `mongotest=regression`. |
| `WithLogger(l)` | Logs the steps of a start, at Info and Warn. `*slog.Logger` works; for zap, zerolog or logrus, [wrap it in one](#logging-with-zap-zerolog-or-logrus). By default nothing is logged. Docker requests are not logged here: to see them, build a client with [`dockerapi.WithLogger`](../docker-clients/dockerapi.md#options) and pass it to `WithDocker`. |
| `WithDocker(client)` | A specific Docker client, such as a [double](#testing-code-that-starts-containers). |

Three settings can come from the environment, so CI can set them for a whole
suite. An option beats the variable, and the variable beats the default.

| Variable | Sets |
| --- | --- |
| `MONGOTEST_IMAGE` | The image, in any shape `WithImage` accepts. |
| `MONGOTEST_START_TIMEOUT` | The start timeout, as a Go duration such as `90s`. |
| `DBTEST_HOST_IP` | The address containers are reachable at. |

A version from an option combines with an image from the environment:
`WithImage("7.0")` under `MONGOTEST_IMAGE=mirror.corp.example/mongo` runs
`mirror.corp.example/mongo:7.0`.

### Logging with zap, zerolog or logrus

zap, zerolog and logrus each provide a `slog` handler. Pass it to
`slog.New`, and pass the logger that returns to `WithLogger`.

```go title="Illustration, not compiled"
// zap: the handler is in zap's experimental module, so its API may change.
import "go.uber.org/zap/exp/zapslog"

logger := slog.New(zapslog.NewHandler(zapLogger.Core()))
opts := mongod.DefaultOptions().WithLogger(logger)
```

```go title="Illustration, not compiled"
// zerolog
import "github.com/rs/zerolog"

logger := slog.New(zerolog.NewSlogHandler(zerologLogger))
opts := mongod.DefaultOptions().WithLogger(logger)
```

```go title="Illustration, not compiled"
// logrus: the handler's package is also named slog, so import it under
// another name.
import logrusslog "github.com/sirupsen/logrus/hooks/slog"

logger := slog.New(logrusslog.NewHandler(logrusLogger, nil)) // nil: default options
opts := mongod.DefaultOptions().WithLogger(logger)
```

## Which image

Five images are known:

| Value | Runs when no version is named | Accepts a bare version such as `8` | Takes `mongod` flags |
| --- | --- | --- | --- |
| `ImageDockerHub` | `docker.io/library/mongo:8.0` | yes | yes |
| `ImagePublicECR` | `public.ecr.aws/docker/library/mongo:8.0` | yes | yes |
| `ImageCommunity` | `docker.io/mongodb/mongodb-community-server:8.0-ubi9` | no | yes |
| `ImageEnterprise` | `docker.io/mongodb/mongodb-enterprise-server:8.0-ubi9` | no | yes |
| `ImageAtlasLocal` | `docker.io/mongodb/mongodb-atlas-local:8.0` | yes | no |

Every image is named in full, registry included, so nothing depends on a
daemon's default registry. A short name such as `mongo:7` is accepted
wherever an image is, and is taken to be Docker Hub's:
`docker.io/library/mongo:7`.

A known image names no version of its own; a start fills in the default the
table shows. A version you name, with `WithImage` or in an image value such
as `mongod.ImageCommunity.WithVersion("8.0-ubi8")`, stays until you name
another, even when a later option changes the image. So
`WithImage("mongo:7.0").WithMongoImage(mongod.ImageCommunity)` still asks for
`7.0`, which the Community image does not publish, and the start is refused.

A start refuses these mistakes about the image before it creates anything:

- A bare version such as `8` for `ImageCommunity` or `ImageEnterprise`, whose
  tags all name an OS variant such as `8.0-ubi9`: `ErrNoSuchVersion`.
- `WithReplicaSet`, `WithMongodArgs` or `WithTLS` for `ImageAtlasLocal`, which
  runs its own replica set and takes no `mongod` flags:
  `ErrUnsupportedForImage`.
- `WithNamedVolume` for `ImageAtlasLocal`, which names its replica set after
  the container and so may not start again on a kept volume:
  `ErrUnsupportedForImage`.

An image the package does not know is treated like the official one. To pull
from your own registry or mirror, name the image there:

```go title="Illustration, not compiled"
mongod.DefaultOptions().WithImage("mirror.corp.example/mongo")
```

A start pulls an image the daemon does not have. Calls to `Start` and
`PullImage` in one process that need the same image share one pull. A pull
that fails because a registry is briefly down is tried again for about a
minute.

When Docker Hub's rate limit refuses a pull, or Docker Hub is still down after
those retries, the pull tries a mirror that serves the same image:

| Image | Mirror |
| --- | --- |
| A Docker official image, such as `mongo` | Amazon ECR Public (`public.ecr.aws/docker`) |
| An image under `mongodb/`, such as `mongodb/mongodb-community-server` | `quay.io/mongodb` |

Other images have no mirror, and an image you name on another registry, such
as your own, is pulled from there alone. A pull stops at Docker Hub, without
trying the mirror, when the image is missing or private there. The mirrors
cannot be turned off.

An image pulled from a mirror keeps the mirror's name, such as
`public.ecr.aws/docker/library/mongo:8.0`. The container runs under that name,
and `Container.Image` returns it. The mirrors serve the same image as Docker
Hub, so the tag is enough.

After a rate limit refuses a pull from Docker Hub or a mirror, that registry is
skipped for five minutes; a pull with nowhere else to go fails at once until
then. If Docker Hub and the mirror both fail, the error says why each one
failed. It matches `dockerclient.ErrRateLimited` if either was a rate limit.

`PullImage` pulls ahead of time, for the image a start with
the same options would run, and does nothing if the daemon already has it.
Call it once in `TestMain`, so the download happens before any test starts
and no test's own deadline has to allow for it:

```go
func TestMain(m *testing.M) {
	--8<-- "mongod/example_test.go:pull-test-main"
}
```

## The container

| Method | Returns |
| --- | --- |
| `URI()` | `mongodb://<host>:<port>/?directConnection=true`, with `<user>:<password>@` when the container has credentials, and `&tls=true` with `WithTLS`. |
| `TLSConfig()` | With `WithTLS`, the client configuration that trusts the container's certificate; otherwise nil. |
| `Host()`, `Port()` | The address to dial. The host is not always loopback; see [Remote and nested daemons](../guides/remote-daemons.md). |
| `ID()`, `Name()`, `Image()` | What was started. |
| `Username()`, `Password()` | The root user, or empty for a `mongod` that does not authenticate. `URI()` already includes them. |
| `ReplicaSet()` | The name given to `WithReplicaSet`, or empty. |
| `Volume()` | The volume that keeps the data, or empty when `Stop` removes it. |
| `Stop(ctx)` | Removes the container and its anonymous volumes, keeping a volume from `WithNamedVolume`. Safe to call twice. |

## When a start returns

A start returns once `mongod` has answered a command on the address in the
URI, so the first command you send works. Some images start a temporary
`mongod`, create the user, then restart it. The start waits for the restarted
one, and for the image's health check if it has one. That check can pass
while the temporary `mongod` still answers. So once it passes, the start
waits a few seconds and asks again on the same connection. If that
connection has dropped, the temporary `mongod` has gone, and the start keeps
waiting.

A start that fails removes the container it created. The error says which
step failed, and matches a sentinel such as `ErrNotReady` or
`ErrContainerExited` with `errors.Is`. When `mongod` was not ready in time,
the message says which of these happened: nothing answered at the address,
something answered that is not `mongod`, `mongod` answered with an error, or
`mongod` did not say whether it enforces the credentials.
A start that gets no answer from the daemon ends at the start timeout, unless
it is pulling an image: the start timeout does not cover a pull, which has
its own limit, ten minutes by default; see `WithPullTimeout`.

## Many containers at once

Containers can be started in parallel, and each is its own server with its
own data. At most 8 containers per CPU start at once in a process; further
starts wait their turn. A start counts against the limit until it returns,
so the limit caps how many containers are starting, not how many are
running. `mongod.SetMaxConcurrentStarts(n)` sets the limit to n starts in
total, not per CPU, and returns the previous limit. Call it before the
first start, for example in `TestMain`.

The wait counts against the context passed to the start, not against the
start timeout. If that context ends first, the start fails with
`ErrNotReady`. Give the context a later deadline.

Each test binary has its own limit, and `go test ./...` runs several
binaries at once, so a machine can be starting more containers than one
limit allows. On a small machine, lower `-p` as well.

A start that runs out of time says in its error how many containers were
starting at once. If they filled the limit, the error says they may have
slowed it, and advises lowering the limit.

## Who can connect

A `mongod` with no users accepts anyone who can reach its port.

A container published on loopback gets no credentials. A container
published on every interface, or on a network shared with the tests' own
container, gets a root user, `mongotest`, with a random password. That
happens with a remote daemon, when `WithHostIP` is not loopback, and when
the tests run in a container that is not on the host network. `URI()` includes the credentials, so a test that takes
its connection string from the container runs unchanged whether or not the
container has them.

`WithAuth` sets credentials of your own. Use `WithoutAuth` to skip the user on
a network you trust. Creating the user makes a start a few seconds slower,
which is why a container only this machine can reach gets none unless you
ask.

If `mongod` still answers a client that gives no credentials when the start
timeout ends, the start removes the container and returns an error matching
`ErrAuthNotEnforced` rather than hand back an open `mongod`. That happens when the image did not create the user, or when a
flag such as `--transitionToAuth` turns authentication off. Use an image that
creates the user, drop the flag, or pass `WithoutAuth`.

Anyone who can use the daemon can read the credentials. Traffic is not
encrypted unless you ask for [TLS](#tls).

## TLS

`WithTLS()` makes `mongod` accept only TLS connections. Each container gets
its own certificate authority, and a server certificate signed by it. Both
are made in memory and expire after a day. The server certificate is valid
for the host in `URI()` and for `localhost`.

Give a driver both the URI, which includes `tls=true`, and `TLSConfig()`,
which trusts the container's certificate authority. Apply the URI first: a URI
applied afterwards replaces the TLS configuration.

```go title="Illustration, not compiled"
client, err := mongo.Connect(options.Client().ApplyURI(c.URI()).SetTLSConfig(c.TLSConfig()))
```

`RunScript` and replica set initiation connect with TLS too. Atlas Local takes
no `mongod` flags, so `WithTLS` is refused for it with
`ErrUnsupportedForImage`.

## Replica sets

`WithReplicaSet("rs0")` starts `mongod` as the only member of a replica set,
which is what transactions and change streams need. The start initiates the set
and returns once it accepts writes. With credentials it also supplies the key
file `mongod` then requires. An empty name is refused with
`ErrEmptyReplicaSet`.

Initiating uses the shell in the image: `mongosh`, or the legacy `mongo`
where the image has only that. An image with neither fails with
`ErrNotInitiated`. A replica set adds about half a second to a start.

## Keeping data between runs

By default the data goes when the container goes. To load a large data set
once and keep it for later runs, keep the data in a named Docker volume:

```go title="Illustration, not compiled"
c, err := mongod.StartWithOptions(ctx, mongod.DefaultOptions().WithNamedVolume("cart-fixtures"))
```

A later start with the same name finds the data again, and `c.Volume()`
returns the name. To know whether a start found data, look for a marker
document that the run which loaded the data left behind.

Nothing in dbtest removes the volume: not `Stop`, not the signal handler and
not `ReapRunningContainers`. Remove it with `docker volume rm <name>`, or
pass a new name to start afresh. `Stop` shuts `mongod` down cleanly before
it removes the container, so no acknowledged write is lost. A process killed
with `SIGKILL` gets no such step.

A kept volume has these limits:

- The root user and password are those of the first start. A later
  `WithAuth` with other credentials does not change them, and the start
  fails with `ErrVolumeCredentialsChanged`.
- The password guards the running `mongod`, not the volume. A later start
  without `WithAuth` serves the kept data with no password, and anyone who
  can use the Docker daemon can mount the volume.
- The replica set name is that of the first start. Pass the same
  `WithReplicaSet` name every time.
- A volume works only with the MongoDB release that created it and the next
  one. With another release the start fails with `ErrContainerExited`.
- Only one container can use a volume at a time. A second start while the
  first runs fails with `ErrContainerExited`, so tests that run at the same
  time each need their own name.

When the container would get a generated password (see
[Who can connect](#who-can-connect)), the start returns
`ErrVolumeNeedsCredentials`, because the next start would generate a
different one. Pass `WithAuth` or `WithoutAuth` as well. An empty name is
refused with `ErrEmptyVolumeName`, and a host directory is refused too,
because a remote daemon would not have it.

## Commands and scripts

`Exec` runs a command inside the container. A command that exits non-zero is
not an error: read `ExitCode` in the result.

`RunScript` runs JavaScript in the image's shell and returns what it printed,
which for a one-line script is the value of its last expression:

```go
--8<-- "mongod/example_test.go:run-script"
```

`db` is the `test` database; use `db.getSiblingDB("app")` for another. Each
shell formats a document its own way, so end with `JSON.stringify(...)` to
parse the result.

On a container with credentials the shell logs in as the root user first. A
script that throws returns a `*ScriptError`, which matches `ErrScriptFailed`
and holds everything the shell wrote. An image with no shell returns
`ErrNoShell`.

## Cleaning up

`Stop` removes the container, even when the context it is given has already
ended, so `t.Cleanup(func() { c.Stop(t.Context()) })` works. Containers that
are never stopped are removed, all at once, when the process gets `SIGINT` or
`SIGTERM`, and the process then dies of that signal. `SIGKILL` cannot be
handled.

To remove whatever is left when a run ends normally, call
`ReapRunningContainers` after `m.Run`:

```go
func TestMain(m *testing.M) {
	--8<-- "mongod/example_test.go:reap-test-main"
}
```

If a container cannot be removed, the error is a `ReapError`, and each
`TeardownError` in it names one container that is still there.

Anything that still escapes carries the label, so one command removes it,
with its volumes:

```sh
docker rm -fv $(docker ps -aq --filter label=mongotest=regression)
```

## Not here yet

- **Data that outlives the container.** Issue
  [#55](https://github.com/TopherGopher/dbtest/issues/55).

## Testing code that starts containers

Pass a double with `Options.WithDocker` and no daemon is needed. A start still
sends `mongod` a command, so the double has to publish a port something answers
on; `mongod/example_test.go` shows one way.
[dockermock](../docker-clients/dockermock.md) describes the doubles.
