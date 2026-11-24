package mongod_test

import (
	"os"
	"testing"

	"github.com/tophergopher/dbtest/mongod"
)

// callerHostIP is DBTEST_HOST_IP as the runner set it, before TestMain
// pinned it.
var callerHostIP string

// The tests assert on images and timeouts that MONGOTEST_* variables also
// set, so a runner that sets one would fail the suite for no fault of the
// code. They are cleared for the whole binary because examples have no
// *testing.T; a test about the environment uses t.Setenv.
//
// The dial address is pinned to loopback, where the doubles listen, so that
// a mocked start gives the same result on a laptop and inside a container.
// WithHostIP still overrides it. A test of where real containers are
// reached puts back the value the runner set, kept in callerHostIP.
func TestMain(m *testing.M) {
	for _, name := range []string{mongod.EnvImage, mongod.EnvStartTimeout} {
		if err := os.Unsetenv(name); err != nil {
			panic("mongod tests: cannot clear " + name + ": " + err.Error())
		}
	}
	callerHostIP = os.Getenv(mongod.EnvHostIP)
	if err := os.Setenv(mongod.EnvHostIP, "127.0.0.1"); err != nil {
		panic("mongod tests: cannot set " + mongod.EnvHostIP + ": " + err.Error())
	}
	os.Exit(m.Run())
}
