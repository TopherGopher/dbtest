package mongod_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tophergopher/dbtest/dockerclient"
	"github.com/tophergopher/dbtest/dockermock"
	"github.com/tophergopher/dbtest/mongod"
)

// The limit on starts at once is for the whole process, so these tests set
// it and put the previous one back. No test in this package runs in
// parallel.

// limitStarts sets the limit for one test.
func limitStarts(t *testing.T, n int) {
	t.Helper()
	previous := mongod.SetMaxConcurrentStarts(n)
	t.Cleanup(func() { mongod.SetMaxConcurrentStarts(previous) })
}

// holdStart starts a container whose daemon does not finish starting it
// until release is called, so the start keeps its turn until then.
func holdStart(t *testing.T) (release func()) {
	t.Helper()
	first, _ := readyMock(t)
	held, starting := make(chan struct{}), make(chan struct{})
	first.ContainerStartFunc = func(context.Context, string) error {
		close(starting)
		<-held
		return nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := start(t, first, mongod.DefaultOptions())
		done <- err
	}()
	select {
	case <-starting:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "the first start never got its turn")
	}
	var released bool
	release = func() {
		if !released {
			released = true
			close(held)
			assert.NoError(t, <-done, "the start that held its turn succeeds")
		}
	}
	t.Cleanup(release)
	return release
}

// startShortly starts a container with a short deadline, so a broken limit
// fails the test quickly instead of hanging it.
func startShortly(t *testing.T, m *dockermock.Mock, opts mongod.Options) (*mongod.Container, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := mongod.StartWithOptions(ctx, opts.WithDocker(m))
	if err == nil {
		t.Cleanup(func() { _ = c.Stop(context.Background()) })
	}
	return c, err
}

func TestAStartBeyondTheLimitWaitsForAnotherToFinish(t *testing.T) {
	limitStarts(t, 1)
	release := holdStart(t)

	second, _ := readyMock(t)
	secondDone := make(chan error, 1)
	go func() {
		_, err := startShortly(t, second, mongod.DefaultOptions())
		secondDone <- err
	}()

	assert.Never(t, func() bool { return len(second.CallsTo("ContainerCreate")) > 0 }, 200*time.Millisecond, 10*time.Millisecond,
		"the second start creates nothing while the first holds the only turn")
	release()
	assert.NoError(t, <-secondDone, "the second goes ahead once the first is done, instead of failing")
}

// notAnswering is a mocked daemon whose container runs, but where nothing
// answers at the published port, as for a wrong address.
func notAnswering(t *testing.T) *dockermock.Mock {
	t.Helper()
	m, _ := readyMock(t)
	dead := closedPort(t)
	m.ContainerInspectFunc = func(context.Context, string) (dockerclient.ContainerInspect, error) {
		return running(dead), nil
	}
	return m
}

// When other starts filled the limit as the time ran out, they may be what
// slowed this one, which looks just like a wrong address. The error says how
// to start fewer, and does not blame the address.
func TestAStartThatRanOutOfTimeWhileOthersFilledTheLimitBlamesTheLoad(t *testing.T) {
	limitStarts(t, 2)
	holdStart(t)

	_, err := startShortly(t, notAnswering(t), mongod.DefaultOptions().WithHostIP("127.0.0.1").WithStartTimeout(100*time.Millisecond))

	require.ErrorIs(t, err, mongod.ErrNoReply, "nothing answered, as for a wrong address")
	assert.Contains(t, err.Error(), mongod.CrowdedAdvice, "the error advises starting fewer at once")
	assert.False(t, mongod.AddressInDoubt(err), "and does not send the caller looking for a wrong address")
}

// A lower limit is what a caller tries first to rule out load. A start that
// was alone is not told to start fewer, so a wrong address still shows.
func TestALoneStartThatRanOutOfTimeStillDoubtsTheAddress(t *testing.T) {
	limitStarts(t, 1)

	_, err := startShortly(t, notAnswering(t), mongod.DefaultOptions().WithHostIP("127.0.0.1").WithStartTimeout(100*time.Millisecond))

	require.ErrorIs(t, err, mongod.ErrNoReply, "nothing answered, as for a wrong address")
	assert.True(t, mongod.AddressInDoubt(err), "the error says how to correct the address")
}
