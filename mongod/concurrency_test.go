package mongod_test

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/mongod"
)

// fanOut is how many containers the test below starts at the same moment.
// What bounds it is the start timeout: every mongod has to come up inside it
// on the CPUs there are. This is half of what a four-CPU runner manages.
const fanOut = 48

// shellsAtOnce is how many mongosh processes the test runs at a time. A
// mongosh is far heavier than the mongod it talks to, so one per container
// all at once would measure the shells and not the starts.
const shellsAtOnce = 8

// Tests that run in parallel each call Start, so starts arrive together.
// Every one has to succeed, and every one has to be given a server of its
// own: a value written to one must be found there and nowhere else.
func TestIntegrationContainersStartedTogetherAreEachTheirOwnServer(t *testing.T) {
	docker, ctx := liveDocker(t)
	key, value := runLabel(t)
	requireImage(t, ctx, docker, mongod.ImageDockerHub)
	opts := mongod.DefaultOptions().WithDocker(docker).WithLabel(key, value)

	containers, errs := startTogether(ctx, opts, fanOut)
	t.Cleanup(func() { stopAll(containers) })
	for i, err := range errs {
		require.NoError(t, err, "start %d of %d made at the same moment must succeed", i+1, fanOut)
	}

	const marks = `db.getSiblingDB("fanout").marks`
	written := inEach(containers, func(i int, c *mongod.Container) error {
		_, err := mongoshIn(ctx, docker, c, fmt.Sprintf(`%s.insertOne({owner: %d})`, marks, i))
		return err
	})
	for i, err := range written {
		require.NoError(t, err, "container %d must take a write as soon as Start has returned it", i+1)
	}

	found := make([]string, len(containers))
	read := inEach(containers, func(i int, c *mongod.Container) (err error) {
		found[i], err = mongoshIn(ctx, docker, c, marks+`.find().toArray().map(m => m.owner).join(",")`)
		return err
	})
	for i, err := range read {
		require.NoError(t, err, "container %d must answer a read", i+1)
		assert.Equal(t, strconv.Itoa(i), found[i],
			"container %d holds the one value written to it: two starts handed the same server would each find the other's value too", i+1)
	}

	stopped := inEach(containers, func(_ int, c *mongod.Container) error { return c.Stop(ctx) })
	for i, err := range stopped {
		require.NoError(t, err, "container %d is removed", i+1)
	}
	if ids, checked := containersLabelled(t, key, value); checked {
		assert.Empty(t, ids, "nothing this test started is left on the daemon")
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

// inEach runs do for every container, shellsAtOnce at a time, and returns
// each one's error.
func inEach(containers []*mongod.Container, do func(i int, c *mongod.Container) error) []error {
	errs := make([]error, len(containers))
	free := make(chan struct{}, shellsAtOnce)
	var wg sync.WaitGroup
	for i, c := range containers {
		wg.Go(func() {
			free <- struct{}{}
			defer func() { <-free }()
			errs[i] = do(i, c)
		})
	}
	wg.Wait()
	return errs
}

// stopAll removes every container that was started. Stop does nothing for
// one that is nil or already gone.
func stopAll(containers []*mongod.Container) {
	for _, c := range containers {
		_ = c.Stop(context.Background())
	}
}

// mongoshIn runs one mongosh expression inside a container and returns what it
// printed. It reports a failure as an error where evalIn fails the test, so
// it can be called off the test's own goroutine.
func mongoshIn(ctx context.Context, docker dockerclient.Client, c *mongod.Container, script string) (string, error) {
	result, err := docker.Exec(ctx, c.ID(), mongoshCommand(c, script)...)
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("mongosh exited with code %d: %s", result.ExitCode, strings.TrimSpace(result.Stdout+" "+result.Stderr))
	}
	return strings.TrimSpace(result.Stdout), nil
}
