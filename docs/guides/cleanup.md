---
title: Cleanup and reaping
description: Everything that removes a dbtest container, in the order it is tried, and how to remove stray ones.
status: current
---

# Cleanup and reaping

A container that outlives its test keeps a port and memory. Three things
remove containers. The first is part of every start; the others cover a test
that never reaches it.

## 1. When the test ends

`Run` removes the container when the test ends, pass or fail. A caller of
`Start` calls `Stop`, which removes the container and its anonymous volumes.
`Stop` still removes it when the context it is given has already ended, and
it can be called again, including after a `Stop` that failed.

With `Start`, prefer `t.Cleanup` over `defer`:

| | `t.Cleanup` | `defer` |
| --- | --- | --- |
| Runs after `t.Fatal` | yes | yes |
| Runs after parallel subtests finish | yes | no, runs when the parent function returns |
| Runs after a panic in the test | yes, the testing package recovers first | yes |
| Runs when the process gets a signal | no | no |

The last row is why the others exist.

## 2. The signal handler

The first container dbtest starts installs a handler for `SIGINT` and
`SIGTERM`. When one arrives, every container started in this process and not
yet removed is force-removed, and the signal is raised again so the process
still exits. This covers ++ctrl+c++ during a run. A second ++ctrl+c++ while
the containers are being removed stops the process at once.

The process exits even if the program installed a handler of its own, so its
own shutdown never runs. A program that handles these signals itself calls
`mongod.DisableSignalHandler()` before the first container starts, then
removes the containers from its shutdown with `ReapRunningContainers`.

You can remove the same containers yourself, for instance from a `TestMain`.
`mongod.ReapRunningContainers` returns an error naming any container it could
not remove:

```go
func TestMain(m *testing.M) {
    m.Run()
    if err := mongod.ReapRunningContainers(context.Background()); err != nil {
        log.Print(err)
    }
}
```

## 3. The label

Every container is created with the label `mongotest=regression`. Whatever
else fails, this lets you find and remove stray containers with one command:

```sh
docker rm -fv $(docker ps -aq --filter label=mongotest=regression)
```

`-v` also removes each container's anonymous volumes, which otherwise
stay on disk. With nothing to remove, `docker rm` reports a missing argument,
which is harmless. To look before removing:

```sh
docker ps -a --filter label=mongotest=regression \
  --format 'table {{.ID}}\t{{.Names}}\t{{.Status}}\t{{.Ports}}'
```

Add the removal command to a CI job's always-run step, so containers left by
a crashed run do not hold ports and memory the next run needs:

```yaml
- name: Remove stray dbtest containers
  if: always()
  run: docker ps -aq --filter label=mongotest=regression | xargs -r docker rm --force --volumes
```

## What cannot be cleaned up

If the Go process is killed with `SIGKILL`, or the machine loses power, none
of the above runs. The containers stay until the command under
[the label](#3-the-label) removes them.

## Checking for leaks in a suite

A quick way to prove a suite is clean is to count before and after:

```sh
before=$(docker ps -aq --filter label=mongotest=regression | wc -l)
go test ./...
after=$(docker ps -aq --filter label=mongotest=regression | wc -l)
test "$before" -eq "$after" || echo "leaked $((after - before)) container(s)"
```
