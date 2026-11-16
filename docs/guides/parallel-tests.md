---
title: Table-driven and parallel tests
description: Choosing between a container per case and a container per table, and running tests in parallel without collisions.
status: current
---

# Table-driven and parallel tests

Every container gets its own port, chosen by Docker, so tests that each
start one can run in parallel. The choice is how many containers to start.

## A container per test, in parallel

The simplest shape. Each test is fully isolated and `t.Parallel` is safe.

```go
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
    inst := mongotest.Run(t)

    cases := []struct {
        name   string
        seed   []hero
        filter bson.M
        want   int
    }{
        {"by city", []hero{{Name: "Diana", City: "Themyscira"}}, bson.M{"city": "Themyscira"}, 1},
        {"no match", []hero{{Name: "Diana", City: "Themyscira"}}, bson.M{"city": "Gotham"}, 0},
        {"empty collection", nil, bson.M{}, 0},
    }

    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            t.Parallel()
            // A database name derived from the subtest name is unique and
            // readable in the server logs. Replace characters mongo rejects.
            db := inst.Database(dbNameFor(t))
            // t.Context() has ended by the time cleanups run.
            t.Cleanup(func() { _ = db.Drop(context.Background()) })

            coll := db.Collection("roster")
            for _, h := range tc.seed {
                _, err := coll.InsertOne(t.Context(), h)
                require.NoError(t, err, "seeding %q must succeed", tc.name)
            }

            n, err := coll.CountDocuments(t.Context(), tc.filter)
            require.NoError(t, err, "the query must run")
            assert.EqualValues(t, tc.want, n, "the filter must match the seeded documents")
        })
    }
}

func dbNameFor(t *testing.T) string {
    return strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
}
```

!!! note "Why cleanup order matters here"
    `Run` registered the container's removal on the outer test, so it runs
    after every subtest, and its `Drop`, has finished.

## Choosing

| | Container per test | Container per table |
| --- | --- | --- |
| Isolation | Complete, including server state such as indexes and server parameters | Per database. Server-wide state is shared across cases |
| Start-up cost | One container per test | One container per table |
| `t.Parallel` | Safe at every level | Safe for subtests; the container outlives them |
| A crashed `mongod` | Affects one test | Fails the whole table |
| Good for | Integration tests of a feature | Many small query or codec cases |

## Running packages in parallel

`go test ./...` runs packages concurrently, and each package's test binary
starts its own containers. If the machine runs short of memory, lower `-p` or
`-parallel` rather than sharing containers between packages. Call
`mongod.PullImage` in `TestMain`, so parallel tests do not each find the image
missing; see [Which image](../reference/mongod.md#which-image).
