package mongod_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/internal/hostaddr"
	"github.com/tophergopher/mongotest/mongod"
)

// EnvHostIP is spelled out in this package so its documentation shows the
// name; the address is read in another package.
func TestEnvHostIPIsTheVariableTheAddressIsReadFrom(t *testing.T) {
	t.Setenv(mongod.EnvHostIP, "10.1.2.3")

	address, err := hostaddr.Resolve("tcp://build-host:2376", "")

	require.NoError(t, err, "an address set in the environment needs no lookup")
	assert.Equal(t, "10.1.2.3", address.Host, "setting the variable mongod names changes where containers are reached")
}
