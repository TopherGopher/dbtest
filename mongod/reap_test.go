package mongod_test

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/dbtest/dockerclient"
	"github.com/tophergopher/dbtest/internal/reaper"
	"github.com/tophergopher/dbtest/mongod"
)

func TestAContainerIsRegisteredForReapingUntilItIsStopped(t *testing.T) {
	m, _ := readyMock(t)
	c := mustStart(t, m, mongod.DefaultOptions())
	assert.Contains(t, reaper.Names(), c.Name(), "a started container is removed if the process is signalled")

	require.NoError(t, c.Stop(context.Background()), "stopping succeeds")
	assert.NotContains(t, reaper.Names(), c.Name(), "and a stopped one is not removed a second time")
}

func TestAContainerIsRegisteredBeforeMongodIsReady(t *testing.T) {
	var mu sync.Mutex
	var registeredDuringTheWait []string
	m, _ := servingMock(t, func(conn net.Conn) {
		mu.Lock()
		registeredDuringTheWait = reaper.Names() // while the start waits for this answer
		mu.Unlock()
		openMongod(conn)
	})

	c := mustStart(t, m, mongod.DefaultOptions())

	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, registeredDuringTheWait, c.Name(), "a signal during the wait removes the container")
}

func TestAFailedStartLeavesNothingRegistered(t *testing.T) {
	m, _ := readyMock(t)
	m.ContainerStartFunc = func(context.Context, string) error { return errors.New("no") }
	before := len(reaper.Names())

	_, err := start(t, m, mongod.DefaultOptions())

	require.Error(t, err, "the start fails")
	assert.Len(t, reaper.Names(), before, "and its container, already removed, is not kept for reaping")
}

func TestReapRunningContainersRemovesWhatIsStillUp(t *testing.T) {
	m, _ := readyMock(t)
	c := mustStart(t, m, mongod.DefaultOptions())

	require.NoError(t, mongod.ReapRunningContainers(context.Background()), "the reap succeeds")

	assert.Len(t, m.CallsTo("ContainerRemove"), 1, "the container nobody stopped is removed")
	assert.NotContains(t, reaper.Names(), c.Name(), "and no longer registered")
	assert.NoError(t, mongod.ReapRunningContainers(context.Background()), "a reap with nothing to do is fine")
}

func TestReapRunningContainersReportsWhatItCouldNotRemove(t *testing.T) {
	m, _ := readyMock(t)
	refused := errors.New("the daemon refused")
	m.ContainerRemoveFunc = func(context.Context, string, dockerclient.RemoveOptions) error { return refused }
	mustStart(t, m, mongod.DefaultOptions())

	err := mongod.ReapRunningContainers(context.Background())

	assert.ErrorIs(t, err, mongod.ErrReap, "a container left running is reported")
	assert.ErrorIs(t, err, refused, "with the daemon's own error")
}

func TestAFailedReapNamesTheContainerLeftRunning(t *testing.T) {
	m, _ := readyMock(t)
	m.ContainerRemoveFunc = func(context.Context, string, dockerclient.RemoveOptions) error {
		return errors.New("the daemon refused")
	}
	c := mustStart(t, m, mongod.DefaultOptions())

	err := mongod.ReapRunningContainers(context.Background())

	var failed *mongod.TeardownError
	require.ErrorAs(t, err, &failed, "each container left behind is a value a caller can read")
	assert.Equal(t, c.Name(), failed.Name, "and it carries the name to pass to docker rm")
	var all *mongod.ReapError
	require.ErrorAs(t, err, &all, "the whole report is readable too")
	assert.Len(t, all.Failures, 1, "with one entry for each container")
}
