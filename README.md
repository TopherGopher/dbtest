# dbtest

[![Documentation](https://img.shields.io/badge/docs-tophergopher.github.io%2Fdbtest-0e8f7a)](https://tophergopher.github.io/dbtest/)
[![Go Reference](https://pkg.go.dev/badge/github.com/tophergopher/dbtest.svg)](https://pkg.go.dev/github.com/tophergopher/dbtest)
[![Coverage](https://coveralls.io/repos/github/TopherGopher/dbtest/badge.svg?branch=main)](https://coveralls.io/github/TopherGopher/dbtest?branch=main)

dbtest starts a real MongoDB server in a Docker container for a Go test,
hands the test a connected client of the official MongoDB Go driver (v1), and
removes the container when the test ends.

```sh
go get github.com/tophergopher/dbtest/mongotest
```

It needs Go 1.26 or newer, and a running Docker or Podman daemon. Without a
daemon, the test fails rather than skipping. For Podman, see the
[Podman guide](https://tophergopher.github.io/dbtest/guides/podman/).

```go
func TestOrders(t *testing.T) {
    orders := mongotest.Run(t).Database("shop").Collection("orders")
    _, err := orders.InsertOne(t.Context(), bson.M{"item": "book"})
    require.NoError(t, err)
}
```

Upgrading from `github.com/tophergopher/mongotest`? Import
`github.com/tophergopher/dbtest/mongotest` instead. The earlier function names
still exist. v0.2.9's `TestConnection` embedded a connection from easymongo,
a separate MongoDB library, and the new `Instance` embeds the driver's client
instead, so calls that went through easymongo change. If you set
`MONGOTEST_HOST_IP`, rename it `DBTEST_HOST_IP`: the old name is ignored.
[CHANGELOG.md](CHANGELOG.md) shows each change.

## Cleaning up stray containers

Every container dbtest starts carries the label `mongotest=regression`. If
a killed test run leaves containers behind, this command removes them and
their volumes:

```shell
docker rm -fv $(docker ps -aq --filter label=mongotest=regression)
```

## Documentation

The documentation site lives in [`docs/`](docs/) and is built with MkDocs
Material. It is published to GitHub Pages from `main` by
[`.github/workflows/docs.yml`](.github/workflows/docs.yml). To preview it
locally:

```shell
pip install -r docs/requirements.txt
mkdocs serve
```

New usage patterns go in `docs/guides/`; see
[Writing a new page](https://tophergopher.github.io/dbtest/reference/writing-docs/).
