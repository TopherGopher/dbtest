package reaper_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// helperCoverDirEnv names the environment variable that turns on coverage
// for the helper programs. go test does not count code that runs in a
// separate program. When the variable holds a directory, the helpers are
// built with -cover and write their counts there, and CI adds them to
// coverage.out.
const helperCoverDirEnv = "DBTEST_HELPER_COVERDIR"

// buildProgram compiles pkg for a test that runs it as a separate process. It
// is built rather than run through `go run`, because `go run` would receive
// the test's signal itself and report its own exit status, not the
// program's. When the variable in helperCoverDirEnv is set, the program
// records coverage of every package in this module, in the same mode as CI's
// coverage.out.
func buildProgram(t *testing.T, pkg string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), filepath.Base(pkg))
	args := []string{"build", "-o", binary}
	if os.Getenv(helperCoverDirEnv) != "" {
		args = append(args, "-cover", "-covermode=atomic", "-coverpkg=github.com/tophergopher/dbtest/...")
	}
	build := exec.Command("go", append(args, pkg)...)
	build.Stderr = os.Stderr
	require.NoError(t, build.Run(), "the helper binary must build")
	return binary
}

// programEnv returns this process's environment plus vars. When helper
// coverage is on, it also sets GOCOVERDIR so the program writes its counts
// to that directory.
func programEnv(vars ...string) []string {
	env := append(os.Environ(), vars...)
	if dir := os.Getenv(helperCoverDirEnv); dir != "" {
		env = append(env, "GOCOVERDIR="+dir)
	}
	return env
}
