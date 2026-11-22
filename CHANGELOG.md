# Changelog

What changed in each release, for someone upgrading: what is different, and
what to do if it affects you.

## Unreleased

### Tests that share a server each get a database of their own

Only code that names a database after `t.Name()`, as the guides used to
show, needs to change.

- **`Instance.DatabaseFor(t)` is new.** It returns a new database that only
  that test uses, and drops it when the test ends. The name is built from the
  test's name, with anything but letters, digits, `-` and `_` replaced, cut to
  fit the server's limit. It ends in a random part, so no two tests share one.
- **If you name a database after `t.Name()`, as the guides used to show, call
  `DatabaseFor` instead.** The server refuses such a name if the test's name
  holds a dot, a `$` or a double quote, or is longer than 63 bytes.
- **The guides now show one server for a whole package,** started in
  `TestMain`: see
  [A container per package](https://tophergopher.github.io/dbtest/guides/parallel-tests/#a-container-per-package).

### A pull tries a mirror when Docker Hub stays down

Nothing needs to change in your code or settings.

- **A pull now tries the image's mirror when Docker Hub is still down after
  about a minute of retries.** Before, only Docker Hub's rate limit sent it
  there. The mirrors have not changed: Amazon ECR Public for a Docker official
  image, and `quay.io/mongodb` for an image under `mongodb/`.
- **A pull that ECR Public refuses with "Data limit exceeded" now matches
  `dockerclient.ErrRateLimited`,** like other registries' rate limits.
- **A registry is skipped for five minutes only after its rate limit refuses
  a pull.** Before, a mirror that failed for any reason was skipped for five
  minutes, so a mirror that was briefly down could not be used again.

### A daemon that cannot be reached or trusted is a connection failure through either client

- **Through `mobyclient`, a refused or timed-out connection, a socket with no
  daemon behind it, or a socket you do not have permission to use now
  matches `dockerclient.ErrConnectionFailed`,** as it already did through
  `dockerapi`. Before, it matched `ErrDaemonResponse`. A socket you do not
  have permission to use also matches `fs.ErrPermission`, and its message
  gives the same fix as `dockerapi`'s.
- **A daemon certificate the client does not trust now matches
  `ErrConnectionFailed` through either client.** Examples are a certificate
  not signed by the CA in `DOCKER_CERT_PATH`, or one that names another
  host. Before, it matched `ErrDaemonResponse`. The message says what to
  check, and `errors.As` still finds the `x509` error.

If you check for `ErrDaemonResponse` to catch these, check for
`ErrConnectionFailed` instead.

## v0.3.0

### The modules are renamed to `github.com/tophergopher/dbtest`

The name now fits the other databases that are planned. Everything in v0.3.0
is new since v0.2.9. Change your imports, then run `go mod tidy` to drop the
old module:

| Before | After |
| --- | --- |
| `github.com/tophergopher/mongotest` | `github.com/tophergopher/dbtest/mongotest` |
| `github.com/tophergopher/mongotest/mongod` | `github.com/tophergopher/dbtest/mongod` |
| `github.com/tophergopher/mongotest/dockerclient`, `/dockerapi`, `/dockermock` | `github.com/tophergopher/dbtest/dockerclient`, `/dockerapi`, `/dockermock` |
| `github.com/tophergopher/mongotest/mobyclient` | `github.com/tophergopher/dbtest/mobyclient` |
| `github.com/tophergopher/mongotest/exporter` | `github.com/tophergopher/dbtest/mongotest/exporter` |

- **The `mongotest` package keeps its name,** and its API is as the entries
  below describe. It is now a module of its own, so the root module no longer
  requires MongoDB's Go driver. Code that uses only `mongod` or the Docker
  clients needs `github.com/tophergopher/dbtest`.
- **`MONGOTEST_HOST_IP` is now `DBTEST_HOST_IP`,** and `mongod.EnvHostIP`
  holds the new name. It is the only variable renamed, because it is the only
  one that is not specific to MongoDB. The old name is no longer read, so a CI
  setting that uses it is ignored without an error. Rename it.
- **Unchanged:** `MONGOTEST_IMAGE`, `MONGOTEST_START_TIMEOUT`, the
  `mongotest=regression` label, the `mongotest-` container name prefix and the
  default username `mongotest`.
- `dockerapi`'s default user agent is now `dbtest-dockerapi/1`, and the
  reaper's messages begin with `dbtest:`.
- The old path, `github.com/tophergopher/mongotest`, gets no releases after
  v0.2.9.

### A program that handles SIGINT and SIGTERM itself can turn off the `mongod` package's signal handler

- **`mongod.DisableSignalHandler()`** turns off the `mongod` package's
  signal handler. That handler removes the containers on SIGINT or SIGTERM
  and then ends the process with the same signal, so a program's own
  shutdown never runs. After calling `DisableSignalHandler`, call
  `mongod.ReapRunningContainers` from your own shutdown, or containers are
  left running. Nothing changes if you do not call it.

### mobyclient finds the daemon as the docker CLI does

- **`mobyclient.New()` and `FromEnv()` now find the daemon as the docker CLI
  does,** unless you pass `WithHost` or `WithMobyClient`: `DOCKER_HOST`, then
  the docker context, then the first Docker or Podman socket that answers. `dockerapi` finds the same daemon. Before, `FromEnv`
  took the address from `DOCKER_HOST` alone, and `New()` ignored the
  environment and used the platform default.
- **`New()` now reads `DOCKER_API_VERSION` and, for a `tcp://` daemon,
  `DOCKER_TLS_VERIFY` and `DOCKER_CERT_PATH`,** whether the address is passed
  with `WithHost` or found. When `DOCKER_TLS_VERIFY` is set and
  `DOCKER_CERT_PATH` is not, the certificates come from the Docker config
  directory, as with the docker CLI.
- **`DOCKER_CERT_PATH` without `DOCKER_TLS_VERIFY` no longer turns on TLS,**
  as with `dockerapi` and the docker CLI. Before, `FromEnv` used TLS without
  checking the server's certificate. Set `DOCKER_TLS_VERIFY=1` to use TLS.
- **`New()` can now fail where it did not:** a broken `config.json`, a
  `DOCKER_CONTEXT` that does not exist, or a bad `DOCKER_API_VERSION` or
  `DOCKER_CERT_PATH`. Each error matches `dockerclient.ErrInvalidArgument`,
  and its `Argument` names the setting to fix.
- **If `New()` now reaches a different daemon,** pass `WithHost` with the
  address you want. To use the moby client's own discovery, build it with
  `client.New(client.FromEnv)` and pass it to `WithMobyClient`.
- **On Windows the two clients reach different endpoints.**
  `mobyclient` opens Docker's named pipe. `dockerapi`, which cannot, tries
  Docker Desktop's TCP endpoint and then a podman machine socket.
- **New: `dockerclient.ResolveHost`** returns the daemon address both clients
  start from, and **`dockerclient.ConfigDir`** the Docker config directory,
  for anyone writing a client of their own.

### `mongod.Start` no longer reads mongod's flags from the container's process list

- **A mongod whose flags are set in a configuration file now starts.**
  `Start` used to read mongod's command line, and refused a mongod whose
  `--auth` it could not see.
- **A container created with credentials is now checked by sending mongod a
  command that needs them, without giving them.** A mongod that answers may
  be the image's set-up mongod, so `Start` keeps waiting. If it still answers
  when the start timeout ends, `Start` removes the container and returns an
  error matching both `ErrAuthNotEnforced` and `ErrNotReady`. Before, it
  returned `ErrAuthNotEnforced` at once.
  `AuthNotEnforcedError`'s message has changed; match the error with
  `errors.Is`, not its text.
- **A start can now end in `ErrNotReady` because mongod never said whether it
  enforces the credentials,** such as a mongod shutting down for the whole
  start timeout.
- **`ErrLoopbackOnly` is deprecated.** Nothing returns it. A mongod that
  listens only on the container's loopback now ends in `ErrNotReady`, with
  nothing answering at the address. It will be removed in the next release
  that breaks compatibility.
- **A daemon or socket proxy that refuses to list a container's processes**
  no longer stops a start.

### dbtest needs Go 1.26 or newer

- **Every module declares `go 1.26`,** the oldest release the Go team still
  supports; v0.2.9 declared `go 1.18`. Requiring dbtest, even only for
  tests, raises your module's `go` line to no more than that.

### A rate-limited pull comes from a mirror

- **When Docker Hub's rate limit refuses a pull,** a Docker official image
  such as `mongo` is pulled from Amazon ECR Public instead. An image under
  `mongodb/` is pulled from `quay.io/mongodb`. Nothing has to be set.
- **Before, such a pull failed** with `dockerclient.ErrRateLimited`. Now it
  fails only when the mirror fails too. That error carries both failures,
  and still matches `dockerclient.ErrRateLimited`.
- **The container runs the image under the mirror's name,** which
  `Container.Image` returns. A test that compares `Container.Image` with the
  name it asked for may now see the mirror's name. `Container.MongoImage`
  still returns the image asked for.

### `mongod.UnresolvedHostError`'s message no longer begins with `mongod:`

- The type, `ErrUnresolvedHost` and `EnvHostIP` are unchanged. Code that
  matched the message text should use `errors.Is(err, mongod.ErrUnresolvedHost)`.

### Start errors no longer begin with `mongod:`

- **`StartError`, `NotReadyError`, `UnpublishedPortError` and
  `AuthNotEnforcedError`** no longer begin with `mongod:`. `NotReadyError` says "the caller's context" rather
  than "the context passed to Start". `ErrNotReady`'s own text is now "the
  container did not become ready in time". Errors about options still begin
  with `mongod:`.
- The types, their fields, the `Op` constants and the sentinels are
  unchanged, so `errors.Is` and `errors.As` match as before; match with
  them, not the text. `%T` prints the types as `*container.StartError`,
  `*container.NotReadyError` and `*container.UnpublishedPortError`.
- **A start that ends with mongod still not enforcing its credentials** no
  longer advises a longer start timeout; the error says what to do instead.

### `mongod.ContainerExitedError`'s message no longer begins with `mongod:`

- It now begins with `container <name>`, and `ErrContainerExited`'s text
  says "before the server in it was ready" rather than "before mongod was
  ready". The type, its fields and `ErrContainerExited` itself are
  unchanged, so `errors.Is` and `errors.As` match as before. Code that
  matched the message text should use `errors.Is(err, mongod.ErrContainerExited)`.
- `%T` prints the error's type as `*container.ExitedError`.
- When the container's output is empty, the message used to suggest a flag
  or replica set name that mongod rejects. Now it says whether the container
  wrote nothing or its log could not be read, and why.

### The `mongotest` package is rebuilt on the new `mongod` package

- **New entry points.** `mongotest.Run(t)` starts a container, connects a
  client of the official MongoDB Go driver (v1, at v1.17.10 or later), and
  removes the container when the test ends. `RunWithOptions` takes options.
  `Start` and `StartWithOptions` work outside a test. `Attach` connects to a
  server you already run. Each returns an `*Instance`, which embeds
  `*mongo.Client`.
- **The exported names from v0.2.9 are removed.** They existed for
  easymongo, a MongoDB library whose tests used them, which dbtest no longer
  supports. v0.2.9's `TestConnection` embedded an easymongo connection;
  `Instance` embeds the driver's client, so `conn.D("db").C("coll")` becomes
  `inst.Database("db").Collection("coll")`, `conn.MongoDriverClient()` becomes
  `inst.Client` and `conn.MongoURI()` becomes `inst.URI()`. Every other
  removed name, and what replaces it, where `inst` is the `*Instance`:

  | Removed | Use instead |
  | --- | --- |
  | `TestConnection` | `Instance` |
  | `NewTestConnection(true)`, `EasyMongoWithContainer` | `Run(t)` in a test, or `Start(ctx)` |
  | `NewTestConnection(false)` | `Attach(ctx, "mongodb://127.0.0.1:27017")`, the server it always connected to |
  | `NewReplicaSetContainer(name)` | `StartWithOptions(ctx, DefaultOptions().WithReplicaSet(name))` |
  | `MongoClientWithContainer(f)`, which called `f` with a client | `Run(t).Client` |
  | `KillMongoContainer()` | `inst.Stop(ctx)` |
  | `MongoContainerID()` | `inst.Container.ID()`; `Container` is nil for an instance from `Attach` |
  | `RunMongoScriptOnContainer(js)` | `inst.Container.RunScript(ctx, js)`; when the script fails, its output is in the `*mongod.ScriptError` |
  | `ExecCommandInMongoContainer(cmd)` | `inst.Container.Exec(ctx, cmd...)`; check `ExitCode`, because a non-zero exit is no longer an error |
  | `GetAvailablePort()` | Nothing: Docker chooses the port |
  | `ReapRunningContainers()` | `mongod.ReapRunningContainers(ctx)`, which now returns an error |
  | `GenerateCARoot()` | `DefaultOptions().WithTLS()`, which makes the certificates |
  | `ErrFailedToConnectToDockerDaemon` | `dockerclient.ErrConnectionFailed` |
  | `ErrMongoContainerAlreadyRunning` | `dockerclient.ErrConflict` |
  | `ErrNoAvailablePorts` | Nothing: only `GetAvailablePort` returned it |

- **`DatabaseExporter` is removed.** It moves to its own module,
  `github.com/tophergopher/dbtest/mongotest/exporter`, which writes to any
  `io.Writer`; its [guide](https://tophergopher.github.io/dbtest/guides/exporting-data/#moving-from-databaseexporter)
  has a table for migrating. `ErrNotImplemented`, `MongoTestError` and `NewMongoTestError` go with
  it.
- **The default image is `mongo:8.0`,** not `mongo:latest`. Set
  `MONGOTEST_IMAGE` or `WithImage` to run another.
- **Replica sets and scripts use `mongosh`,** so they work on every current
  image. Before, they needed the legacy `mongo` shell, which images from
  MongoDB 6.0 do not have.
- **A replica set takes the name you give it.** Before, only `"cf"` worked.
- **Scripts return standard output, trimmed.** Before, they returned standard
  output and standard error together.
- **The `mongotest` package writes nothing to standard output or standard error.**
  It used to log each start through logrus.
- **Errors.** A failed connection matches the new `ErrNotConnected`. A start
  that cannot reach Docker matches `dockerclient.ErrConnectionFailed`, and one
  whose container name is taken matches `dockerclient.ErrConflict`. Check for
  them with `errors.Is` on the error a start returns.
- **A failed start returns no instance and leaves no container.** A cleanup
  call on the error path is no longer needed, but does no harm.
- **The `mongotest` module needs only the driver, testify and the root
  module.** logrus, easymongo and mongo-tools are no longer dependencies.

### Changed

- **Ctrl-C now ends the process.** On SIGINT or SIGTERM dbtest removes the
  containers it started and then lets the signal end the process, as it would
  have without dbtest. Before, the containers were removed but the test
  binary kept running. Nothing to change, unless a program of yours handles
  these signals itself; it can call `mongod.DisableSignalHandler`.
- **The signal handler is installed when the first container is created, not
  when the package is imported.** A program that imports dbtest and starts
  no container keeps its own handling of Ctrl-C. Only SIGINT and SIGTERM are
  handled.
- **`Stop` returns nil for a container that is already gone.** v0.2.9's
  `KillMongoContainer` returned the daemon's "not found" error. If you read
  that error to learn whether a container still existed, ask the daemon
  instead; stopping a container twice is safe.
- **Daemon errors keep the whole message.** When a container fails to start
  because its port or its name is already in use, the error now says so.
  Before, that part was cut off.

### Added

- **Package `mongod`** starts a MongoDB container and gives you its
  connection string, with no MongoDB driver imported: `mongod.Start(ctx)`,
  then `c.URI()`.
- `mongod.DefaultOptions()` chooses the image, a replica set, credentials and
  more; the package documentation lists them.
- `mongod.PullImage` fetches the image before the tests need it, and does
  nothing if the daemon already has it.
- When `Start` and `PullImage` calls in one process need the same missing
  image, they share one pull. A pull that fails because a registry is
  briefly down is tried again for up to about a minute.
- `dockerclient.ErrRateLimited` matches a pull that a registry refused
  because of its rate limit, from `dockerapi` or `mobyclient`.
- `mobyclient` reports a daemon's 429, 500 and 503 as a `StatusError` with
  that code, as `dockerapi` does, so they no longer match
  `ErrDaemonResponse`. Match `ErrRateLimited`, or read `StatusError.StatusCode`.
- `Options.WithPullTimeout` bounds a pull of a missing image, ten minutes by
  default, separately from the wait for mongod.
- When a container exits before mongod is ready, the error now ends with the
  last of what mongod wrote, which usually says why.
  `ContainerExitedError.Output` holds the same text.
- If Ctrl-C stops a test run and a container cannot be removed, the logger
  given to `mongod.WithLogger` for that container now gets an Error record
  naming it. The report on stderr is unchanged.
