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
- **Each module other than the root has a `replace`** pointing at the
  repository root, so it builds against the root's working tree, and can
  require a root version that has no tag yet. Go applies `replace` only in the
  module being built, so released versions keep the line harmlessly.

## Releasing

Each module is released by its own tag, all with the same version: the root
as `vX.Y.Z`, and the others with their directory as a prefix, such as
`mongotest/vX.Y.Z`. A release is one pull request and one GitHub release.

Choose a version above the highest existing `vX.Y.Z` tag. The tags made
before the module was renamed from `github.com/tophergopher/mongotest`, up to
v0.2.9, count as versions of `github.com/tophergopher/dbtest` too, and a lower
version is never the one `go get ...@latest` picks. A pushed tag cannot be
taken back, because the Go module proxy keeps what it has fetched.

1. In a pull request, point the other three modules at the new version, and
   rename the CHANGELOG's "Unreleased" heading to the version:

    ```sh
    for m in mongotest mongotest/exporter mobyclient; do
      (cd "$m" && go mod edit -require=github.com/tophergopher/dbtest@vX.Y.Z)
    done
    ```

    The `replace` lines keep this building before `vX.Y.Z` is tagged.

2. Publish a GitHub release with a new tag `vX.Y.Z` and, as its target, that
   pull request's merge commit. Publish it as a full release: the workflow
   skips pre-releases. The release creates the root module's tag. The Release workflow (`.github/workflows/release.yml`)
   then checks that the commit is on `main`, that `vX.Y.Z` is the highest
   version tag, and that all three modules require it. It tags the three
   modules on the same commit, pushes the tags together, and asks the Go
   module proxy for all four versions.

If the run fails, its log says which check failed and what to do:

- **A check failed:** nothing was pushed, but the root tag is public and
  cannot be reused. Follow the log's advice, which is usually to release the
  next patch version.
- **The push failed:** re-run the job from the Actions page.
- **Only the proxy step failed:** the tags are pushed. Ask the proxy again,
  for example with
  `curl https://proxy.golang.org/github.com/tophergopher/dbtest/mongotest/@v/vX.Y.Z.info`.

If the workflow cannot run, for a release whose commit predates it or with
Actions unavailable, tag the merge commit by hand instead. Before the push,
check the output: the commit is on `main`, all three modules require `vX.Y.Z`,
and all four tags name the commit. If a GitHub release already created
`vX.Y.Z`, leave it out of the loop and the push.

```sh
c=SHA-of-the-merge-commit
git fetch origin
git merge-base --is-ancestor "$c" origin/main && echo "on main"
git grep -l "tophergopher/dbtest vX.Y.Z" "$c" -- '*/go.mod'  # the three modules
for t in vX.Y.Z mongotest/vX.Y.Z mongotest/exporter/vX.Y.Z mobyclient/vX.Y.Z; do
  git tag -a "$t" -m "$t" "$c"
done
git tag --points-at "$c"  # exactly the four tags
git push --atomic origin vX.Y.Z mongotest/vX.Y.Z mongotest/exporter/vX.Y.Z mobyclient/vX.Y.Z
```

`--atomic` pushes all four or none.

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
