---
title: Contributing
description: How to build and test dbtest, and what a change has to meet.
---

# Contributing

Issues and pull requests are welcome at
[github.com/TopherGopher/dbtest](https://github.com/TopherGopher/dbtest).
This page says how to build and test, and what a change has to meet.

## Build and test

```sh
git clone https://github.com/TopherGopher/dbtest
cd dbtest

gofmt -l .                              # must print nothing
go vet ./...
go test -race ./...                     # root module

# Each of these is a separate module, which the root's ./... does not reach.
(cd mongotest && go test -race ./...)
(cd mongotest/exporter && go test -race ./...)
(cd mobyclient && go test -race ./...)
```

Integration tests need a Docker daemon, and pull the images they use. Without
a daemon they fail rather than skip.

Benchmarks and fuzzing:

```sh
go test ./... -run='^$' -bench=. -benchmem
go test ./dockerclient/ -run='^$' -fuzz=FuzzCheckID -fuzztime=30s
```

## Known traps

- **Do not commit a `go.work`.** A local one is fine, and `.gitignore` keeps
  it out. A workspace resolves all four modules' dependencies together, so
  the root is tested against newer versions than its users get; CI tests each
  module from its own `go.mod`. If `go work sync` has rewritten `go.mod` and
  `go.sum`, restore them with `git checkout -- go.mod go.sum`.
- **Every `go.mod` pins the Go release used to build and test** in a
  `toolchain` line: the
  root, `mongotest/go.mod`, `mongotest/exporter/go.mod` and
  `mobyclient/go.mod`. If your Go is older, the `go` command downloads that
  release, unless you have set `GOTOOLCHAIN=local`. Change the line in all
  four files together. CI installs exactly the root's release, and fails if
  another module names a different one.
- **Each module other than the root carries a `replace`** pointing at the
  repository root, so a change to the root is tested here before it is
  tagged. Go ignores a `replace` in a module that another module requires,
  so the line is harmless in released versions and stays in them. A comment
  in each file explains it.

## Releasing

Each module is released by its own tag: the root as `vX.Y.Z`, and the others
with their directory as a prefix, such as `mongotest/vX.Y.Z`. The other
modules require the root at the new version, which exists only once the root
is tagged. So `release.sh` runs twice, with a commit between.

1. On an up-to-date `main` with no local changes, run `./release.sh vX.Y.Z`.
   It tags the root `vX.Y.Z` and pushes the tag. It then stops, naming each
   module that does not yet require the root at `vX.Y.Z`, with the command
   that fixes it:

    ```sh
    (cd mongotest && go mod edit -require=github.com/tophergopher/dbtest@vX.Y.Z)
    ```

2. Run those commands, then commit and push the change to `main`.
3. Run `./release.sh vX.Y.Z` again. It finds the root already tagged, checks
   the other modules, and tags and pushes `mongotest/vX.Y.Z`,
   `mongotest/exporter/vX.Y.Z` and `mobyclient/vX.Y.Z`.

A tag that already exists is left alone, so a run that stopped partway can be
run again. The script refuses a version that is not newer than every existing
tag: the tags from before the rename, up to v0.2.9, are versions of this
module path too.

## The standard

A change is reviewed against
[`AGENTS.md`](https://github.com/TopherGopher/dbtest/blob/main/AGENTS.md)
at the repository root. In short:

- Every pull request starts from an issue that says what the problem is and
  what done looks like.
- A call with no options does the right thing without the caller reading
  the docs, and the simplest code that works wins.
- The test is written first. It asserts on behaviour and typed errors, never
  on message wording.
- Comments and docs are brief: plain words, whole sentences, no history and
  no measurements.
- Someone with no context reads the change before it merges.

## Documentation

The site you are reading lives in `docs/` and is rebuilt on every push to
`main`. A change that adds or alters behaviour should update the guide that
describes it in the same pull request. [Writing a new page](../reference/writing-docs.md)
has the conventions and the local preview command.

## Pull requests

- One concern per pull request. A refactor and a behaviour change are two
  pull requests.
- The description says what changed and why, and names the issue it closes.
- A change a user would notice adds a line to `CHANGELOG.md`, under
  "Unreleased".
- CI must be green: `gofmt`, `go vet`, the tests of all four modules, and
  the documentation build.
