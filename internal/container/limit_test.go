package container_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tophergopher/dbtest/dockerclient"
	"github.com/tophergopher/dbtest/dockermock"
	"github.com/tophergopher/dbtest/internal/container"
)

// The start limit is for the whole process, so these tests set it and put
// the default back. No test in this package runs in parallel.

// limitStarts sets the limit for one test.
func limitStarts(t *testing.T, n int) {
	t.Helper()
	container.SetStartLimit(n)
	t.Cleanup(func() { container.SetStartLimit(0) })
}

// readyDaemon is a mocked daemon whose container is ready as soon as it runs.
func readyDaemon() *dockermock.Mock {
	return daemon(func(int) (dockerclient.ContainerInspect, error) { return inspected(""), nil })
}

// holdSlot starts a container that stays not ready until the returned
// function is called, so it holds its slot until then. Its start's error
// arrives on the channel.
func holdSlot(t *testing.T) (release func(), done <-chan error) {
	t.Helper()
	held, ready := make(chan struct{}), make(chan struct{})
	s := spec(time.Minute)
	s.Ready = func(context.Context, string, int) error {
		select {
		case <-ready:
		default:
			close(ready)
		}
		<-held
		return nil
	}
	errs := make(chan error, 1)
	go func() {
		_, err := newContainer(readyDaemon()).Start(context.Background(), s)
		errs <- err
	}()
	<-ready
	return func() { close(held) }, errs
}

func TestAStartBeyondTheLimitWaitsForAFreeSlot(t *testing.T) {
	limitStarts(t, 1)
	release, first := holdSlot(t)

	m := readyDaemon()
	second := make(chan error, 1)
	go func() {
		_, err := newContainer(m).Start(context.Background(), spec(time.Second))
		second <- err
	}()

	assert.Never(t, func() bool { return len(m.CallsTo("ContainerCreate")) > 0 }, 200*time.Millisecond, 10*time.Millisecond,
		"a start beyond the limit creates nothing while every slot is taken")
	release()
	require.NoError(t, <-first, "the start holding the slot succeeds")
	assert.NoError(t, <-second, "the waiting start goes ahead once the slot is free, instead of failing")
}

func TestWaitingForASlotDoesNotUseUpTheStartTimeout(t *testing.T) {
	limitStarts(t, 1)
	release, first := holdSlot(t)
	time.AfterFunc(300*time.Millisecond, release)

	_, err := newContainer(readyDaemon()).Start(context.Background(), spec(100*time.Millisecond))

	require.NoError(t, <-first, "the start holding the slot succeeds")
	assert.NoError(t, err, "the start timeout begins when the slot is taken, so a wait longer than it does not fail the start")
}

func TestAContextThatEndsWhileWaitingForASlotEndsTheStart(t *testing.T) {
	limitStarts(t, 1)
	release, first := holdSlot(t)
	t.Cleanup(func() { release(); <-first })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	m := readyDaemon()

	_, err := newContainer(m).Start(ctx, spec(time.Minute))

	require.ErrorIs(t, err, container.ErrNotReady, "the start did not get as far as being ready")
	assert.ErrorIs(t, err, context.DeadlineExceeded, "the caller's context is what ended it")
	var notReady *container.NotReadyError
	require.ErrorAs(t, err, &notReady, "with the details")
	assert.True(t, notReady.ContextEnded, "a longer start timeout would not have helped")
	assert.Empty(t, m.CallsTo("ContainerCreate"), "and nothing was created")
}

// A start that runs out of time while every slot is taken may have been
// slowed by the others, so the image's package is told, to advise fewer
// starts at once rather than a wrong address.
func TestTheAdviceIsToldWhetherEverySlotWasTaken(t *testing.T) {
	cases := map[string]struct {
		limit       int
		wantCrowded bool
	}{
		"every slot taken": {limit: 1, wantCrowded: true},
		"slots to spare":   {limit: 2, wantCrowded: false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			limitStarts(t, tc.limit)
			s := spec(100 * time.Millisecond)
			s.Ready = func(context.Context, string, int) error { return errors.New("nothing answered") }
			var crowded bool
			s.Advice = func(_ error, everySlotTaken bool) string {
				crowded = everySlotTaken
				return ""
			}

			_, err := newContainer(readyDaemon()).Start(context.Background(), s)

			var notReady *container.NotReadyError
			require.ErrorAs(t, err, &notReady, "the start runs out of time")
			assert.Equal(t, 1, notReady.Running, "the error says how many starts were running, this one included")
			assert.Equal(t, tc.wantCrowded, crowded, "the advice is told whether every slot was taken")
		})
	}
}

func TestALimitBelowOneRestoresTheDefault(t *testing.T) {
	limitStarts(t, 1)
	container.SetStartLimit(0)
	release, first := holdSlot(t)
	defer func() { release(); <-first }()

	_, err := newContainer(readyDaemon()).Start(context.Background(), spec(time.Second))

	assert.NoError(t, err, "the default lets more than one start run at once")
}
