package mongod

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A Docker client keeps connections to the daemon open and has no Close. If
// every start built its own, a suite of a hundred tests would hold a hundred
// connections until the process ended, and run out of file descriptors on a
// machine with a low limit.
//
// This reaches into the package because the shared client is not visible
// through anything a start returns.
func TestTheDefaultClientIsCreatedOnceAndShared(t *testing.T) {
	first, err := config{}.client()
	require.NoError(t, err, "a client can be built from the environment")
	second, err := config{}.client()
	require.NoError(t, err, "and asked for again")

	assert.Same(t, first, second, "starts with no client share one")
}
