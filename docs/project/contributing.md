---
title: Contributing
description: How to build and test mongotest, and what a change has to meet.
---

# Contributing

Issues and pull requests are welcome at
[github.com/TopherGopher/mongotest](https://github.com/TopherGopher/mongotest).
This page says how to build and test, and what a change has to meet.

## Build and test

```sh
git clone https://github.com/TopherGopher/mongotest
cd mongotest

gofmt -l .                              # must print nothing
go vet ./...
go test -race ./...                     # root module

cd mobyclient && go test -race ./...    # separate module, not reached by ./...
cd ../exporter && go test -race ./...   # separate module, not reached by ./...
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
  it out. A workspace resolves all three modules' dependencies together, so
  the root is tested against newer versions than its users get; CI tests each
  module from its own `go.mod`. If `go work sync` has rewritten `go.mod` and
  `go.sum`, restore them with `git checkout -- go.mod go.sum`.
- **Every `go.mod` pins the Go release** with a `toolchain` line: the root,
  `mobyclient/go.mod` and `exporter/go.mod`. If your Go is older, the `go`
  command downloads the pinned release the first time it runs. Change the pin
  in all three files together. CI installs the root's release and fails if
  another module pins a different one.
- **`mobyclient/go.mod` carries a `replace`** pointing at the repository root
  until a tag contains `dockerclient`. It is annotated in the file.

## The standard

A change is reviewed against
[`AGENTS.md`](https://github.com/TopherGopher/mongotest/blob/main/AGENTS.md)
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
- CI must be green: `gofmt`, `go vet`, the tests of all three modules, and
  the documentation build.
