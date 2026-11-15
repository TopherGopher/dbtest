---
title: Quickstart
description: A complete test that starts MongoDB in a container, writes a document and reads it back.
status: current
---

# Quickstart

One complete test: it starts a MongoDB server, inserts a document, reads it
back and removes the server.

## The whole test

```go title="hero_test.go"
package heroes

import (
    "testing"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
    "github.com/tophergopher/mongotest"
    "go.mongodb.org/mongo-driver/bson"
    "go.mongodb.org/mongo-driver/bson/primitive"
)

type hero struct {
    ID   primitive.ObjectID `bson:"_id"`
    Name string             `bson:"name"`
    City string             `bson:"city"`
}

func TestInsertAndFind(t *testing.T) {
    coll := mongotest.Run(t).Database("heroes").Collection("roster")

    want := hero{ID: primitive.NewObjectID(), Name: "Diana", City: "Themyscira"}
    _, err := coll.InsertOne(t.Context(), want)
    require.NoError(t, err, "inserting into a fresh collection must succeed")

    var got hero
    err = coll.FindOne(t.Context(), bson.M{"name": "Diana"}).Decode(&got)
    require.NoError(t, err, "the document just inserted must be found by name")
    assert.Equal(t, want, got, "the document must round-trip unchanged")
}
```

Fetch the dependencies and run the test. It needs Go 1.27 or newer and a
running Docker daemon:

```sh
go get github.com/tophergopher/mongotest github.com/stretchr/testify
go test -run TestInsertAndFind -v .
```

## What each part does

**`mongotest.Run(t)`** starts a container from the official `mongo` image,
connects a client to it, and removes the container when the test ends, pass
or fail. Without a Docker daemon, the test fails; it is not skipped.

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
