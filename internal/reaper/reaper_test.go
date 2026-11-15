package reaper

import (
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
	Register("first", tear("first"))
	Register("second", tear("second"))
	Register("third", tear("third"))

	require.NoError(t, Reap(context.Background()), "every teardown succeeds, so the reap does")

	assert.ElementsMatch(t, []string{"first", "second", "third"}, torn, "every registration is torn down, in no promised order")
	assert.Empty(t, Names(), "a reaped registration is gone; leaving it would tear the same container down twice")
}

func TestReapReportsEveryFailureAndStillRunsTheRest(t *testing.T) {
	resetForTest(t)
	first := errors.New("first cannot be removed")
	second := errors.New("second cannot be removed")
	var ran atomic.Int32
	Register("ok-before", func(context.Context) error { ran.Add(1); return nil })
	Register("fails-one", func(context.Context) error { ran.Add(1); return first })
	Register("ok-between", func(context.Context) error { ran.Add(1); return nil })
	Register("fails-two", func(context.Context) error { ran.Add(1); return second })

	err := Reap(context.Background())

	require.Error(t, err, "two teardowns failed, so the reap did")
	assert.ErrorIs(t, err, first, "the first failure is reported")
	assert.ErrorIs(t, err, second, "and so is the second; a reaper that stopped at the first would leak everything after it")
	assert.EqualValues(t, 4, ran.Load(), "every teardown runs regardless, because the containers left behind are the thing being prevented")
}

func TestReapNamesWhatFailed(t *testing.T) {
	resetForTest(t)
	busy := errors.New("device or resource busy")
	Register("mongotest-1a2b3c4d", func(context.Context) error { return busy })

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
		Register(name, func(context.Context) error { return failure })
	}

	err := Reap(context.Background())

	var reap *ReapError
	require.ErrorAs(t, err, &reap, "the reap reports what failed")
	var names []string
	for _, failed := range reap.Failures {
		names = append(names, failed.Name)
	}
	assert.Equal(t, []string{"first", "second", "third"}, names, "teardowns run together, so the report is in the order things were started")
}

// One removal that never returns must not cost every other container: on a
// signal there is one deadline for the lot.
func TestOneStuckTeardownDoesNotStopTheOthers(t *testing.T) {
	resetForTest(t)
	var removed atomic.Int32
	Register("stuck", func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() })
	for range 4 {
		Register("fine", func(ctx context.Context) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			removed.Add(1)
			return nil
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := Reap(ctx)

	require.ErrorIs(t, err, context.DeadlineExceeded, "the stuck one is reported")
	assert.EqualValues(t, 4, removed.Load(), "and the rest were removed while it was stuck")
}

func TestTheSignalReportNamesWhatIsStillRunning(t *testing.T) {
	failures := []*TeardownError{
		{Name: "mongotest-1a2b3c4d", Err: context.DeadlineExceeded},
		{Name: "mongotest-5e6f7a8b", Err: context.DeadlineExceeded},
	}

	report := leftBehind(syscall.SIGINT, failures)

	assert.Contains(t, report, "mongotest-1a2b3c4d", "the process is dying, so this text is all the reader gets")
	assert.Contains(t, report, "mongotest-5e6f7a8b", "and it has to name every container to remove by hand")
}

func TestReapStopsAtTheContextDeadline(t *testing.T) {
	resetForTest(t)
	released := make(chan struct{})
	t.Cleanup(func() { close(released) })
	Register("hangs", func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-released:
			return nil
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := Reap(ctx)

	require.Error(t, err, "the teardown never finished, so the reap reports it rather than pretending")
	assert.ErrorIs(t, err, context.DeadlineExceeded, "the budget is what ended it")
	assert.Less(t, time.Since(started), 5*time.Second,
		"a reaper that can be hung by one stuck container is worse than none: the signal path gives it a deadline for exactly this reason")
}

func TestUnregisterKeepsAReapFromTouchingIt(t *testing.T) {
	resetForTest(t)
	torn := false
	handle := Register("stopped-already", func(context.Context) error { torn = true; return nil })

	assert.True(t, Unregister(handle), "the handle was registered, so unregistering it reports that it did something")
	require.NoError(t, Reap(context.Background()), "there is nothing left to reap")

	assert.False(t, torn, "a container its owner already stopped must not be torn down again; the second removal would be a 404 at best and another caller's container at worst")
	assert.False(t, Unregister(handle), "unregistering twice reports that the second call found nothing, which is how a double Stop stays silent")
}

func TestNamesReportsWhatWouldBeReaped(t *testing.T) {
	resetForTest(t)
	Register("first", func(context.Context) error { return nil })
	second := Register("second", func(context.Context) error { return nil })

	assert.Equal(t, []string{"first", "second"}, Names(), "Names is in registration order, because that is the order they were started in")

	Unregister(second)
	assert.Equal(t, []string{"first"}, Names(), "and it tracks what is actually still registered, which is what makes a leak diagnosable")
}

func TestHandlesAreNotReused(t *testing.T) {
	resetForTest(t)
	first := Register("first", func(context.Context) error { return nil })
	Unregister(first)
	second := Register("second", func(context.Context) error { return nil })

	assert.NotEqual(t, first, second,
		"handles are never reused, so a stale Unregister from a container that already stopped cannot remove a different container that started afterwards")
}

func TestRegisterInstallsTheHandlerLazily(t *testing.T) {
	resetForTest(t)
	assert.False(t, Installed(), "nothing is installed until something is registered: importing this package must not change how a binary responds to Ctrl-C")

	Register("first", func(context.Context) error { return nil })

	assert.True(t, Installed(), "the first registration installs the handler, because only then is there anything to reap")
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
			})
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
	assert.Len(t, torn, workers/2, "exactly the registrations nobody unregistered were torn down; parallel tests register and unregister from many goroutines at once, so the registry has to be correct under that and not merely race-free")
}

func TestReapWithNothingRegisteredIsNil(t *testing.T) {
	resetForTest(t)

	assert.NoError(t, Reap(context.Background()), "a process that started no containers has nothing to tear down, and a deferred reap should not have to check first")
}

func TestReapUntilEmptyTearsDownWhatRegistersWhileItRuns(t *testing.T) {
	resetForTest(t)
	var late bool
	Register("first", func(context.Context) error {
		// What a parallel test does when a signal lands mid-start: its
		// container comes into existence while the reap is already running.
		Register("late", func(context.Context) error { late = true; return nil })
		return nil
	})

	require.Empty(t, reapUntilEmpty(context.Background()), "both teardowns succeed")

	assert.True(t, late, "a signal does not stop the rest of the process, so something registered during the reap is still this process's to remove before it dies")
	assert.Empty(t, Names(), "nothing is left registered when the signal is re-raised")
}

func TestReapUntilEmptyGivesUpAtItsDeadline(t *testing.T) {
	resetForTest(t)
	failure := errors.New("the daemon is not answering")
	var attempts int
	var again func(context.Context) error
	again = func(context.Context) error {
		attempts++
		Register("again", again) // something that keeps coming back
		return failure
	}
	Register("again", again)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	failures := reapUntilEmpty(ctx)

	require.NotEmpty(t, failures, "what went wrong on the way is still reported")
	assert.ErrorIs(t, failures[0], failure, "with its cause")
	assert.Greater(t, attempts, 1, "it does go round again while there is time")
	assert.NotEmpty(t, Names(), "and stops at the deadline rather than holding a dying process open for something that never drains")
}

func TestReapUntilEmptyWaitsForAReapAlreadyRunning(t *testing.T) {
	resetForTest(t)
	started, release := make(chan struct{}), make(chan struct{})
	Register("slow", func(context.Context) error {
		close(started)
		<-release
		return nil
	})
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
		assert.Fail(t, "the signal path returned while a teardown was still running, so the process would die and leave it unfinished")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		assert.Fail(t, "the signal path must return once the running teardown has finished")
	}
}
