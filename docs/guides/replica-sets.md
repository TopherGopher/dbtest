---
title: Replica sets
description: Starting a single-member replica set for transactions and change streams.
status: current
---

# Replica sets

Transactions, change streams and retryable writes need a replica set. A
single member is enough for tests.

```go
func TestTransfer(t *testing.T) {
	--8<-- "mongotest/mongotest_test.go:transaction"
}
```

`WithReplicaSet("rs0")` starts `mongod` as the only member of a replica set
named `rs0`, and sets the set up. `Run` returns once the member accepts
writes, so the first transaction needs no retry.

The URI includes `directConnection=true`. Without it, the driver would switch
to the member's own address, `localhost` inside the container, which the test
cannot reach. Code that builds its own client should use `inst.URI()`
unchanged.

The [Atlas Local image](../reference/mongod.md#which-image) already runs as a
replica set. Do not add `WithReplicaSet` with it; the start fails with
`mongod.ErrUnsupportedForImage`.

## Change streams

With a replica set running, change streams work as they do in production:

```go
func TestEvents(t *testing.T) {
	--8<-- "mongotest/guides_test.go:change-stream"
}
```
