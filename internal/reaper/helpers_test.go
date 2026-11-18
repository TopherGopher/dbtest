package reaper_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// helperCoverDir names the environment variable that asks for the helper
// programs' coverage. CI sets it to a directory, and merges what the helpers
// write there into the root module's coverage: a helper is a separate
// program, so go test does not count it.
const helperCoverDir = "MONGOTEST_HELPER_COVERDIR"

// buildProgram compiles pkg for a test that runs it as a separate process. It
// is built rather than run through `go run`, because `go run` sits between
// the signal and the process under test. When helperCoverDir is set, the
// program is built to record coverage of this module's packages.
func buildProgram(t *testing.T, pkg string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), filepath.Base(pkg))
	args := []string{"build", "-o", binary}
	if os.Getenv(helperCoverDir) != "" {
		args = append(args, "-cover", "-covermode=atomic", "-coverpkg=github.com/tophergopher/mongotest/...")
	}
	build := exec.Command("go", append(args, pkg)...)
	build.Stderr = os.Stderr
	require.NoError(t, build.Run(), "the helper binary must build")
	return binary
}

// programEnv is the environment for a helper program: this process's, the
// given variables, and GOCOVERDIR when helperCoverDir asks for coverage.
func programEnv(vars ...string) []string {
	env := append(os.Environ(), vars...)
	if dir := os.Getenv(helperCoverDir); dir != "" {
		env = append(env, "GOCOVERDIR="+dir)
	}
	return env
}
