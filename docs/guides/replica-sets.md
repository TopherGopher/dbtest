---
title: Replica sets
description: Starting a single-member replica set for transactions and change streams.
status: current
---

# Replica sets

Transactions, change streams and retryable writes need a replica set. A
single member is enough for tests.

```go
inst := mongotest.RunWithOptions(t, mongotest.DefaultOptions().WithReplicaSet("rs0"))

ctx := t.Context()
session, err := inst.StartSession()
require.NoError(t, err)
defer session.EndSession(ctx)

_, err = session.WithTransaction(ctx, func(sc mongo.SessionContext) (any, error) {
    coll := inst.Database("bank").Collection("accounts")
    if _, err := coll.InsertOne(sc, bson.M{"owner": "a", "balance": 100}); err != nil {
        return nil, err
    }
    _, err := coll.InsertOne(sc, bson.M{"owner": "b", "balance": 0})
    return nil, err
})
require.NoError(t, err, "a transaction must commit on a single-member replica set")
```

`WithReplicaSet("rs0")` starts `mongod` as the only member of a replica set
named `rs0`, and sets the set up. `Run` returns once the member accepts
writes, so the first transaction needs no retry.

The URI includes `directConnection=true`. Without it, the driver would switch
to the member's own address, `localhost` inside the container, which the test
cannot reach. Code that builds its own client should use `inst.URI()`
unchanged.

The earlier `NewReplicaSetContainer(name)` still works, and does the same.

The [Atlas Local image](../reference/mongod.md#which-image) already runs as a
replica set. Do not add `WithReplicaSet` with it; the start fails with
`mongod.ErrUnsupportedForImage`.

## Change streams

With a replica set running, change streams work as they do in production:

```go
// A deadline keeps the test from hanging if no event arrives.
ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
defer cancel()

coll := inst.Database("app").Collection("events")
stream, err := coll.Watch(ctx, mongo.Pipeline{})
require.NoError(t, err)
defer stream.Close(ctx)

_, err = coll.InsertOne(ctx, bson.M{"kind": "created"})
require.NoError(t, err)

require.True(t, stream.Next(ctx), "the insert must appear on the change stream")
```
