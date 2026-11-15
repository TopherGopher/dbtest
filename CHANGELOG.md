# Changelog

What changed in each release, for someone upgrading: what is different, and
what to do if it affects you.

## Unreleased

### The root package is rebuilt on `mongod`

- **New entry points.** `mongotest.Run(t)` starts a container, connects a
  client of the official driver (v1.17), and removes the container when the
  test ends. `RunWithOptions`, `Start`, `StartWithOptions` and `Attach` cover
  options, code outside a test, and a server you already run. Each returns an
  `*Instance`, which embeds `*mongo.Client`.
- **The earlier names still compile.** `TestConnection` is now `Instance`,
  and `NewTestConnection`, `NewReplicaSetContainer`, `MongoClientWithContainer`,
  `KillMongoContainer`, `MongoContainerID`, `RunMongoScriptOnContainer`,
  `ExecCommandInMongoContainer`, `GetAvailablePort`, `ReapRunningContainers`
  and `GenerateCARoot` are thin wrappers. The reference lists what new code
  uses instead.
- **easymongo is gone.** `TestConnection` embedded an easymongo connection;
  `Instance` embeds the driver's client. Calls through easymongo stop
  compiling: `conn.D("db").C("coll")` becomes `inst.Database("db").Collection("coll")`,
  `conn.MongoDriverClient()` becomes `inst.Client`, and `conn.MongoURI()`
  becomes `inst.URI()`. `EasyMongoWithContainer` is removed; use `Run`.
- **`DatabaseExporter` is removed.** It moves to its own module,
  `github.com/tophergopher/mongotest/exporter`, which writes to any
  `io.Writer`; its guide has a table for moving over. `ErrNotImplemented` and
  `MongoTestError` go with it.
- **The default image is `mongo:8.0`,** not `mongo:latest`, and replica sets
  and scripts use `mongosh`, so they work on every current image. A replica
  set takes the name you give it.
- **Scripts return standard output, trimmed.** On a failed script or command,
  the earlier methods return what the process wrote and an error matching the
  new `ErrCommandFailed`.
- **The root package writes nothing to standard output or standard error.**
  It used to log each start through logrus.
- **Errors.** A failed connection matches the new `ErrNotConnected`.
  `ErrFailedToConnectToDockerDaemon` and `ErrMongoContainerAlreadyRunning` are
  now `dockerclient.ErrConnectionFailed` and `dockerclient.ErrConflict`, which
  `errors.Is` matches against what a start returns.
- **A failed start returns no instance and leaves no container.** There is
  nothing to stop.
- **The root module needs only the driver and testify.** The Docker client,
  logrus, easymongo and mongo-tools are no longer dependencies.

### Changed

- **Ctrl-C now ends the process.** On SIGINT or SIGTERM mongotest removes the
  containers it started and then lets the signal end the process, as it would
  have without mongotest. Before, the containers were removed but the test
  binary kept running. Nothing to change, unless a long-running program of
  yours relied on surviving the signal; issue
  [#67](https://github.com/TopherGopher/mongotest/issues/67) adds a way to
  opt out.
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
- `mongod.PullImage` fetches the image before the tests need it, and leaves
  an image the daemon already has alone. Call it once in `TestMain`, so tests
  that start in parallel do not each find the image missing.
- `Options.WithPullTimeout` bounds a pull of a missing image, ten minutes by
  default, separately from the wait for mongod.
