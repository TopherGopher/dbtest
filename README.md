# dbtest

[![Documentation](https://img.shields.io/badge/docs-tophergopher.github.io%2Fdbtest-0e8f7a)](https://tophergopher.github.io/dbtest/)
[![Go Reference](https://pkg.go.dev/badge/github.com/tophergopher/dbtest.svg)](https://pkg.go.dev/github.com/tophergopher/dbtest)
[![Coverage](https://coveralls.io/repos/github/TopherGopher/dbtest/badge.svg?branch=main)](https://coveralls.io/github/TopherGopher/dbtest?branch=main)

dbtest starts a real database server in a Docker container for a Go test.
For MongoDB it hands the test a connected client of the official MongoDB Go
driver, and removes the container when the test ends. For PostgreSQL, the
`postgres` package hands it a connection string. A connected client is
planned.

```sh
go get github.com/tophergopher/dbtest/mongotest    # if you use go.mongodb.org/mongo-driver
go get github.com/tophergopher/dbtest/mongotestv2  # if you use go.mongodb.org/mongo-driver/v2
```

Starting a new project with no driver yet? Use `mongotestv2`: driver v1 is
in maintenance.

dbtest needs Go 1.26 or newer, and a running Docker or Podman daemon. Without a
daemon, the test fails rather than skipping. For Podman, see the
[Podman guide](https://tophergopher.github.io/dbtest/guides/podman/).

```go
import (
    "testing"

    "github.com/stretchr/testify/require"
    "github.com/tophergopher/dbtest/mongotest"
    "go.mongodb.org/mongo-driver/bson"
)

func TestOrders(t *testing.T) {
    orders := mongotest.Run(t).Database("shop").Collection("orders")
    _, err := orders.InsertOne(t.Context(), bson.M{"item": "book"})
    require.NoError(t, err)
}
```

With driver v2, import `github.com/tophergopher/dbtest/mongotestv2` and
`go.mongodb.org/mongo-driver/v2/bson`, and write `mongotestv2.Run(t)`.
Nothing else changes. Do not leave `bson` on v1: a v1 `bson` value passed
to a v2 client compiles, but at run time it is stored as the wrong type or
refused.

Upgrading from `github.com/tophergopher/mongotest`? It used driver v1, so
import `github.com/tophergopher/dbtest/mongotest` instead. The names the old
module exported at v0.2.9 are gone, and
[CHANGELOG.md](CHANGELOG.md#the-mongotest-package-is-rebuilt-on-the-new-mongod-package)
lists what replaces each. If you set `MONGOTEST_HOST_IP`, rename it to
`DBTEST_HOST_IP`: the old name is ignored. `MONGOTEST_IMAGE` and
`MONGOTEST_START_TIMEOUT` keep their names.

## PostgreSQL

The `postgres` package starts PostgreSQL and returns a connection string
for any driver. It adds no dependency to your module. Install it with:

```sh
go get github.com/tophergopher/dbtest
```

`Start` does not take `t`, so stop the container yourself, as below.

```go
c, err := postgres.Start(t.Context()) // import "github.com/tophergopher/dbtest/postgres"
require.NoError(t, err)
t.Cleanup(func() { c.Stop(context.Background()) })

db, err := sql.Open("pgx", c.URI()) // or pgxpool.New(ctx, c.URI())
```

See [The postgres package](https://tophergopher.github.io/dbtest/reference/postgres/).

## Cleaning up stray containers

Every container dbtest starts carries the label `mongotest=regression`. If
a killed test run leaves containers behind, this command removes them and
their unnamed volumes:

```shell
docker ps -aq --filter label=mongotest=regression | xargs -r docker rm -fv
```

The command keeps volumes you named with `mongod.Options.WithNamedVolume`.
Remove those with `docker volume rm <name>`.

## Documentation

The documentation site lives in [`docs/`](docs/) and is built with
[Zensical](https://zensical.org/). It is published to GitHub Pages from `main` by
[`.github/workflows/docs.yml`](.github/workflows/docs.yml). New usage
patterns go in `docs/guides/`.
[Writing a new page](https://tophergopher.github.io/dbtest/reference/writing-docs/)
says how to preview the site locally and what a page must hold.
