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
whole table pays one start-up. Here `hero` is a struct with `Name` and
`City` fields:

```go
func TestRosterQueries(t *testing.T) {
	--8<-- "mongotest/guides_test.go:table"
}
```

`DatabaseFor` names the database after the case, with a random suffix, so
no two cases share one.

!!! note "The container outlives the subtests"
    Parallel subtests run after the outer test function returns. The
    container is still there, because `Run` registered its removal on the
    outer test, and that cleanup waits for every subtest.

## A container per package

When a package has many small tests, start one container for all of them in
`TestMain`, and give each test its own database.

```go
--8<-- "mongotest/internal/sharedserver/sharedserver_test.go:test-main"
```

Each test then takes its own database from the shared server:

```go
func TestOrders(t *testing.T) {
	--8<-- "mongotest/internal/sharedserver/sharedserver_test.go:database-for"
}
```

Let `TestMain` return, as here. The testing package then exits with the
result of `m.Run`. Calling `os.Exit` yourself would skip the deferred `Stop`
and leave the container running.

A test that panics, or a run that `go test -timeout` ends, also exits without
running the deferred `Stop`, so the container is left running. Every dbtest
container carries the label `mongotest=regression`, and
[The label](cleanup.md#3-the-label) gives the command that removes them.

## Choosing

| | Container per test | Container per table | Container per package |
| --- | --- | --- | --- |
| Isolation | Complete, including server state such as indexes and server parameters | Per database. Server-wide state is shared across cases | Per database. Server-wide state is shared across the package |
| Start-up cost | One container per test | One container per table | One container per package |
| `t.Parallel` | Safe at every level | Safe at every level | Safe at every level |
| A crashed `mongod` | Affects one test | Fails the whole table | Fails the rest of the package |
| Good for | Integration tests of a feature | Many small query or codec cases | Many small tests that need no server state of their own |

## Running packages in parallel

`go test ./...` runs packages concurrently, and each package's test binary
starts its own containers, under a limit of its own. If the machine runs
short of memory, lower `-p` or `-parallel` rather than sharing containers
between packages.

Parallel tests in one package that need a missing image share one pull.
Separate packages are separate processes, so the first `go test ./...` on a
machine may pull a missing image once per package. To pull it before any
test starts, see
[Which image](../reference/mongod.md#which-image).
