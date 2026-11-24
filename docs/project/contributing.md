---
title: Contributing
description: How to build and test dbtest, and what a change has to meet.
---

# Contributing

Issues and pull requests are welcome at
[github.com/TopherGopher/dbtest](https://github.com/TopherGopher/dbtest).
This page says how to build and test, and what a change has to meet.

## Build and test

The repository has six modules: the root, `mongotest`, `mongotestv2`,
`mongotest/exporter`, `mobyclient` and `pgtest`.

```sh
git clone https://github.com/TopherGopher/dbtest
cd dbtest

gofmt -l .                              # must print nothing
go vet ./...
go test -race ./...                     # root module

# Each of these is a separate module, which the root's ./... does not reach.
(cd mongotest && go vet ./... && go test -race ./...)
(cd mongotestv2 && go vet ./... && go test -race ./...)
(cd mongotest/exporter && go vet ./... && go test -race ./...)
(cd mobyclient && go vet ./... && go test -race ./...)
(cd pgtest && go vet ./... && go test -race ./...)
```

Integration tests need a Docker daemon, and pull the images they use. Without
a daemon they fail rather than skip.

### Benchmarks and fuzzing

The Docker client benchmarks run against an in-process fake Docker daemon,
so no Docker is needed. `BenchmarkStart` in `mongod` starts real
containers, so it needs a Docker daemon and takes minutes:

```sh
go test ./dockerapi/... ./dockermock/... ./dockerclient/... -run='^$' -bench=. -benchmem
go test ./mongod/ -run='^$' -bench=BenchmarkStart -benchtime=1x
go test ./dockerclient/ -run='^$' -fuzz=FuzzCheckID -fuzztime=30s
(cd pgtest && go test -run='^$' -fuzz=FuzzDatabaseName -fuzztime=30s)
```

## Known traps

- **A local `go.work` hides version problems.** A workspace resolves the
  dependencies of all six modules together. The root is then tested against
  newer versions than its users get. CI tests each module from its own
  `go.mod`, so it can fail where your run passed. `.gitignore` keeps a
  `go.work` out of commits. If `go work sync` has rewritten the modules'
  `go.mod` and `go.sum` files, restore them all with
  `git checkout -- go.mod go.sum '*/go.mod' '*/go.sum'`.
- **Every `go.mod` has a `toolchain` line naming the Go release that builds
  and tests it.** If your Go is older, the `go` command downloads that
  release. With `GOTOOLCHAIN=local`, it uses your Go instead, which may not
  be the release CI uses. Change the line in all six files together. CI
  installs exactly the root's release, and fails if another module names a
  different one.
- **A module's tests see your uncommitted root changes; its users do not.**
  Each module other than the root has a `replace` pointing at the repository
  root, so it builds against the root's working tree. Its users get only the
  root version its `go.mod` requires, so a change that spans the root and a
  module reaches them only once both are released. The release steps below release
  them together. Go ignores `replace` in a module that is a dependency, so the line does
  no harm in a release.
- **`mongotest` and `mongotestv2` keep copies of the same files:**
  `mongotest.go`, `database.go`, `errors.go` and their tests. A change to one
  belongs in the other, in the same pull request. Each copied file that is
  not a test says in its header where its two copies differ, and
  `diff mongotest/mongotest.go mongotestv2/mongotest.go` shows it. CI does
  not compare the copies. It checks only that `mongotestv2`, tests included,
  builds no part of driver v1.

## Releasing

Each module is released by its own tag, all with the same version: the root
as `vX.Y.Z`, and the others with their directory as a prefix, such as
`mongotest/vX.Y.Z`. A release is one pull request and one GitHub release.

Choose a version above the highest `vX.Y.Z` tag. That includes the old tags,
up to v0.2.9, from when the module was `github.com/tophergopher/mongotest`.
`go get ...@latest` picks the highest version, so a lower one would never
reach users. A pushed tag cannot be taken back, because the Go module proxy keeps
what it has fetched.

1. In a pull request, point the other five modules at the new version, and
   rename the CHANGELOG's "Unreleased" heading to the version:

    ```sh
    for m in mongotest mongotestv2 mongotest/exporter mobyclient pgtest; do
      (cd "$m" && go mod edit -require=github.com/tophergopher/dbtest@vX.Y.Z)
    done
    ```

    The `replace` lines keep this building before `vX.Y.Z` is tagged.
    Never tag a module on its own. `pgtest` uses the `postgres` package,
    which no root version up to v0.3.0 has, so a `pgtest` tag must require
    the root version released with it.

2. Publish a full GitHub release, not a pre-release, with a new tag `vX.Y.Z`
   on that pull request's merge commit. The release creates the root module's
   tag, and publishing it starts the Release workflow
   (`.github/workflows/release.yml`). The workflow checks that the commit is
   on `main`, that `vX.Y.Z` is the highest version tag, and that all five
   modules require it. It then tags the five modules on the same commit,
   pushes the tags together, and asks the Go module proxy for all six
   versions. It does nothing for a pre-release.

Publish the release as soon as a new module reaches `main`. Until it is
tagged, `go get` gives its users a pseudo-version of `main`, which requires
the last root release and may not build against it. Today that holds for
`pgtest` and `mongotestv2`. `pgtest` cannot build against v0.3.0, which
has no `postgres` package.

If the run fails, its log says which step failed and what to do:

- **A check failed:** nothing was pushed, but the root tag is public and
  cannot be reused. Follow the log's advice, which is usually to release the
  next patch version.
- **The push failed:** re-run the job from the Actions page.
- **Only the proxy step failed:** the tags are pushed. Ask the proxy again,
  for example with
  `curl https://proxy.golang.org/github.com/tophergopher/dbtest/mongotest/@v/vX.Y.Z.info`.

If the workflow cannot run, for a release whose commit predates it or with
Actions unavailable, tag the merge commit by hand instead. If a GitHub
release already created `vX.Y.Z`, leave it out of the loop and the push.
First check and tag:

```sh
c=SHA-of-the-merge-commit
git fetch origin
git merge-base --is-ancestor "$c" origin/main && echo "on main"
git grep -l "tophergopher/dbtest vX.Y.Z" "$c" -- '*/go.mod'  # the five modules
for t in vX.Y.Z mongotest/vX.Y.Z mongotestv2/vX.Y.Z mongotest/exporter/vX.Y.Z mobyclient/vX.Y.Z pgtest/vX.Y.Z; do
  git tag -a "$t" -m "$t" "$c"
done
git tag --points-at "$c"  # exactly the six tags
```

Push only if the output shows that the commit is on `main`, that all five
modules require `vX.Y.Z`, and that all six tags name the commit:

```sh
git push --atomic origin vX.Y.Z mongotest/vX.Y.Z mongotestv2/vX.Y.Z mongotest/exporter/vX.Y.Z mobyclient/vX.Y.Z pgtest/vX.Y.Z
```

`--atomic` pushes all six or none.

## Image copies

The Copy images workflow (`.github/workflows/copy-images.yml`) copies each
MongoDB image the library uses by default to this repository's packages on
ghcr.io. The copies are a last resort for the library's pulls, after Docker
Hub and its mirror; the library starts using them in a follow-up to #111,
once they are published. Each copy keeps its Docker Hub path: `mongo:8.0`,
whose full path is `library/mongo:8.0`, becomes
`ghcr.io/tophergopher/dbtest/library/mongo:8.0`. Each copy has the same
digest as its source. The workflow runs twice a day, and can be started by
hand from the Actions page.

- **After a run creates a new package,** make it public. GitHub may create
  packages as private, and a private package cannot be pulled without
  logging in. On the repository's page, open the package under Packages,
  then "Package settings", and change its visibility to public. Under
  "Manage Actions access" on the same page, check that only this repository
  has Write.
- **When a default image in `mongod/image.go` changes,** change the list in
  `.github/scripts/copy-images.sh` to match. Make the new package public
  after the next run.
- **If a run fails,** its log names each image that was not copied, and why.
  The other images are still copied. The images are copied from ECR Public
  and quay.io; if either is unavailable, the next run tries again. If a
  copy's digest differs from its source's, the log names the package version
  to delete.

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
- CI must be green: `gofmt`, `go vet`, the tests of all six modules, and
  the documentation build.
