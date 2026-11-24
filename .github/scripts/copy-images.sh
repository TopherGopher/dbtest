#!/bin/bash
# copy-images.sh copies each MongoDB image the library uses by default to a
# registry, as an exact copy with the same digest. Each copy keeps its Docker
# Hub path, so library/mongo:8.0 becomes DESTINATION/library/mongo:8.0. The
# Copy images workflow runs it; see "Image copies" in
# docs/project/contributing.md.
#
# Usage:
#
#	.github/scripts/copy-images.sh ghcr.io/tophergopher/dbtest
#
# An image whose copy is already current is skipped. A failure with one image
# does not stop the others, but fails the run.
set -euo pipefail

# The Docker Hub path of each default image in mongod/image.go, at its
# default tag. When a default changes, change it here too.
images="
library/mongo:8.0
mongodb/mongodb-community-server:8.0-ubi9
mongodb/mongodb-atlas-local:8.0
mongodb/mongodb-enterprise-server:8.0-ubi9
"
to=${1:?"usage: copy-images.sh DESTINATION, such as ghcr.io/tophergopher/dbtest"}

fail() {
	echo "copy-images: $*" >&2
	return 1
}

# mirror prints the registry to copy an image from: the mirror the library
# also pulls from. It serves the same digests as Docker Hub, and pulls from it
# do not count against Docker Hub's rate limit.
mirror() {
	case $1 in
	library/*) echo "public.ecr.aws/docker/$1" ;;
	*) echo "quay.io/$1" ;;
	esac
}

# digest prints the digest of the manifest or index a reference names: the
# sha256 of its bytes, exactly as the registry serves them.
digest() {
	local sum
	sum=$(docker buildx imagetools inspect --raw "$1" | sha256sum) || return 1
	echo "sha256:${sum%% *}"
}

# copy copies one image, named by its Docker Hub path, and checks that the
# copy has the source's digest. It skips an image whose copy is already
# current.
copy() {
	local from want got
	from=$(mirror "$1")
	want=$(digest "$from") || { fail "cannot read $from; the next scheduled run retries, or start a run now from the Actions page"; return; }
	got=$(digest "$to/$1" 2>/dev/null) || got=none
	if [ "$got" = "$want" ]; then
		echo "$to/$1 is current at $want"
		return 0
	fi
	# Copying by digest copies exactly what was read above, even if the tag
	# moves to a new image during the run. Given one source and no other
	# flags, imagetools create copies the index and every platform's
	# manifest byte for byte.
	docker buildx imagetools create --tag "$to/$1" "$from@$want" || { fail "cannot copy $from to $to/$1; see the docker error above, then start a run again from the Actions page"; return; }
	got=$(digest "$to/$1") || { fail "copied $from to $to/$1, but cannot read it back to check its digest; start a run again from the Actions page"; return; }
	if [ "$got" != "$want" ]; then
		fail "$to/$1 has digest $got, but $from has $want. Delete version $got of the package on GitHub so that nothing pulls it, then find out why the copy differs"
		return
	fi
	echo "copied $from to $to/$1 at $want"
}

failed=0
for image in $images; do
	copy "$image" || failed=1
done
exit "$failed"
