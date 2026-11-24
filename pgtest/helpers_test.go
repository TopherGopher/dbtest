package pgtest_test

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// The integration tests start real containers. They need a Docker daemon,
// and fail rather than skip without one.

// containerExists asks the docker CLI whether a container is still there.
func containerExists(t *testing.T, id string) bool {
	t.Helper()
	out, err := exec.Command("docker", "ps", "-aq", "--no-trunc", "--filter", "id="+id).Output()
	require.NoError(t, err, "the docker CLI lists containers")
	return strings.TrimSpace(string(out)) != ""
}

// databaseExists asks the server whether it has a database of that name.
func databaseExists(t *testing.T, pool *pgxpool.Pool, name string) bool {
	t.Helper()
	var exists bool
	err := pool.QueryRow(context.Background(), "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&exists)
	require.NoError(t, err, "the server lists its databases")
	return exists
}

// recorder stands in for a test. It keeps the cleanups a helper registers and
// the failures and skips it reports, so that a test can end it when it
// chooses and see what happened. Fatal and Skip end the goroutine, as they
// do a test's, so call them through inGoroutine. It answers to name, if it is
// given one.
type recorder struct {
	testing.TB
	name     string
	cleanups []func()
	failures []string
	failed   bool
	skipped  bool
}

func (r *recorder) Name() string {
	if r.name == "" {
		return r.TB.Name()
	}
	return r.name
}

func (r *recorder) Cleanup(f func()) { r.cleanups = append(r.cleanups, f) }

func (r *recorder) Fail() { r.failed = true }

func (r *recorder) Error(args ...any) { r.failures = append(r.failures, fmt.Sprint(args...)) }

func (r *recorder) Errorf(format string, args ...any) {
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
}

func (r *recorder) FailNow() { r.Fail(); runtime.Goexit() }

func (r *recorder) Fatal(args ...any) { r.Error(args...); runtime.Goexit() }

func (r *recorder) Fatalf(format string, args ...any) { r.Errorf(format, args...); runtime.Goexit() }

func (r *recorder) Failed() bool { return r.failed || len(r.failures) > 0 }

func (r *recorder) SkipNow() { r.skipped = true; runtime.Goexit() }

func (r *recorder) Skip(...any) { r.SkipNow() }

func (r *recorder) Skipf(string, ...any) { r.SkipNow() }

func (r *recorder) Skipped() bool { return r.skipped }

// end runs the cleanups, last first, as the end of a test does.
func (r *recorder) end() {
	for i := len(r.cleanups) - 1; i >= 0; i-- {
		inGoroutine(r.cleanups[i])
	}
}

// inGoroutine runs f in a goroutine of its own and waits for it, so that a
// recorder's Fatal or Skip ends f and not the caller.
func inGoroutine(f func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	<-done
}
