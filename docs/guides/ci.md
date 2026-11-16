---
title: Continuous integration
description: Running a mongotest suite under GitHub Actions and other hosted runners.
status: changing
tracks: ["#52", "#46"]
---

# Continuous integration

mongotest needs a Docker-compatible daemon reachable from the test process.
Most hosted Linux runners have one. This page covers the shapes that work.

## GitHub Actions

`ubuntu-latest` ships with Docker running and the current user in the
`docker` group, so the test job needs nothing special:

```yaml title=".github/workflows/go.yml"
name: Go

on:
  push:
    branches: [ main ]
  pull_request:

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod

      - name: Test
        run: go test -race -count=1 ./...

      - name: Remove stray mongotest containers
        if: always()
        run: docker ps -aq --filter label=mongotest=regression | xargs -r docker rm --force --volumes
```

### Using a service container instead

If you would rather not start containers from inside the test, run MongoDB as
a job service and use `Attach`:

```yaml
jobs:
  test:
    runs-on: ubuntu-latest
    services:
      mongo:
        image: mongo:8
        ports:
          - 27017:27017
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - run: go test -race -count=1 ./...
        env:
          MONGODB_URI: mongodb://localhost:27017
```

The trade-off is isolation: every test shares one server. Give each test its
own database name and drop it in cleanup, as shown in
[Starting a server](test-connection.md#a-server-you-already-run).

To choose at run time:

```go
func startMongo(t *testing.T) *mongotest.Instance {
    t.Helper()
    uri := os.Getenv("MONGODB_URI")
    if uri == "" {
        return mongotest.Run(t)
    }
    inst, err := mongotest.Attach(t.Context(), uri)
    require.NoError(t, err, "the MongoDB service must be reachable")
    t.Cleanup(func() { _ = inst.Stop(context.Background()) })
    return inst
}
```

## Other runners

=== "GitLab CI"

    Use a runner with the Docker socket mounted, or the shell executor on a
    host that runs Docker. Docker-in-Docker (`docker:dind` as a service)
    should work: mongotest dials the daemon's host, as
    [Remote and nested daemons](remote-daemons.md) explains. Proving it in CI
    is [#52](https://github.com/TopherGopher/mongotest/issues/52).

    ```yaml
    test:
      image: golang:1.27
      tags: [docker-socket]   # a runner that mounts /var/run/docker.sock
      script:
        - go test -race -count=1 ./...
    ```

=== "Self-hosted"

    Anything with a daemon the test process can reach. Set `DOCKER_HOST` if
    the socket is not at the default path, and make sure the runner user is in
    the `docker` group.

=== "macOS and Windows runners"

    GitHub's macOS runners do not ship Docker. Windows runners run Windows
    containers by default and cannot run the Linux `mongo` image. Run the
    mongotest suite on Linux and restrict other platforms to tests that use
    `Attach` against a service, or skip them.

When the tests run in a container, or the daemon is on another machine,
[Remote and nested daemons](remote-daemons.md) says how the address is
worked out.

## Tips for stable CI runs

- **Pin the image.** The default is `mongo:8.0`, which still moves between
  patch releases. Set `MONGOTEST_IMAGE` to a version, such as `8.0.4`, in the
  job's environment to pin one.
- **Pull the image once.** Call `mongod.PullImage` in `TestMain`, so the first
  test is not also a download; see
  [Which image](../reference/mongod.md#which-image).

- **Use `-count=1`.** Integration tests should not be served from the test
  cache.
