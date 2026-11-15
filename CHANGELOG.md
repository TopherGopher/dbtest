# Changelog

What changed in each release, for someone upgrading: what is different, and
what to do if it affects you.

## Unreleased

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
