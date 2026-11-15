package reaper

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

// DefaultReapTimeout bounds the reap a signal triggers, so one container
// that will not remove cannot keep a dying process alive.
const DefaultReapTimeout = 30 * time.Second

// reRaiseGrace is how long to wait for a re-raised signal to end the
// process before exiting directly.
const reRaiseGrace = 5 * time.Second

// runningPoll is how often the signal path looks again while a Reap started
// elsewhere is still running its teardowns.
const runningPoll = 10 * time.Millisecond

// ErrReap is matched by the error Reap returns when any teardown failed.
var ErrReap = errors.New("one or more teardowns failed")

// Handle identifies one registration. Handles are never reused.
type Handle int64

// entry is one registered teardown. The name is for error messages.
type entry struct {
	handle Handle
	name   string
	tear   func(context.Context) error
}

// registry is the process-wide set of teardowns. It is global because a
// signal arrives once for the process, not once per caller.
var registry struct {
	mu      sync.Mutex
	entries []entry // in registration order
	// running counts teardowns a Reap has taken and not yet finished.
	running int
	next    Handle
	// handler is non-nil once a signal handler is installed.
	handler chan os.Signal
}

// signals are the ones handled: SIGINT and SIGTERM. SIGKILL cannot be
// caught, and SIGQUIT is left for Go's stack dump.
var signals = []os.Signal{syscall.SIGINT, syscall.SIGTERM}

// Register records a teardown to run on a signal or a Reap, and returns the
// handle that takes it back. The first call installs the signal handler, so
// importing this package alone changes nothing.
//
// name appears in the error if the teardown fails. The teardown's context may
// be close to its deadline, so it should not retry at length.
func Register(name string, tear func(context.Context) error) Handle {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.next++
	registry.entries = append(registry.entries, entry{handle: registry.next, name: name, tear: tear})
	if registry.handler == nil {
		installLocked(signals)
	}
	return registry.next
}

// Unregister takes a registration back, and reports whether it was still
// there. Call it once the thing has been torn down some other way.
func Unregister(h Handle) bool {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	i := slices.IndexFunc(registry.entries, func(e entry) bool { return e.handle == h })
	if i < 0 {
		return false
	}
	registry.entries = slices.Delete(registry.entries, i, i+1)
	return true
}

// Names returns what is registered, oldest first. It is for diagnostics: a
// test that leaks a container can say which.
func Names() []string {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	names := make([]string, 0, len(registry.entries))
	for _, e := range registry.entries {
		names = append(names, e.name)
	}
	return names
}

// Reap runs every registered teardown and empties the registry. It needs
// no signal: call it at the end of a TestMain.
//
// The teardowns run at the same time, so one that is stuck cannot use up the
// time the others need. The error is a [ReapError] naming each failure. A
// failed teardown is not kept for a second attempt.
func Reap(ctx context.Context) error {
	entries := takeEntries()
	failures := make([]*TeardownError, len(entries))
	var wg sync.WaitGroup
	for i, e := range entries {
		wg.Go(func() { failures[i] = tearDown(ctx, e) })
	}
	wg.Wait()
	failures = slices.DeleteFunc(failures, func(f *TeardownError) bool { return f == nil })
	if len(failures) == 0 {
		return nil
	}
	return &ReapError{Failures: failures}
}

// tearDown runs one teardown that takeEntries handed out, and returns its
// failure, or nil.
func tearDown(ctx context.Context, e entry) *TeardownError {
	defer finishOne()
	if err := e.tear(ctx); err != nil {
		return &TeardownError{Name: e.name, Err: err}
	}
	return nil
}

// takeEntries empties the registry and returns what was in it, counting
// each entry as running until finishOne is called for it. The registry keeps
// no reference to the slice, so the caller owns it.
func takeEntries() []entry {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	entries := registry.entries
	registry.entries = nil
	registry.running += len(entries)
	return entries
}

// finishOne records that a teardown taken by takeEntries has returned.
func finishOne() {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.running--
}

// outstanding returns how many teardowns are registered, and how many a
// Reap is running right now.
func outstanding() (registered, running int) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return len(registry.entries), registry.running
}

// TeardownError names one thing a reap could not tear down, and why.
type TeardownError struct {
	Name string // what was registered: a container's name or id
	Err  error
}

func (e *TeardownError) Error() string {
	return fmt.Sprintf("%s could not be removed and may still be running: %v", e.Name, e.Err)
}

func (e *TeardownError) Unwrap() error { return e.Err }

// ReapError reports that at least one teardown failed. It matches ErrReap
// and each individual failure.
type ReapError struct {
	Failures []*TeardownError // in the order they were registered
}

func (e *ReapError) Error() string {
	lines := make([]string, 0, len(e.Failures))
	for _, failed := range e.Failures {
		lines = append(lines, failed.Error())
	}
	return "mongotest: " + strings.Join(lines, "\n") + "\nRemove what is left with: docker rm -fv " + names(e.Failures)
}

// names returns what failed, as arguments for a docker command.
func names(failures []*TeardownError) string {
	all := make([]string, 0, len(failures))
	for _, failed := range failures {
		all = append(all, failed.Name)
	}
	return strings.Join(all, " ")
}

func (e *ReapError) Unwrap() []error {
	errs := make([]error, 0, len(e.Failures))
	for _, failed := range e.Failures {
		errs = append(errs, failed)
	}
	return errs
}

func (e *ReapError) Is(target error) bool { return target == ErrReap }

// Installed reports whether a signal handler is in place.
func Installed() bool {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return registry.handler != nil
}

// installLocked starts the handler. The caller holds the registry lock.
//
// A signal the process was started with ignored is left ignored: listening
// for it would turn a signal the process was meant to survive, such as
// SIGINT in a shell's background job, into a reap and an exit.
func installLocked(wanted []os.Signal) {
	active := filterSignals(wanted, signal.Ignored)
	if len(active) == 0 {
		return // nothing to listen for; the next Register tries again
	}
	ch := make(chan os.Signal, 1)
	registry.handler = ch
	signal.Notify(ch, active...)
	go reapOnSignal(ch)
}

// filterSignals returns the signals that are not ignored.
func filterSignals(wanted []os.Signal, ignored func(os.Signal) bool) []os.Signal {
	active := make([]os.Signal, 0, len(wanted))
	for _, sig := range wanted {
		if !ignored(sig) {
			active = append(active, sig)
		}
	}
	return active
}

// reapOnSignal waits for a signal, reaps, and then lets the signal end the
// process as it would have without a handler.
func reapOnSignal(ch chan os.Signal) {
	sig, ok := <-ch
	if !ok {
		return // the handler was removed, which only the tests do
	}
	// Stop handling first, so a second Ctrl-C kills at once. Only this
	// signal is reset: Reset undoes every package's Notify for it.
	signal.Stop(ch)
	signal.Reset(sig)

	ctx, cancel := context.WithTimeout(context.Background(), DefaultReapTimeout)
	defer cancel()
	if failures := reapUntilEmpty(ctx); len(failures) > 0 {
		// The process is about to die, so stderr is the only place left.
		fmt.Fprintln(os.Stderr, leftBehind(sig, failures))
	}
	reRaise(sig)
}

// leftBehind is what a dying process says about the containers it could not
// remove: which they are, why, and the command that removes them.
func leftBehind(sig os.Signal, failures []*TeardownError) string {
	var b strings.Builder
	fmt.Fprintf(&b, "mongotest: stopped by %v, but %d container(s) could not be removed within %s and may still be running:\n",
		sig, len(failures), DefaultReapTimeout)
	for _, failed := range failures {
		fmt.Fprintf(&b, "  %s: %v\n", failed.Name, failed.Err)
	}
	b.WriteString("Remove them with: docker rm -fv " + names(failures))
	return b.String()
}

// reapUntilEmpty reaps until nothing is registered and no teardown is still
// running, or the context ends. It returns every teardown that failed.
//
// One pass is not enough. A signal does not stop the rest of the process: a
// parallel test can register a new container while the first pass removes
// others, and a Reap that a TestMain started may still be part-way through.
func reapUntilEmpty(ctx context.Context) []*TeardownError {
	var failures []*TeardownError
	for ctx.Err() == nil {
		registered, running := outstanding()
		switch {
		case registered > 0:
			failures = append(failures, failuresOf(Reap(ctx))...)
		case running > 0:
			// Someone else's Reap holds what is left. Wait for it.
			select {
			case <-ctx.Done():
			case <-time.After(runningPoll):
			}
		default:
			return failures
		}
	}
	return failures
}

// failuresOf returns the teardowns a Reap reported as failed.
func failuresOf(err error) []*TeardownError {
	var reap *ReapError
	if errors.As(err, &reap) {
		return reap.Failures
	}
	return nil
}

// reRaise sends the signal to this process again, now that nothing handles
// it, so the process dies of what it was sent. If that somehow does not end
// it, it exits with the status a shell reports for a signal death.
func reRaise(sig os.Signal) {
	if p, err := os.FindProcess(os.Getpid()); err == nil {
		if err := p.Signal(sig); err == nil {
			time.Sleep(reRaiseGrace) // delivery is asynchronous
		}
	}
	os.Exit(exitCodeFor(sig))
}

// exitCodeFor returns the exit status a shell reports for dying of a signal.
func exitCodeFor(sig os.Signal) int {
	const signalExitBase = 128
	if s, ok := sig.(syscall.Signal); ok {
		return signalExitBase + int(s)
	}
	return 1
}

// stopForTest empties the registry and removes any handler, because the
// tests in this package share the one global registry.
func stopForTest() {
	registry.mu.Lock()
	ch := registry.handler
	registry.handler = nil
	registry.entries = nil
	registry.running = 0
	registry.mu.Unlock()
	if ch != nil {
		signal.Stop(ch)
		close(ch)
	}
}
