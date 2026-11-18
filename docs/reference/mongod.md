---
title: The mongod package
description: "Start a MongoDB container and get its URI, with no MongoDB driver imported."
status: current
tracks: ["#32", "#33"]
---

# The mongod package

`github.com/tophergopher/mongotest/mongod` starts a MongoDB container, waits
until it answers, tells you where to connect, and removes it if the process
is interrupted. It imports no MongoDB driver, so any driver version works
with it.

Reach for `mongod` when you want the container without a driver. For a
connection with the driver already set up, use
[`mongotest.Run`](../guides/test-connection.md).

```go
c, err := mongod.Start(ctx)
if err != nil {
    return err
}
defer c.Stop(context.Background())

client, err := mongo.Connect(options.Client().ApplyURI(c.URI())) // driver v2 here; v1 takes a ctx first
```

`Start` runs `docker.io/library/mongo:8.0`, publishes `mongod` on a host port
the daemon picks, labels the container `mongotest=regression`, and waits up
to a minute.

## Options

`StartWithOptions` takes an `Options` value. Begin with `DefaultOptions` and
chain the `With` methods:

```go
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
| `WithName(name)`, `WithLabel(key, value)` | The container's name, and labels besides `mongotest=regression`. |
| `WithLogger(l)` | Where a start is logged. `*slog.Logger` works; the default discards. |
| `WithDocker(client)` | A specific Docker client, such as a [double](#testing-code-that-starts-containers). |

Three settings can come from the environment, so CI can set them for a whole
suite. An option beats the variable, and the variable beats the default.

| Variable | Sets |
| --- | --- |
| `MONGOTEST_IMAGE` | The image, in any shape `WithImage` accepts. |
| `MONGOTEST_START_TIMEOUT` | The start timeout, as a Go duration such as `90s`. |
| `MONGOTEST_HOST_IP` | The address containers are reachable at. |

A version from an option combines with an image from the environment:
`WithImage("7.0")` under `MONGOTEST_IMAGE=mirror.corp.example/mongo` runs
`mirror.corp.example/mongo:7.0`.

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

A start refuses two mistakes before it creates anything:

- A bare version such as `8` for `ImageCommunity` or `ImageEnterprise`, whose
  tags all name an OS variant such as `8.0-ubi9`: `ErrNoSuchVersion`.
- `WithReplicaSet`, `WithMongodArgs` or `WithTLS` for `ImageAtlasLocal`, which
  runs its own replica set and takes no `mongod` flags:
  `ErrUnsupportedForImage`.

An image the package does not know is treated like the official one. To pull
through a mirror, name the image there:

```go
mongod.DefaultOptions().WithImage("mirror.corp.example/mongo")
```

A start pulls an image the daemon does not have. Calls to `Start` and
`PullImage` in one process that need the same image share one pull. A pull
that fails because a registry is briefly down is tried again for about a
minute.

When Docker Hub's rate limit refuses a pull, a Docker official image such as
`mongo` is pulled from Amazon ECR Public instead. An image under `mongodb/`,
such as `mongodb/mongodb-community-server`, is pulled from `quay.io/mongodb`.
For the next five minutes, pulls of that image in this process go straight to
the mirror. The container runs the image under the mirror's name, such as
`public.ecr.aws/docker/library/mongo:8.0`, and `Container.Image` returns that
name. To be sure a mirror serves exactly the content you expect, name the
image by digest. These fallbacks are for Docker Hub's images only, and cannot
be turned off. An image you name on another registry, such as
`mirror.corp.example`, is pulled from there alone.

A Docker Hub image with no mirror is tried again for about a minute. If the
limit still refuses it, the error matches `dockerclient.ErrRateLimited`, and
later pulls of that image in this process fail at once for five minutes. So
does an image whose mirror fails too, and that error names both failures.

`PullImage` pulls ahead of time, for the image a start with
the same options would run, and does nothing if the daemon already has it.
Call it once in `TestMain`, so the download happens before any test starts
and no test's own deadline has to allow for it:

```go
func TestMain(m *testing.M) {
    if err := mongod.PullImage(context.Background(), mongod.DefaultOptions()); err != nil {
        log.Fatal(err)
    }
    os.Exit(m.Run())
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
| `Stop(ctx)` | Removes the container and its volumes. Safe to call twice. |

## When a start returns

A start returns once `mongod` has answered a command on the address in the
URI, so the first command you send works. Some images start a temporary
`mongod`, create the user, then restart it. The start waits for the restarted
one, and for the image's health check if it has one.

A start that fails removes the container it created. The error says which
step failed, and matches a sentinel such as `ErrNotReady` or
`ErrContainerExited` with `errors.Is`. When `mongod` was not ready in time,
the message says which of these happened: nothing answered at the address,
something answered that is not `mongod`, or `mongod` answered with an error.
A start that gets no answer from the daemon ends at the start timeout, unless
it is pulling an image: the start timeout does not cover a pull, which has
its own limit, ten minutes by default; see `WithPullTimeout`.

## Many containers at once

Containers can be started in parallel, and each is its own server with its
own data. How many a machine manages at once depends on its CPUs, because
every `mongod` has to be ready within the start timeout; issue
[#82](https://github.com/TopherGopher/mongotest/issues/82) will make extra
starts wait their turn.

## Who can connect

A `mongod` with no users accepts anyone who can reach its port.

A container published on loopback gets no credentials. A container published
on every interface gets a root user, `mongotest`, with a random password.
That happens with a remote daemon, when the tests run in a container, or when
`WithHostIP` is not loopback. `URI()` includes the credentials, so a test that
takes its connection string from the container runs unchanged whether or not
the container has them.

`WithAuth` sets credentials of your own. Use `WithoutAuth` to skip the user on
a network you trust. Creating the user makes a start a few seconds slower,
which is why a loopback container gets none unless you ask.

If the image does not create the user, the start removes the container and
returns `ErrAuthNotEnforced` rather than hand back an open `mongod`.

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

```go
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

## Commands and scripts

`Exec` runs a command inside the container. A command that exits non-zero is
not an error: read `ExitCode` in the result.

`RunScript` runs JavaScript in the image's shell and returns what it printed,
which for a one-line script is the value of its last expression:

```go
ok, err := c.RunScript(ctx, "db.runCommand({ping: 1}).ok") // "1"
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
    code := m.Run()
    if err := mongod.ReapRunningContainers(context.Background()); err != nil {
        log.Print(err)
    }
    os.Exit(code)
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
  [#55](https://github.com/TopherGopher/mongotest/issues/55).

## Testing code that starts containers

Pass a double with `Options.WithDocker` and no daemon is needed. A start still
sends `mongod` a command, so the double has to publish a port something answers
on; `mongod/example_test.go` shows one way.
[dockermock](../docker-clients/dockermock.md) describes the doubles.
