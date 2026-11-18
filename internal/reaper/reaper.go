package reaper

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

// reapTimeout bounds the reap a signal triggers, so one container that cannot
// be removed cannot keep a dying process alive. Only tests change it, and
// doc.go states its value.
var reapTimeout = 30 * time.Second

// reRaiseGrace is how long to wait for a re-raised signal to end the
// process before exiting directly.
const reRaiseGrace = 5 * time.Second

// runningPoll is how often the signal path looks again while a Reap started
// elsewhere is still running its teardowns.
const runningPoll = 10 * time.Millisecond

// ErrReap is matched by the error Reap returns when any teardown failed.
var ErrReap = errors.New("one or more teardowns failed")

// errPanicked is why a teardown that panicked failed.
var errPanicked = errors.New("the teardown panicked")

// Handle identifies one registration. Handles are never reused.
type Handle int64

// entry is one registered teardown. The name is for error messages.
type entry struct {
	handle Handle
	name   string
	tear   func(context.Context) error
	log    Logger
}

// Logger is told about each container that could not be removed after a
// signal. dockerclient.Logger, and so *slog.Logger, satisfies it.
type Logger interface {
	Error(msg string, keysAndValues ...any)
}

// registry is the process-wide set of teardowns. It is global because a
// signal arrives once for the process, not once per caller.
var registry struct {
	mu      sync.Mutex
	entries []entry // in registration order
	// running holds teardowns a Reap has taken and that have not returned.
	running []entry
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
// be close to its deadline, so it should not retry at length. If a signal
// arrives and this teardown fails or does not finish, log gets an Error
// record about it. log may be nil.
func Register(name string, tear func(context.Context) error, log Logger) Handle {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.next++
	registry.entries = append(registry.entries, entry{handle: registry.next, name: name, tear: tear, log: log})
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
// The teardowns run at the same time, so a stuck one cannot use up the time
// the others need. Reap returns when ctx ends, even if a teardown ignores
// ctx; that teardown keeps running in the background. The error is a
// [ReapError] naming each failure, including a panic and a teardown that had
// not finished. A failed teardown is not kept for a second attempt.
func Reap(ctx context.Context) error {
	entries := takeEntries()
	outcomes := make(chan outcome, len(entries))
	for i, e := range entries {
		go func() { outcomes <- outcome{i, tearDown(ctx, e)} }()
	}
	failures := collect(ctx, entries, outcomes)
	failures = slices.DeleteFunc(failures, func(f *TeardownError) bool { return f == nil })
	if len(failures) == 0 {
		return nil
	}
	return &ReapError{Failures: failures}
}

// outcome is the result of one teardown in a Reap: its failure, or nil.
type outcome struct {
	index  int // position in the entries the Reap took
	failed *TeardownError
}

// collect returns each teardown's failure, or nil, by its position in
// entries. It waits until every teardown has returned or ctx ends. A
// teardown still running when ctx ends is reported as failed with the
// context's cause, because its container may still be there.
func collect(ctx context.Context, entries []entry, outcomes <-chan outcome) []*TeardownError {
	failures := make([]*TeardownError, len(entries))
	finished := make([]bool, len(entries))
	record := func(o outcome) { failures[o.index], finished[o.index] = o.failed, true }
	for range entries {
		select {
		case o := <-outcomes:
			record(o)
		case <-ctx.Done():
			// select picks at random among ready cases, so outcomes may be
			// waiting that would otherwise be reported as unfinished.
			drain(outcomes, record)
			for i, e := range entries {
				if !finished[i] {
					failures[i] = &TeardownError{Name: e.name, Err: context.Cause(ctx), handle: e.handle, log: e.log}
				}
			}
			return failures
		}
	}
	return failures
}

// drain records every outcome already sent, without waiting for more.
func drain(outcomes <-chan outcome, record func(outcome)) {
	for {
		select {
		case o := <-outcomes:
			record(o)
		default:
			return
		}
	}
}

// tearDown runs one teardown that takeEntries handed out, and returns its
// failure, or nil.
func tearDown(ctx context.Context, e entry) *TeardownError {
	defer finishOne(e.handle)
	if err := recovering(ctx, e.tear); err != nil {
		return &TeardownError{Name: e.name, Err: err, handle: e.handle, log: e.log}
	}
	return nil
}

// recovering runs a teardown and returns a panic in it as its error. Each
// teardown has a goroutine of its own, where a panic would end the process
// while the others were still removing things.
func recovering(ctx context.Context, tear func(context.Context) error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: %v", errPanicked, recovered)
		}
	}()
	return tear(ctx)
}

// takeEntries empties the registry and returns what was in it, and records
// each entry as running until finishOne is called for it. The registry keeps
// no reference to the returned slice, so the caller owns it.
func takeEntries() []entry {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	entries := registry.entries
	registry.entries = nil
	registry.running = append(registry.running, entries...)
	return entries
}

// finishOne records that a teardown taken by takeEntries has returned.
func finishOne(h Handle) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.running = slices.DeleteFunc(registry.running, func(e entry) bool { return e.handle == h })
}

// outstanding returns how many teardowns are registered, and how many a
// Reap is running right now.
func outstanding() (registered, running int) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return len(registry.entries), len(registry.running)
}

// unfinished returns the teardowns still registered or still running in a
// Reap, except those already in reported.
func unfinished(reported []*TeardownError) []entry {
	seen := make(map[Handle]bool, len(reported))
	for _, failed := range reported {
		seen[failed.handle] = true
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	var left []entry
	for _, e := range slices.Concat(registry.entries, registry.running) {
		if !seen[e.handle] {
			left = append(left, e)
		}
	}
	return left
}

// TeardownError names one thing a reap could not tear down, and why.
type TeardownError struct {
	Name string // what was registered: a container's name or id
	Err  error
	// handle tells apart two registrations with the same name.
	handle Handle
	// log is the logger the teardown was registered with, or nil.
	log Logger
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
	return "mongotest: " + strings.Join(lines, "\n") + "\nRemove what is left with: docker rm -fv " + strings.Join(names(e.Failures), " ")
}

// names returns the name of everything that failed.
func names(failures []*TeardownError) []string {
	all := make([]string, 0, len(failures))
	for _, failed := range failures {
		all = append(all, failed.Name)
	}
	return all
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

	// Deferred, so a logger that ends this goroutine, as t.FailNow does,
	// cannot keep the process from dying of the signal.
	defer reRaise(sig)
	ctx, cancel := context.WithTimeout(context.Background(), reapTimeout)
	defer cancel()
	reapAndReport(ctx, sig, os.Stderr)
}

// reapAndReport reaps until nothing is left or ctx ends, and then reports
// each container it could not remove. It writes to w first, because a dying
// process may not flush its logger, and then to each container's own logger.
func reapAndReport(ctx context.Context, sig os.Signal, w io.Writer) {
	left := reapLeavingBehind(ctx)
	if len(left.names()) > 0 {
		fmt.Fprintln(w, left.report(sig))
	}
	for _, failed := range left.failed {
		logLeftBehind(failed.log, failed.Name, sig, failed.Err)
	}
	for _, e := range left.unfinished {
		logLeftBehind(e.log, e.name, sig, errUnfinished)
	}
}

// errUnfinished is the error given for a container whose removal did not
// finish before ctx ended.
var errUnfinished = errors.New("its removal did not finish in time")

// logLeftBehind logs a container that could not be removed after a signal.
// It recovers if the logger panics, as one that writes to a finished test
// does, because the process must still re-raise the signal and die of it.
func logLeftBehind(log Logger, name string, sig os.Signal, err error) {
	if log == nil {
		return
	}
	defer func() { _ = recover() }()
	log.Error("A container could not be removed after a signal, so it may still be running. "+
		"Remove it by hand: docker rm -fv "+name, "container", name, "signal", sig.String(), "error", err)
}

// reapLeavingBehind reaps until nothing is left or ctx ends, and returns
// the containers that may still be running.
func reapLeavingBehind(ctx context.Context) leftBehind {
	failed := reapUntilEmpty(ctx)
	return leftBehind{failed: failed, unfinished: unfinished(failed)}
}

// leftBehind is what a dying process could not remove: the teardowns that
// failed, and those that had not finished when its time ran out. An
// unfinished teardown either never started or is still running in another
// Reap.
type leftBehind struct {
	failed     []*TeardownError
	unfinished []entry
}

// names returns every container that may still be running.
func (l leftBehind) names() []string {
	all := names(l.failed)
	for _, e := range l.unfinished {
		all = append(all, e.name)
	}
	return all
}

// report says which containers are left, why, and the command that removes
// them.
func (l leftBehind) report(sig os.Signal) string {
	var b strings.Builder
	fmt.Fprintf(&b, "mongotest: stopped by %v, but %d container(s) could not be removed within %s and may still be running:\n",
		sig, len(l.names()), reapTimeout)
	for _, failed := range l.failed {
		fmt.Fprintf(&b, "  %s: %v\n", failed.Name, failed.Err)
	}
	for _, e := range l.unfinished {
		fmt.Fprintf(&b, "  %s: %v\n", e.name, errUnfinished)
	}
	b.WriteString("Remove them with: docker rm -fv " + strings.Join(l.names(), " "))
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
	registry.running = nil
	registry.mu.Unlock()
	if ch != nil {
		signal.Stop(ch)
		close(ch)
	}
}
