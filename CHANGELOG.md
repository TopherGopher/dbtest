# Changelog

What changed in each release, for someone upgrading: what is different, and
what to do if it affects you.

## Unreleased

### The root package is rebuilt on the new `mongod` package

- **New entry points.** `mongotest.Run(t)` starts a container, connects a
  client of the official MongoDB Go driver (v1, at v1.17.10 or later), and
  removes the container when the test ends. `RunWithOptions`, `Start`, `StartWithOptions` and `Attach` cover
  options, code outside a test, and a server you already run. Each returns an
  `*Instance`, which embeds `*mongo.Client`.
- **The earlier names still compile.** `TestConnection` is now `Instance`,
  and `NewTestConnection`, `NewReplicaSetContainer`, `MongoClientWithContainer`,
  `KillMongoContainer`, `MongoContainerID`, `RunMongoScriptOnContainer`,
  `ExecCommandInMongoContainer`, `GetAvailablePort`, `ReapRunningContainers`
  and `GenerateCARoot` still compile: most are thin wrappers, and
  `GetAvailablePort` and `GenerateCARoot` are kept as they were. The
  [reference](https://tophergopher.github.io/mongotest/reference/#earlier-names)
  lists what new code uses instead.
- **easymongo is gone.** `TestConnection` embedded an easymongo connection;
  `Instance` embeds the driver's client. Calls through easymongo stop
  compiling: `conn.D("db").C("coll")` becomes `inst.Database("db").Collection("coll")`,
  `conn.MongoDriverClient()` becomes `inst.Client`, and `conn.MongoURI()`
  becomes `inst.URI()`. `EasyMongoWithContainer` is removed; use `Run`.
- **`DatabaseExporter` is removed.** It moves to its own module,
  `github.com/tophergopher/mongotest/exporter`, which writes to any
  `io.Writer`; its [guide](https://tophergopher.github.io/mongotest/guides/exporting-data/#moving-from-databaseexporter)
  has a table for migrating. `ErrNotImplemented`, `MongoTestError` and `NewMongoTestError` go with
  it.
- **The default image is `mongo:8.0`,** not `mongo:latest`. Set
  `MONGOTEST_IMAGE` or `WithImage` to run another.
- **Replica sets and scripts use `mongosh`,** so they work on every current
  image. Before, they needed the legacy `mongo` shell, which images from
  MongoDB 6.0 do not have.
- **A replica set takes the name you give it.** Before, only `"cf"` worked.
- **Scripts return standard output, trimmed.** Before, they returned standard
  output and standard error together. On a failed script or command, the
  earlier methods return what the process wrote and an error matching the new
  `ErrCommandFailed`.
- **The root package writes nothing to standard output or standard error.**
  It used to log each start through logrus.
- **Errors.** A failed connection matches the new `ErrNotConnected`.
  `ErrFailedToConnectToDockerDaemon` and `ErrMongoContainerAlreadyRunning`
  keep their names, and are now the same values as
  `dockerclient.ErrConnectionFailed` and `dockerclient.ErrConflict`. Check
  for them with `errors.Is` on the error a start returns.
- **A failed start returns no instance and leaves no container.** A cleanup
  call on the error path is no longer needed, and is harmless.
- **The root module needs only the driver and testify.** The Docker client,
  logrus, easymongo and mongo-tools are no longer dependencies.

### Changed

- **Ctrl-C now ends the process.** On SIGINT or SIGTERM mongotest removes the
  containers it started and then lets the signal end the process, as it would
  have without mongotest. Before, the containers were removed but the test
  binary kept running. Nothing to change, unless a long-running program of
  yours relied on surviving the signal; a way to opt out is planned in
  [#67](https://github.com/TopherGopher/mongotest/issues/67).
- **The signal handler is installed when the first container is created, not
  when the package is imported.** A program that imports mongotest and starts
  no container keeps its own handling of Ctrl-C. Only SIGINT and SIGTERM are
  handled.
- **`KillMongoContainer` returns nil for a container that is already gone.**
  It used to return the daemon's "not found" error. If you read that error to
  learn whether a container still existed, ask the daemon instead; killing a
  container twice is now safe.

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
  because of its rate limit, from `dockerapi` or `mobyclient`. A rate limit
  is reported at once, not retried, and for the official image the error
  suggests `mongod.ImagePublicECR`.
- `mobyclient` reports a daemon's 429, 500 and 503 as a `StatusError` with
  that code, as `dockerapi` does, so they no longer match
  `ErrDaemonResponse`. Match `ErrRateLimited`, or read `StatusError.StatusCode`.
- `Options.WithPullTimeout` bounds a pull of a missing image, ten minutes by
  default, separately from the wait for mongod.
