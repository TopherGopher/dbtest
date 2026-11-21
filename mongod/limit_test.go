package mongod_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tophergopher/dbtest/dockerclient"
	"github.com/tophergopher/dbtest/mongod"
)

// The start limit is for the whole process, so these tests set it and put
// the default back. No test in this package runs in parallel.

// limitStarts sets the limit for one test.
func limitStarts(t *testing.T, n int) {
	t.Helper()
	mongod.SetStartLimit(n)
	t.Cleanup(func() { mongod.SetStartLimit(0) })
}

func TestAStartBeyondTheStartLimitWaitsForAnotherToFinish(t *testing.T) {
	limitStarts(t, 1)
	first, _ := readyMock(t)
	held, starting := make(chan struct{}), make(chan struct{})
	first.ContainerStartFunc = func(context.Context, string) error {
		close(starting)
		<-held
		return nil
	}
	firstDone := make(chan error, 1)
	go func() {
		_, err := start(t, first, mongod.DefaultOptions())
		firstDone <- err
	}()
	<-starting

	second, _ := readyMock(t)
	secondDone := make(chan error, 1)
	go func() {
		_, err := start(t, second, mongod.DefaultOptions())
		secondDone <- err
	}()

	assert.Never(t, func() bool { return len(second.CallsTo("ContainerCreate")) > 0 }, 200*time.Millisecond, 10*time.Millisecond,
		"the second start creates nothing while the first holds the only slot")
	close(held)
	require.NoError(t, <-firstDone, "the first start succeeds")
	assert.NoError(t, <-secondDone, "the second goes ahead once the first is done, instead of failing")
}

// When every start slot was taken as the time ran out, the other starts may
// be what slowed this one, which looks just like a wrong address. The error
// says to run fewer at once, and does not blame the address.
func TestAStartThatRanOutOfTimeWithEverySlotTakenBlamesTheLoad(t *testing.T) {
	limitStarts(t, 1)
	m, _ := readyMock(t)
	dead := closedPort(t)
	m.ContainerInspectFunc = func(context.Context, string) (dockerclient.ContainerInspect, error) {
		return running(dead), nil
	}

	_, err := start(t, m, mongod.DefaultOptions().WithHostIP("127.0.0.1").WithStartTimeout(100*time.Millisecond))

	require.ErrorIs(t, err, mongod.ErrNoReply, "nothing answered, as for a wrong address")
	var notReady *mongod.NotReadyError
	require.ErrorAs(t, err, &notReady, "the start ran out of time")
	assert.Equal(t, 1, notReady.Running, "the error says how many starts were under way")
	assert.True(t, mongod.AdvisesFewerStarts(notReady), "the error says how to run fewer starts at once")
	assert.False(t, mongod.AddressInDoubt(notReady), "and does not send the caller looking for a wrong address")
}
