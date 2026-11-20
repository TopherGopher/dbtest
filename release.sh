#!/bin/sh
# release.sh tags a release of every module in this repository:
#
#	./release.sh vX.Y.Z
#
# Run it twice. The first run tags the root module and stops, naming each
# other module that does not yet require the root at vX.Y.Z. Fix those, commit
# and push, then run it again to tag the other modules. See "Releasing" in
# docs/project/contributing.md.
set -eu

root=github.com/tophergopher/dbtest
modules="mongotest mongotest/exporter mobyclient"

version=${1:-}
case $version in
v[0-9]*.[0-9]*.[0-9]*) ;;
*)
	echo "usage: $0 vX.Y.Z" >&2
	exit 2
	;;
esac

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
# A tag made by a run that stopped before pushing it is pushed as it is.
tag() {
	if git ls-remote --exit-code --tags origin "refs/tags/$1" >/dev/null; then
		echo "$1 already exists"
		return
	fi
	if ! git rev-parse --quiet --verify "refs/tags/$1" >/dev/null; then
		git tag -a "$1" -m "$1"
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
