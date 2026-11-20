package mongod_test

import (
	"context"
	"net"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/dbtest/mongod"
)

// fanOut is how many containers the test below starts at the same moment:
// enough to prove each is its own server, quickly and without depending on
// how busy a shared CI runner is. Issue #82 covers how many a machine can
// start at once.
const fanOut = 16

// Tests that run in parallel each call Start, so starts arrive together.
// Every one has to succeed, and every one has to be given a server of its
// own: a container no other start was given, published on a port no other
// has, with a mongod answering at the address the container reports.
func TestIntegrationContainersStartedTogetherAreEachTheirOwnServer(t *testing.T) {
	docker, ctx := liveDocker(t)
	key, value := runLabel(t)
	requireImage(t, ctx, docker, mongod.ImageDockerHub)
	opts := mongod.DefaultOptions().WithDocker(docker).WithLabel(key, value)

	containers, errs := startTogether(ctx, opts, fanOut)
	t.Cleanup(func() { stopTogether(context.Background(), containers) })
	for i, err := range errs {
		require.NoError(t, err, "start %d of %d made at the same moment must succeed", i+1, fanOut)
	}

	ids, ports := map[string]bool{}, map[int]bool{}
	for i, c := range containers {
		ids[c.ID()], ports[c.Port()] = true, true

		info, err := docker.ContainerInspect(ctx, c.ID())
		require.NoError(t, err, "container %d is on the daemon under the id it reports", i+1)
		assert.Equal(t, strconv.Itoa(c.Port()), info.HostPort("27017/tcp"),
			"container %d is published on its own port", i+1)
		assert.NoError(t, askHello(net.JoinHostPort(c.Host(), strconv.Itoa(c.Port()))),
			"container %d answers at its address", i+1)
	}
	assert.Len(t, ids, fanOut, "every start was given a container no other start was given")
	assert.Len(t, ports, fanOut, "published on a port no other has")

	for i, err := range stopTogether(ctx, containers) {
		require.NoError(t, err, "container %d is removed", i+1)
	}
	if left, checked := containersLabelled(t, key, value); checked {
		assert.Empty(t, left, "nothing this test started is left on the daemon")
	}
}

// startTogether starts n containers at the same moment. It returns them in
// order, with the error of each start that failed.
func startTogether(ctx context.Context, opts mongod.Options, n int) ([]*mongod.Container, []error) {
	containers, errs := make([]*mongod.Container, n), make([]error, n)
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			<-release
			containers[i], errs[i] = mongod.StartWithOptions(ctx, opts)
		})
	}
	close(release)
	wg.Wait()
	return containers, errs
}

// stopTogether removes every container at the same moment, and returns each
// one's error. Stop does nothing for one that is nil or already gone.
func stopTogether(ctx context.Context, containers []*mongod.Container) []error {
	errs := make([]error, len(containers))
	var wg sync.WaitGroup
	for i, c := range containers {
		wg.Go(func() { errs[i] = c.Stop(ctx) })
	}
	wg.Wait()
	return errs
}
