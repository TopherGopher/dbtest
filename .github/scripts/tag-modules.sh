#!/bin/bash
# tag-modules.sh tags the modules other than the root for a release whose root
# tag, vX.Y.Z, already exists, and pushes the three tags together. The release
# workflow runs it; see "Releasing" in docs/project/contributing.md.
#
#	.github/scripts/tag-modules.sh vX.Y.Z
#
# It pushes nothing unless every check passes.
set -euo pipefail

root=github.com/tophergopher/dbtest
modules="mongotest mongotest/exporter mobyclient"
version=${1:-}

fail() {
	echo "tag-modules: $*" >&2
	exit 1
}

# required prints the version of the root that a module's go.mod requires, in a
# require block or on a single require line, at the given commit.
required() {
	git show "$1:$2/go.mod" | awk -v m="$root" '$1 == m { print $2 } $1 == "require" && $2 == m { print $3 }'
}

# highest prints the highest vX.Y.Z tag in the repository.
highest() {
	git tag --list | grep -Ex 'v[0-9]+\.[0-9]+\.[0-9]+' | sort -V | tail -n 1
}

if ! printf '%s\n' "$version" | grep -Eqx 'v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)'; then
	fail "the release's tag is \"$version\", not vX.Y.Z; tag modules by hand or publish a release named vX.Y.Z"
fi

git fetch --quiet --tags origin main
commit=$(git rev-parse "refs/tags/$version^{commit}")

if ! git merge-base --is-ancestor "$commit" origin/main; then
	fail "$version names $commit, which is not on main; release the next patch version from main instead"
fi
# The tags from before the rename, up to v0.2.9, are versions of this module
# path too, so a lower version would never be what "go get ...@latest" picks.
if [ "$(highest)" != "$version" ]; then
	fail "$version is not the highest tag ($(highest) is); release a version above it"
fi
for m in $modules; do
	if [ "$(required "$commit" "$m")" != "$version" ]; then
		fail "$m/go.mod at $version does not require $root $version; merge the version-bump pull request, then release the next patch version"
	fi
	if git rev-parse --quiet --verify "refs/tags/$m/$version" >/dev/null; then
		fail "the tag $m/$version already exists"
	fi
done

for m in $modules; do
	git tag -a "$m/$version" -m "$m/$version" "$commit"
done
# --atomic pushes all three tags or none.
git push --atomic origin $(for m in $modules; do printf '%s ' "$m/$version"; done)
echo "tagged and pushed $(for m in $modules; do printf '%s ' "$m/$version"; done)at $commit"
