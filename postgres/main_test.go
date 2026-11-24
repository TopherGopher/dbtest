package postgres_test

import (
	"os"
	"testing"

	"github.com/tophergopher/dbtest/postgres"
)

// The tests assert on images and timeouts that DBTEST_POSTGRES_* variables
// also set, so a runner that sets one would fail the suite for no fault of
// the code. They are cleared for the whole binary because examples have no
// *testing.T; a test about the environment uses t.Setenv.
//
// The dial address is pinned to loopback, where the stand-ins listen, so
// that a mocked start gives the same result on a laptop and inside a
// container. The integration tests put back the value the runner set.
func TestMain(m *testing.M) {
	for _, name := range []string{postgres.EnvImage, postgres.EnvStartTimeout} {
		if err := os.Unsetenv(name); err != nil {
			panic("postgres tests: cannot clear " + name + ": " + err.Error())
		}
	}
	callerHostIP = os.Getenv(postgres.EnvHostIP)
	if err := os.Setenv(postgres.EnvHostIP, "127.0.0.1"); err != nil {
		panic("postgres tests: cannot set " + postgres.EnvHostIP + ": " + err.Error())
	}
	os.Exit(m.Run())
}

// callerHostIP is DBTEST_HOST_IP as the runner set it, before TestMain
// pinned it.
var callerHostIP string
