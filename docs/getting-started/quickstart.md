---
title: Quickstart
description: A complete test that starts MongoDB in a container, writes a document and reads it back.
status: current
---

# Quickstart

One complete test: it starts a MongoDB server, inserts a document, reads it
back and removes the server.

## The whole test

Pick the tab for the driver your module requires in `go.mod`:
`go.mongodb.org/mongo-driver` is v1, and `go.mongodb.org/mongo-driver/v2` is
v2. If it requires neither, pick v2: driver v1 is in maintenance.

=== "Driver v1"

    ```go title="hero_test.go"
    --8<-- "mongotest/internal/heroes/hero_test.go"
    ```

=== "Driver v2"

    ```go title="hero_test.go"
    --8<-- "mongotestv2/internal/heroes/hero_test.go"
    ```

    Import `bson` from driver v2 as well. A v1 `bson` value passed to a v2
    client compiles, but at run time it is stored as the wrong type or
    refused.

Fetch the dependencies and run the test. It needs Go 1.26 or newer and a
running Docker or Podman daemon:

=== "Driver v1"

    ```sh
    go get github.com/tophergopher/dbtest/mongotest github.com/stretchr/testify
    go test -run TestInsertAndFind -v .
    ```

=== "Driver v2"

    ```sh
    go get github.com/tophergopher/dbtest/mongotestv2 github.com/stretchr/testify
    go test -run TestInsertAndFind -v .
    ```

## What each part does

**`mongotest.Run(t)`**, or `mongotestv2.Run(t)`, starts a container from the
official `mongo` image, connects a client to it, and removes the container
when the test ends, pass or fail. Without a Docker daemon, the test fails; it is not skipped.

**`Database` and `Collection`** are the official driver's own methods. `Run`
returns an `*Instance`, which embeds the driver's `*mongo.Client`, so every
client method works on it.

**The insert and the find** are ordinary driver calls. The database is empty
when the test starts, so the test can assert on exact contents without
clearing anything first.

[How it works](how-it-works.md) follows the container from creation to
removal.

## Where next

- Need the same server across a table of cases? See
  [Table-driven and parallel tests](../guides/parallel-tests.md).
- Testing transactions or change streams? Those need a
  [replica set](../guides/replica-sets.md).
- Running in GitHub Actions? The [CI guide](../guides/ci.md) has a working
  workflow.
