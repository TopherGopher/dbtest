---
title: Table-driven and parallel tests
description: Choosing between a container per test, per table and per package, and running tests in parallel without collisions.
status: current
---

# Table-driven and parallel tests

Every container gets its own port, chosen by Docker, so tests that each
start one can run in parallel. The choice is how many containers to start.

## A container per test, in parallel

The simplest shape. Each test is fully isolated and `t.Parallel` is safe.
A test binary starts at most 8 containers per CPU at once, and further starts
wait their turn; see
[Many containers at once](../reference/mongod.md#many-containers-at-once).

```go title="Illustration, not compiled"
func TestFindByCity(t *testing.T) {
    t.Parallel()
    inst := mongotest.Run(t)
    // ...
}

func TestFindByName(t *testing.T) {
    t.Parallel()
    inst := mongotest.Run(t)
    // ...
}
```

## A container per table, a database per case

When a table has many small cases, start one container for the table and give
each case its own database. Cases still cannot see each other's data, and the
whole table pays one start-up.

```go
func TestRosterQueries(t *testing.T) {
	--8<-- "mongotest/example_test.go:table"
}
```

`DatabaseFor` builds the database's name from the case's name, with anything
but letters, digits, `-` and `_` replaced, cut to fit the server's limit. The
name ends in a random part, so no two cases share one.

!!! note "Why cleanup order matters here"
    `Run` registered the container's removal on the outer test, so it runs
    after every subtest has finished and its database has been dropped.

## A container per package

When a package has many small tests, start one container for all of them in
`TestMain`, and give each test its own database.

```go title="Illustration, not compiled"
var server *mongotest.Instance

func TestMain(m *testing.M) {
    ctx := context.Background()
    var err error
    if server, err = mongotest.Start(ctx); err != nil {
        log.Fatal(err)
    }
    defer server.Stop(ctx)
    m.Run()
}

func TestFindByCity(t *testing.T) {
    t.Parallel()
    roster := server.DatabaseFor(t).Collection("roster")
    // ...
}
```

Let `TestMain` return, as here. The testing package then exits with the
result of `m.Run`. Calling `os.Exit` yourself would skip the deferred `Stop`
and leave the container running.

A test that panics, or a run that `go test -timeout` ends, also exits without
running the deferred `Stop`, so the container is left running.
[The label](cleanup.md#3-the-label) finds it.

## Choosing

| | Container per test | Container per table | Container per package |
| --- | --- | --- | --- |
| Isolation | Complete, including server state such as indexes and server parameters | Per database. Server-wide state is shared across cases | Per database. Server-wide state is shared across the package |
| Start-up cost | One container per test | One container per table | One container per package |
| `t.Parallel` | Safe at every level | Safe for subtests; the container outlives them | Safe at every level; the container outlives every test |
| A crashed `mongod` | Affects one test | Fails the whole table | Fails the rest of the package |
| Good for | Integration tests of a feature | Many small query or codec cases | Many small tests that need no server state of their own |

## Running packages in parallel

`go test ./...` runs packages concurrently, and each package's test binary
starts its own containers, under a limit of its own. If the machine runs
short of memory, lower `-p` or `-parallel` rather than sharing containers
between packages. Parallel tests in one package that need a missing image
share one pull. Separate packages are separate processes, so each pulls a
missing image on its own; see
[Which image](../reference/mongod.md#which-image).
