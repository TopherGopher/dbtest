# mongotest

[![Documentation](https://img.shields.io/badge/docs-tophergopher.github.io%2Fmongotest-0e8f7a)](https://tophergopher.github.io/mongotest/)
[![Go Reference](https://pkg.go.dev/badge/github.com/tophergopher/mongotest.svg)](https://pkg.go.dev/github.com/tophergopher/mongotest)

mongotest starts a real MongoDB server in a Docker container for a Go test,
hands the test a connected client of the official driver (v1), and removes
the container when the test ends.

```go
func TestOrders(t *testing.T) {
    orders := mongotest.Run(t).Database("shop").Collection("orders")
    _, err := orders.InsertOne(t.Context(), bson.M{"item": "book"})
    require.NoError(t, err)
}
```

It needs Docker or Podman, and fails the test without one; it never skips
it. Code written for `NewTestConnection` still compiles; [CHANGELOG.md](CHANGELOG.md)
says what changed.

# Cleaning up stray containers

Every container mongotest starts carries the label `mongotest=regression`. If
a killed test run leaves one behind, this removes them all, with their
volumes:

```shell
docker rm -fv $(docker ps -aq --filter label=mongotest=regression)
```

# Documentation

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
