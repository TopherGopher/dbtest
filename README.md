# mongotest

[![Documentation](https://img.shields.io/badge/docs-tophergopher.github.io%2Fmongotest-0e8f7a)](https://tophergopher.github.io/mongotest/)
[![Go Reference](https://pkg.go.dev/badge/github.com/tophergopher/mongotest.svg)](https://pkg.go.dev/github.com/tophergopher/mongotest)
[![Coverage](https://coveralls.io/repos/github/TopherGopher/mongotest/badge.svg?branch=main)](https://coveralls.io/github/TopherGopher/mongotest?branch=main)

mongotest starts a real MongoDB server in a Docker container for a Go test,
hands the test a connected client of the official MongoDB Go driver (v1), and
removes the container when the test ends.

```sh
go get github.com/tophergopher/mongotest
```

It needs Go 1.27 or newer, and Docker or Podman. Without one, the test fails;
it is not skipped.

```go
func TestOrders(t *testing.T) {
    orders := mongotest.Run(t).Database("shop").Collection("orders")
    _, err := orders.InsertOne(t.Context(), bson.M{"item": "book"})
    require.NoError(t, err)
}
```

Upgrading? The earlier function names still exist, but code that used the
easymongo connection must change; [CHANGELOG.md](CHANGELOG.md) shows how.
For Podman, see the [Podman guide](https://tophergopher.github.io/mongotest/guides/podman/).

## Cleaning up stray containers

Every container mongotest starts carries the label `mongotest=regression`. If
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
[Writing a new page](https://tophergopher.github.io/mongotest/reference/writing-docs/).
