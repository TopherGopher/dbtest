package container_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tophergopher/dbtest/dockerclient"
	"github.com/tophergopher/dbtest/dockermock"
	"github.com/tophergopher/dbtest/internal/container"
	"github.com/tophergopher/dbtest/internal/reaper"
)

// The limit is for the whole process, so these tests set it and put the
// previous one back. No test in this package runs in parallel.

// limitStarts sets the limit for one test.
func limitStarts(t *testing.T, n int) {
	t.Helper()
	previous := container.SetMaxConcurrentStarts(n)
	t.Cleanup(func() { container.SetMaxConcurrentStarts(previous) })
}

// shortContext bounds a start that should go ahead, so that a broken limit
// fails the test quickly instead of hanging it.
func shortContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// readyDaemon is a mocked daemon whose container is ready as soon as it runs.
func readyDaemon() *dockermock.Mock {
	return daemon(func(int) (dockerclient.ContainerInspect, error) { return inspected(""), nil })
}

// holdTurn starts a container whose hook blocks until the returned function
// is called, so the start keeps its turn until then. hook is given a
// function that blocks, and installs it in the spec. The start's error
// arrives on the channel.
func holdTurn(t *testing.T, hook func(s *container.Spec, block func())) (release func(), done <-chan error) {
	t.Helper()
	held, reached := make(chan struct{}), make(chan struct{})
	signal := sync.OnceFunc(func() { close(reached) })
	s := spec(time.Minute)
	hook(&s, func() {
		signal()
		<-held
	})
	errs := make(chan error, 1)
	go func() {
		_, err := newContainer(readyDaemon()).Start(context.Background(), s)
		errs <- err
	}()
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "the holding start never got its turn")
	}
	return func() { close(held) }, errs
}

// whileReady holds a start's turn in its readiness check.
func whileReady(s *container.Spec, block func()) {
	s.Ready = func(context.Context, string, int) error { block(); return nil }
}

// holdSlot starts a container that keeps its turn until release is called.
func holdSlot(t *testing.T) (release func(), done <-chan error) {
	t.Helper()
	return holdTurn(t, whileReady)
}

func TestAStartBeyondTheLimitWaitsForAFreeSlot(t *testing.T) {
	limitStarts(t, 1)
	release, first := holdSlot(t)

	m := readyDaemon()
	second := make(chan error, 1)
	go func() {
		_, err := newContainer(m).Start(shortContext(t), spec(time.Second))
		second <- err
	}()

	assert.Never(t, func() bool { return len(m.CallsTo("ContainerCreate")) > 0 }, 200*time.Millisecond, 10*time.Millisecond,
		"a start beyond the limit creates nothing while the limit is reached")
	release()
	require.NoError(t, <-first, "the start holding the slot succeeds")
	assert.NoError(t, <-second, "the waiting start goes ahead once the slot is free, instead of failing")
}

func TestAStartKeepsItsTurnUntilAfterReadyReturns(t *testing.T) {
	limitStarts(t, 1)
	release, first := holdTurn(t, func(s *container.Spec, block func()) {
		s.AfterReady = func(context.Context) error { block(); return nil }
	})

	m := readyDaemon()
	second := make(chan error, 1)
	go func() {
		_, err := newContainer(m).Start(shortContext(t), spec(time.Second))
		second <- err
	}()

	assert.Never(t, func() bool { return len(m.CallsTo("ContainerCreate")) > 0 }, 200*time.Millisecond, 10*time.Millisecond,
		"work after ready, such as initiating a replica set, still counts as starting")
	release()
	require.NoError(t, <-first, "the start holding the slot succeeds")
	assert.NoError(t, <-second, "the waiting start goes ahead once AfterReady returns")
}

func TestWaitingForASlotDoesNotUseUpTheStartTimeout(t *testing.T) {
	limitStarts(t, 1)
	release, first := holdSlot(t)
	time.AfterFunc(300*time.Millisecond, release)
	s := spec(100 * time.Millisecond)
	s.Ready = func(ctx context.Context, _ string, _ int) error { return ctx.Err() }

	_, err := newContainer(readyDaemon()).Start(shortContext(t), s)

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

	_, err := container.New("pg-waiting", m, dockerclient.NopLogger()).Start(ctx, spec(time.Minute))

	require.ErrorIs(t, err, container.ErrNotReady, "the start did not get as far as being ready")
	assert.ErrorIs(t, err, context.DeadlineExceeded, "the caller's context is what ended it")
	var notReady *container.NotReadyError
	require.ErrorAs(t, err, &notReady, "with the details")
	assert.True(t, notReady.ContextEnded, "a longer start timeout would not have helped")
	assert.Empty(t, m.CallsTo("ContainerCreate"), "nothing was created")
	assert.Equal(t, 1, container.StartingWhenItEnded(notReady), "the error counts the holder, not the start that was waiting")
	assert.Equal(t, 1, container.Starting(), "and the start that gave up took no slot")
	assert.NotContains(t, reaper.Names(), "pg-waiting", "a container never created is not kept for the reaper")
}

// select chooses at random between a free slot and an ended context, so
// this tries enough times that a start which ignored the context would be
// caught.
func TestAContextThatHasAlreadyEndedStartsNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := readyDaemon()

	for range 20 {
		_, err := newContainer(m).Start(ctx, spec(time.Minute))
		require.ErrorIs(t, err, context.Canceled, "the caller's context is what ended it")
	}

	assert.Empty(t, m.CallsTo("ContainerCreate"), "no container was created, though a slot was free")
}

// A start that runs out of time while other starts filled the limit may
// have been slowed by them, so the image's package is told, to advise fewer
// at once rather than a wrong address. The error counts every start.
func TestTheAdviceIsToldWhenOtherStartsFilledTheLimit(t *testing.T) {
	cases := map[string]struct {
		limit, others, wantStarting int
		wantCrowded                 bool
	}{
		"others filled the limit":         {limit: 2, others: 1, wantStarting: 2, wantCrowded: true},
		"room to spare":                   {limit: 3, others: 1, wantStarting: 2, wantCrowded: false},
		"a lone start at a limit of one":  {limit: 1, others: 0, wantStarting: 1, wantCrowded: false},
		"a lone start with room to spare": {limit: 2, others: 0, wantStarting: 1, wantCrowded: false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			limitStarts(t, tc.limit)
			for range tc.others {
				release, done := holdSlot(t)
				t.Cleanup(func() { release(); <-done })
			}
			s := spec(100 * time.Millisecond)
			s.Ready = func(context.Context, string, int) error { return errors.New("nothing answered") }
			var crowded bool
			s.Advice = func(_ error, filled bool) string {
				crowded = filled
				return ""
			}

			_, err := newContainer(readyDaemon()).Start(shortContext(t), s)

			var notReady *container.NotReadyError
			require.ErrorAs(t, err, &notReady, "the start runs out of time")
			assert.Equal(t, tc.wantStarting, container.StartingWhenItEnded(notReady), "the error counts every start under way, this one included")
			assert.Equal(t, tc.wantCrowded, crowded, "the advice is told only when others filled the limit")
		})
	}
}

func TestALimitBelowOneRestoresTheDefault(t *testing.T) {
	limitStarts(t, 1)
	container.SetMaxConcurrentStarts(0)
	release, first := holdSlot(t)
	defer func() { release(); <-first }()

	_, err := newContainer(readyDaemon()).Start(shortContext(t), spec(time.Second))

	assert.NoError(t, err, "the default lets more than one start run at once")
}

func TestSettingTheLimitReturnsThePreviousOne(t *testing.T) {
	original := container.SetMaxConcurrentStarts(2)

	assert.Equal(t, 2, container.SetMaxConcurrentStarts(3), "the limit just set is returned")
	assert.Equal(t, 3, container.SetMaxConcurrentStarts(original), "so a caller can put back what was there")
	assert.Equal(t, original, container.SetMaxConcurrentStarts(original), "and the original is back")
}
