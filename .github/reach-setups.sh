#!/bin/sh
# Runs TestIntegrationReach in one of the setups CI covers, to check that a
# start reaches mongod there. CI runs each setup in its own job;
# to run one by hand, from the repository root:
#
#	.github/reach-setups.sh sibling
#
# The setups:
#
#	sibling       the test runs in a container on a user-defined network,
#	              with the Docker socket mounted
#	host-network  the same, with --network host
#	dind          the test runs in a container next to a docker:dind
#	              service, reached at tcp://docker:2375
#	tcp           the test runs on this machine, and reaches this machine's
#	              daemon over tcp:// on the docker0 bridge address,
#	              standing in for a remote daemon
#
# It needs Docker and Go, and dind needs permission to run a privileged
# container. It tags the pinned image below as mongo:8.0, and removes what
# it creates and nothing else.
set -eu

setup=${1:-}
case $setup in
sibling | host-network | dind | tcp) ;;
*)
	echo "usage: $0 sibling|host-network|dind|tcp" >&2
	exit 2
	;;
esac
# The test runs in the image the start uses, so one pull serves both. It is
# pinned because sibling and host-network give it the Docker socket.
image=mongo:8.0@sha256:bdc216e9d5a6ec2cfdac789b5832fdab64cd32055f59885cae918a92de63c361
network=dbtest-reach-$$
# The helper images are pinned, so a change upstream cannot change what runs.
dind_image=docker:29-dind@sha256:4bbc7223f81db790097c6dea42fb93e05b8fead4f0431fbe6ccaba6e8473201c
socat_image=alpine/socat:1.8.0.3@sha256:beb4a68d9e4fe6b0f21ea774a0fde6c31f580dde6368939ed70100c5385b015e
work=$(mktemp -d)

cleanup() {
	docker rm -f "dbtest-dind-$$" "dbtest-dockertcp-$$" >/dev/null 2>&1 || true
	# A mongod left by a test that was killed would keep the network in use.
	docker ps -aq --filter network="$network" | xargs -r docker rm -f >/dev/null 2>&1 || true
	docker network rm "$network" >/dev/null 2>&1 || true
	rm -rf "$work"
}
trap cleanup EXIT
# sh does not run the EXIT trap when a signal kills it, so these traps call
# exit, which does.
trap 'exit 130' INT TERM HUP

# A static test binary runs in any image.
CGO_ENABLED=0 go test -c -o "$work/reach.test" ./mongod
docker image inspect "$image" >/dev/null 2>&1 || docker pull --quiet "$image" >/dev/null
docker tag "$image" mongo:8.0

# wait_for runs a command once a second until it succeeds, for a minute.
wait_for() {
	tries=0
	until "$@" >/dev/null 2>&1; do
		tries=$((tries + 1))
		if [ "$tries" -ge 60 ]; then
			echo "$0: gave up waiting for: $*" >&2
			exit 1
		fi
		sleep 1
	done
}

# in_container runs the test in a container of $image. Its arguments go to
# docker run.
in_container() {
	docker run --rm -e DBTEST_REACH_SETUP="$setup" \
		-v "$work/reach.test:/reach.test:ro" "$@" \
		--entrypoint /reach.test "$image" -test.run '^TestIntegrationReach$' -test.v
}

case $setup in
sibling)
	docker network create "$network" >/dev/null
	# With WithTLS a start dials this network's gateway, so the test is told
	# what it is.
	gateway=$(docker network inspect "$network" --format '{{(index .IPAM.Config 0).Gateway}}')
	in_container --network "$network" -e DBTEST_REACH_GATEWAY="$gateway" \
		-v /var/run/docker.sock:/var/run/docker.sock
	;;
host-network)
	in_container --network host -v /var/run/docker.sock:/var/run/docker.sock
	;;
dind)
	docker network create "$network" >/dev/null
	# An empty DOCKER_TLS_CERTDIR makes the daemon listen on 2375 without
	# TLS. Only containers on $network can reach it.
	docker run -d --privileged --name "dbtest-dind-$$" --network "$network" --network-alias docker \
		-e DOCKER_TLS_CERTDIR= "$dind_image" >/dev/null
	wait_for docker exec "dbtest-dind-$$" docker info
	in_container --network "$network" -e DOCKER_HOST=tcp://docker:2375
	;;
tcp)
	# The daemon listens only on its socket, so a forwarder serves it on the
	# docker0 address. It accepts connections only from that address, which
	# is the one this machine's own processes connect from. A container
	# connects from its own address, so it is refused.
	gateway=$(docker network inspect bridge --format '{{(index .IPAM.Config 0).Gateway}}')
	docker run -d --rm --name "dbtest-dockertcp-$$" --network host \
		-v /var/run/docker.sock:/var/run/docker.sock \
		"$socat_image" "tcp-listen:2375,bind=$gateway,range=$gateway/32,fork,reuseaddr" \
		unix-connect:/var/run/docker.sock >/dev/null
	wait_for docker --host "tcp://$gateway:2375" info
	DOCKER_HOST="tcp://$gateway:2375" DBTEST_REACH_SETUP=tcp "$work/reach.test" -test.run '^TestIntegrationReach$' -test.v
	;;
esac
