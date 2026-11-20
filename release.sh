#!/bin/sh
# release.sh tags a release of every module in this repository:
#
#	./release.sh vX.Y.Z
#
# Run it twice. The first run tags the root module and stops, naming each
# other module that does not yet require the root at vX.Y.Z. Run the commands
# it prints, commit and push, then run it again to tag the other modules. See
# "Releasing" in docs/project/contributing.md.
set -eu

root=github.com/tophergopher/dbtest
modules="mongotest mongotest/exporter mobyclient"

version=${1:-}
if ! printf '%s\n' "$version" | grep -Eqx 'v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)'; then
	echo "usage: $0 vX.Y.Z" >&2
	exit 2
fi

# The repository has tags from before the rename, up to v0.2.9. They become
# versions of this module path too, so a release must be newer than all of
# them, or "@latest" would pick an old tag whose go.mod names another path.
latest=$(git tag --list | grep -Ex 'v[0-9]+\.[0-9]+\.[0-9]+' | sort -V | tail -n 1 || true)
if [ -n "$latest" ] && ! git rev-parse --quiet --verify "refs/tags/$version" >/dev/null; then
	if [ "$(printf '%s\n%s\n' "$latest" "$version" | sort -V | tail -n 1)" = "$latest" ]; then
		echo "release: $version is not newer than the latest tag, $latest" >&2
		exit 1
	fi
fi

if [ -n "$(git status --porcelain)" ]; then
	echo "release: commit or stash your local changes first" >&2
	exit 1
fi

# A tag must name a commit that is on main, or the release would carry code
# nobody reviewed.
git fetch --quiet origin main
if ! git merge-base --is-ancestor HEAD origin/main; then
	echo "release: HEAD is not on origin/main; push it, or check out main" >&2
	exit 1
fi

# tag creates an annotated tag on HEAD and pushes it, unless origin has it.
# If an earlier run created the tag but did not push it, that tag is pushed,
# but only if it names a commit on origin/main: a stale local tag could name
# unreviewed code, and a pushed version cannot be taken back.
tag() {
	if git ls-remote --exit-code --tags origin "refs/tags/$1" >/dev/null; then
		echo "$1 already exists"
		return
	fi
	if ! git rev-parse --quiet --verify "refs/tags/$1" >/dev/null; then
		git tag -a "$1" -m "$1"
	elif ! git merge-base --is-ancestor "refs/tags/$1^{commit}" origin/main; then
		echo "release: the local tag $1 names a commit that is not on origin/main; delete it with: git tag -d $1" >&2
		exit 1
	fi
	git push --quiet origin "$1"
	echo "pushed $1"
}

# required prints the version of the root that a module's go.mod requires,
# whether in a require block or on a single require line.
required() {
	awk -v m="$root" '$1 == m { print $2 } $1 == "require" && $2 == m { print $3 }' "$1/go.mod"
}

tag "$version"

stale=""
for m in $modules; do
	if [ "$(required "$m")" != "$version" ]; then
		stale="$stale $m"
	fi
done
if [ -n "$stale" ]; then
	echo "release: these modules do not require $root $version yet:" >&2
	for m in $stale; do
		echo "	(cd $m && go mod edit -require=$root@$version)" >&2
	done
	echo "Run those, commit and push to main, then run $0 $version again." >&2
	exit 1
fi

for m in $modules; do
	tag "$m/$version"
done
