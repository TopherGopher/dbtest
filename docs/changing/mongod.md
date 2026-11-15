---
title: The mongod package
description: "Start a MongoDB container and get its URI, with no MongoDB driver imported. In PR #53."
status: landing
tracks: ["PR #53", "#32", "#33"]
---

# The mongod package

`github.com/tophergopher/mongotest/mongod` starts a MongoDB container, waits
until it answers, tells you where to connect, and removes it if the process
is interrupted. It imports no MongoDB driver, so any driver version works
with it.

Tests use [`NewTestConnection`](../guides/test-connection.md) today. Reach
for `mongod` once PR #53 merges, when you want the container without a
driver.

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
to a minute. `NewTestConnection` runs `mongo:latest` instead.

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
| `WithMongoImage(img)` | One of the [known images](#which-image). A version named with `WithImage` is kept, whichever comes first. |
| `WithReplicaSet(name)` | A single-member [replica set](#replica-sets), ready for writes. |
| `WithAuth(user, password)`, `WithoutAuth()` | See [Who can connect](#who-can-connect). |
| `WithPort(port)` | A fixed host port. Prefer the default: a port found free can be taken before the container binds it. |
| `WithHostIP(host)` | The address containers are reachable at; see [Remote and nested daemons](remote-daemons.md). |
| `WithStartTimeout(d)` | How long a start waits, replica set initiation included. The default is one minute. |
| `WithMongodArgs(args...)` | Extra flags for `mongod`. |
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

| Value | Runs | Accepts a bare version such as `8` | Takes `mongod` flags |
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

A start refuses two mistakes before it creates anything:

- A bare version such as `8` for `ImageCommunity` or `ImageEnterprise`, whose
  tags all name an OS variant such as `8.0-ubi9`: `ErrNoSuchVersion`.
- `WithReplicaSet` or `WithMongodArgs` for `ImageAtlasLocal`, which runs its
  own replica set and takes no `mongod` flags: `ErrUnsupportedForImage`.

An image the package does not know is treated like the official one. To pull
through a mirror, name the image there:

```go
mongod.DefaultOptions().WithImage("mirror.corp.example/mongo")
```

A start pulls an image the daemon does not have. `PullImage` does that ahead
of time, for the image a start with the same options would run, and leaves
alone an image the daemon already has. Call it once in `TestMain`, so that
tests starting in parallel do not each find the image missing:

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
| `URI()` | `mongodb://<host>:<port>/?directConnection=true`, with `<user>:<password>@` when the container has credentials. |
| `Host()`, `Port()` | The address to dial. The host is not always loopback; see [Remote and nested daemons](remote-daemons.md). |
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
A start that gets no answer from the daemon ends at the start timeout.

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

Anyone who can use the daemon can read the credentials, and traffic is not
encrypted.

## Replica sets

`WithReplicaSet("rs0")` starts `mongod` as the only member of a replica set,
which is what transactions and change streams need. The start initiates the set
and returns once it accepts writes. With credentials it also supplies the key
file `mongod` then requires. An empty name is refused with
`ErrEmptyReplicaSet`.

Initiating uses the shell in the image: `mongosh`, or the legacy `mongo`
where the image has only that. An image with neither fails with
`ErrNotInitiated`. A replica set adds about half a second to a start.

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

- **TLS.** There is no option for it until a container can be given a
  certificate, which is issue
  [#34](https://github.com/TopherGopher/mongotest/issues/34).
- **Exec and scripts.** `Container` has no exec method; pass a client with
  `Options.WithDocker` and exec through it. Issue
  [#35](https://github.com/TopherGopher/mongotest/issues/35) adds `Exec` and
  `RunScript`.
- **Data that outlives the container.** Issue
  [#55](https://github.com/TopherGopher/mongotest/issues/55).

## Testing code that starts containers

Pass a double with `Options.WithDocker` and no daemon is needed. A start still
sends `mongod` a command, so the double has to publish a port something answers
on; `mongod/example_test.go` shows one way.
[dockermock](../docker-clients/dockermock.md) describes the doubles.
