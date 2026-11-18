package reaper

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetForTest empties the registry and stops any handler, so that one test
// cannot see another's registrations. The registry is deliberately global --
// that is the whole point of a reaper, since a signal arrives once for the
// process and not once per caller -- so tests have to clean up after
// themselves rather than getting a fresh instance each.
func resetForTest(t *testing.T) {
	t.Helper()
	stopForTest()
	t.Cleanup(stopForTest)
}

func TestReapRunsEveryTeardown(t *testing.T) {
	resetForTest(t)
	var mu sync.Mutex
	var torn []string
	tear := func(name string) func(context.Context) error {
		return func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			torn = append(torn, name)
			return nil
		}
	}
	Register("first", tear("first"), nil)
	Register("second", tear("second"), nil)
	Register("third", tear("third"), nil)

	require.NoError(t, Reap(context.Background()), "every teardown succeeds, so the reap does")

	assert.ElementsMatch(t, []string{"first", "second", "third"}, torn, "every registration is torn down, in no promised order")
	assert.Empty(t, Names(), "a reaped registration is gone")
}

func TestReapReportsEveryFailureAndStillRunsTheRest(t *testing.T) {
	resetForTest(t)
	first := errors.New("first cannot be removed")
	second := errors.New("second cannot be removed")
	var ran atomic.Int32
	Register("ok-before", func(context.Context) error { ran.Add(1); return nil }, nil)
	Register("fails-one", func(context.Context) error { ran.Add(1); return first }, nil)
	Register("ok-between", func(context.Context) error { ran.Add(1); return nil }, nil)
	Register("fails-two", func(context.Context) error { ran.Add(1); return second }, nil)

	err := Reap(context.Background())

	require.Error(t, err, "the reap fails when any teardown fails")
	assert.ErrorIs(t, err, first, "the first failure is reported")
	assert.ErrorIs(t, err, second, "the second is reported too")
	assert.EqualValues(t, 4, ran.Load(), "every teardown runs")
}

func TestReapNamesWhatFailed(t *testing.T) {
	resetForTest(t)
	busy := errors.New("device or resource busy")
	Register("mongotest-1a2b3c4d", func(context.Context) error { return busy }, nil)

	err := Reap(context.Background())

	require.ErrorIs(t, err, busy, "the teardown's own error is kept, so a caller can tell why")
	var failed *TeardownError
	require.ErrorAs(t, err, &failed, "each failure is a value a caller can read")
	assert.Equal(t, "mongotest-1a2b3c4d", failed.Name, "and it names what is still running, so it can be removed by hand")
}

func TestReapListsFailuresInRegistrationOrder(t *testing.T) {
	resetForTest(t)
	failure := errors.New("cannot be removed")
	for _, name := range []string{"first", "second", "third"} {
		Register(name, func(context.Context) error { return failure }, nil)
	}

	err := Reap(context.Background())

	var reap *ReapError
	require.ErrorAs(t, err, &reap, "the reap reports what failed")
	var names []string
	for _, failed := range reap.Failures {
		names = append(names, failed.Name)
	}
	assert.Equal(t, []string{"first", "second", "third"}, names, "reported in the order they were started")
}

// One removal that never returns must not cost every other container: on a
// signal there is one deadline for the lot.
func TestOneStuckTeardownDoesNotStopTheOthers(t *testing.T) {
	resetForTest(t)
	const others = 4
	var removed atomic.Int32
	othersRemoved := make(chan struct{})
	stillStuck := errors.New("stuck until the others had been removed")
	// It gives way only once every other teardown has run, so the test does
	// not depend on how soon a loaded machine schedules them. The context is
	// what ends it if they never run.
	Register("stuck", func(ctx context.Context) error {
		select {
		case <-othersRemoved:
			return stillStuck
		case <-ctx.Done():
			return ctx.Err()
		}
	}, nil)
	for range others {
		Register("fine", func(context.Context) error {
			if removed.Add(1) == others {
				close(othersRemoved)
			}
			return nil
		}, nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := Reap(ctx)

	require.ErrorIs(t, err, stillStuck, "the stuck teardown is reported")
	assert.EqualValues(t, others, removed.Load(), "every other teardown ran")
}

// A teardown runs on a goroutine of its own, where a panic would end the
// process with the other containers half removed.
func TestATeardownThatPanicsIsReportedAndTheRestStillRun(t *testing.T) {
	resetForTest(t)
	removed := false
	Register("panics", func(context.Context) error { panic("nil map") }, nil)
	Register("fine", func(context.Context) error { removed = true; return nil }, nil)

	err := Reap(context.Background())

	var failed *TeardownError
	require.ErrorAs(t, err, &failed, "the panic is one container's failure, not the end of the process")
	assert.Equal(t, "panics", failed.Name, "and it names the container that may be left")
	assert.ErrorIs(t, err, errPanicked, "saying that its removal panicked")
	assert.True(t, removed, "the other container is removed all the same")
	registered, running := outstanding()
	assert.Zero(t, registered+running, "nothing is left counted as in progress")
}

// The process is dying, so what it writes is all the reader gets: it has to
// account for every container that may be left.
func TestWhatIsLeftBehindIsEveryFailureAndEverythingUnfinished(t *testing.T) {
	left := leftBehind{
		failed: []*TeardownError{
			{Name: "mongotest-1a2b3c4d", Err: context.DeadlineExceeded},
			{Name: "mongotest-5e6f7a8b", Err: context.DeadlineExceeded},
		},
		unfinished: []entry{{name: "mongotest-9c0d1e2f"}},
	}

	assert.Equal(t, []string{"mongotest-1a2b3c4d", "mongotest-5e6f7a8b", "mongotest-9c0d1e2f"}, left.names(),
		"failed and untried containers are both reported")
}

func TestASignalsReapWritesWhatItLeftBehind(t *testing.T) {
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	refuse := func(context.Context) error { return errors.New("the daemon refused") }
	remove := func(context.Context) error { return nil }
	cases := []struct {
		name     string
		ctx      context.Context
		teardown func(context.Context) error
		written  bool
	}{
		{"a removal that failed", context.Background(), refuse, true},
		{"a container there was no time left to try", ended, remove, true},
		{"nothing, when everything was removed", context.Background(), remove, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetForTest(t)
			Register("mongotest-1a2b3c4d", tc.teardown, nil)
			var out bytes.Buffer

			reapAndReport(tc.ctx, syscall.SIGINT, &out)

			assert.Equal(t, tc.written, out.Len() > 0, "a report is written only when a container may still be running")
		})
	}
}

func TestReapStopsAtTheContextDeadline(t *testing.T) {
	resetForTest(t)
	released := make(chan struct{})
	t.Cleanup(func() { close(released) })
	// The teardown ignores its context, as a removal stuck in a system call
	// would.
	Register("hangs", func(context.Context) error {
		<-released
		return nil
	}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	reaped := make(chan error, 1)
	go func() { reaped <- Reap(ctx) }()
	var err error
	select {
	case err = <-reaped:
	case <-time.After(5 * time.Second):
		require.Fail(t, "Reap returns when its context ends")
	}

	var failed *TeardownError
	require.ErrorAs(t, err, &failed, "the unfinished teardown is reported")
	assert.Equal(t, "hangs", failed.Name, "the error names the container that may be left")
	assert.ErrorIs(t, err, context.DeadlineExceeded, "the context deadline is what ended it")
}

func TestUnregisterKeepsAReapFromTouchingIt(t *testing.T) {
	resetForTest(t)
	torn := false
	handle := Register("stopped-already", func(context.Context) error { torn = true; return nil }, nil)

	assert.True(t, Unregister(handle), "unregistering a handle reports it")
	require.NoError(t, Reap(context.Background()), "there is nothing left to reap")

	assert.False(t, torn, "an unregistered container is not torn down")
	assert.False(t, Unregister(handle), "a second unregister finds nothing")
}

func TestNamesReportsWhatWouldBeReaped(t *testing.T) {
	resetForTest(t)
	Register("first", func(context.Context) error { return nil }, nil)
	second := Register("second", func(context.Context) error { return nil }, nil)

	assert.Equal(t, []string{"first", "second"}, Names(), "in registration order")

	Unregister(second)
	assert.Equal(t, []string{"first"}, Names(), "only what is still registered")
}

func TestHandlesAreNotReused(t *testing.T) {
	resetForTest(t)
	first := Register("first", func(context.Context) error { return nil }, nil)
	Unregister(first)
	second := Register("second", func(context.Context) error { return nil }, nil)

	assert.NotEqual(t, first, second,
		"a stale handle removes nothing newer")
}

func TestRegisterInstallsTheHandlerLazily(t *testing.T) {
	resetForTest(t)
	assert.False(t, Installed(), "nothing is installed before a registration")

	Register("first", func(context.Context) error { return nil }, nil)

	assert.True(t, Installed(), "the first registration installs the handler")
}

func TestRegisterIsSafeUnderConcurrentUse(t *testing.T) {
	resetForTest(t)
	const workers = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	torn := map[string]bool{}

	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := fmt.Sprintf("container-%02d", i)
			handle := Register(name, func(context.Context) error {
				mu.Lock()
				defer mu.Unlock()
				torn[name] = true
				return nil
			}, nil)
			// Half of them stop normally, as a passing test would.
			if i%2 == 0 {
				Unregister(handle)
			}
		}()
	}
	wg.Wait()

	require.NoError(t, Reap(context.Background()), "the reap succeeds")
	mu.Lock()
	defer mu.Unlock()
	assert.Len(t, torn, workers/2, "exactly what was never unregistered is torn down")
}

func TestReapWithNothingRegisteredIsNil(t *testing.T) {
	resetForTest(t)

	assert.NoError(t, Reap(context.Background()), "reaping nothing is not an error")
}

func TestReapUntilEmptyTearsDownWhatRegistersWhileItRuns(t *testing.T) {
	resetForTest(t)
	var late bool
	Register("first", func(context.Context) error {
		// What a parallel test does when a signal lands mid-start: its
		// container comes into existence while the reap is already running.
		Register("late", func(context.Context) error { late = true; return nil }, nil)
		return nil
	}, nil)

	require.Empty(t, reapUntilEmpty(context.Background()), "both teardowns succeed")

	assert.True(t, late, "a registration made during the reap is reaped too")
	assert.Empty(t, Names(), "nothing is left registered when the signal is re-raised")
}

func TestReapUntilEmptyGivesUpAtItsDeadline(t *testing.T) {
	resetForTest(t)
	const rounds = 3
	failure := errors.New("the daemon is not answering")
	// The context ends during the third round, so the test does not depend
	// on how many rounds a loaded machine fits into a length of time. Its
	// own deadline is what ends a reap that ignores the cancellation.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var attempts int
	var again func(context.Context) error
	again = func(context.Context) error {
		attempts++
		if attempts == rounds {
			cancel()
		}
		Register("again", again, nil) // something that keeps coming back
		return failure
	}
	Register("again", again, nil)

	failures := reapUntilEmpty(ctx)

	require.Len(t, failures, rounds, "it retries while there is time, and reports failures")
	assert.ErrorIs(t, failures[0], failure, "with its cause")
	assert.Equal(t, rounds, attempts, "no round starts after the deadline")
	assert.NotEmpty(t, Names(), "a teardown that keeps re-registering is left registered")
}

func TestReapUntilEmptyWaitsForAReapAlreadyRunning(t *testing.T) {
	resetForTest(t)
	started, release := make(chan struct{}), make(chan struct{})
	Register("slow", func(context.Context) error {
		close(started)
		<-release
		return nil
	}, nil)
	// What a TestMain does at the end of a run.
	go func() { _ = Reap(context.Background()) }()
	<-started

	// And a signal arriving while it is still removing things.
	done := make(chan struct{})
	go func() {
		_ = reapUntilEmpty(context.Background())
		close(done)
	}()

	select {
	case <-done:
		assert.Fail(t, "reapUntilEmpty waits for a teardown already running")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		assert.Fail(t, "the signal path must return once the running teardown has finished")
	}
}

// Reap promises not to keep a failed teardown for a second attempt: a
// second Reap would only fail the same way, and Names would keep reporting
// a container as leaked after its failure was already reported.
func TestAFailedTeardownIsNotKept(t *testing.T) {
	resetForTest(t)
	Register("mongotest-1a2b3c4d", func(context.Context) error { return errors.New("the daemon refused") }, nil)

	require.ErrorIs(t, Reap(context.Background()), ErrReap, "the failure is reported")

	assert.Empty(t, Names(), "a failed teardown is removed from the registry")
}

// A signal can arrive while a TestMain's Reap is still removing containers.
// If the signal's reap times out before that Reap finishes, the report has
// to name its containers too: the process is about to die, so the other Reap
// will never report them.
func TestWhatIsLeftBehindIncludesARemovalAnotherReapHasNotFinished(t *testing.T) {
	resetForTest(t)
	started, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(release) })
	Register("mongotest-1a2b3c4d", func(context.Context) error {
		close(started)
		<-release
		return nil
	}, nil)
	go func() { _ = Reap(context.Background()) }()
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	left := reapLeavingBehind(ctx)

	assert.Equal(t, []string{"mongotest-1a2b3c4d"}, left.names(), "the container whose removal had not finished may still be running")
}

// On a signal, a removal that ignores its context is reported as failed and
// is still running. It must be named once, or the docker rm command the
// report prints fails on the second copy.
func TestAStuckRemovalIsNamedOnce(t *testing.T) {
	resetForTest(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	Register("mongotest-1a2b3c4d", func(context.Context) error {
		<-release
		return nil
	}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	left := reapLeavingBehind(ctx)

	assert.Equal(t, []string{"mongotest-1a2b3c4d"}, left.names(), "a stuck removal is named once")
}
