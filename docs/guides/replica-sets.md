---
title: Replica sets
description: Starting a single-member replica set for transactions and change streams.
status: current
---

# Replica sets

Transactions, change streams and some retryable write behaviour need a replica
set. A single member is enough for tests.

```go
inst := mongotest.RunWithOptions(t, mongotest.DefaultOptions().WithReplicaSet("rs0"))

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

The start runs `mongod` as the only member of a set with that name, initiates
it, and returns once it accepts writes, so the first transaction needs no
retry. The URI carries `directConnection=true`, which keeps the driver off the
`localhost` address the member advertises, an address that works only inside
the container.

The earlier `NewReplicaSetContainer(name)` still works, and does the same.

Atlas Local runs a replica set of its own: use it without `WithReplicaSet`,
which it refuses with `mongod.ErrUnsupportedForImage`.

## Change streams

With a replica set running, change streams work as they do in production:

```go
coll := inst.Database("app").Collection("events")
stream, err := coll.Watch(ctx, mongo.Pipeline{})
require.NoError(t, err)
defer stream.Close(ctx)

_, err = coll.InsertOne(ctx, bson.M{"kind": "created"})
require.NoError(t, err)

require.True(t, stream.Next(ctx), "the insert must appear on the change stream")
```
