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

It needs Go 1.26 or newer, and Docker or Podman. Without one, the test fails;
it is not skipped.

```go
func TestOrders(t *testing.T) {
    orders := mongotest.Run(t).Database("shop").Collection("orders")
    _, err := orders.InsertOne(t.Context(), bson.M{"item": "book"})
    require.NoError(t, err)
}
```

Upgrading from `github.com/tophergopher/mongotest`? Import
`github.com/tophergopher/dbtest/mongotest` instead. The earlier function names
still exist, but code that used the easymongo connection must change;
[CHANGELOG.md](CHANGELOG.md) shows how.
For Podman, see the [Podman guide](https://tophergopher.github.io/dbtest/guides/podman/).

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
