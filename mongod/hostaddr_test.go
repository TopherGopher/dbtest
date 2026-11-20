package mongod_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/dbtest/internal/hostaddr"
	"github.com/tophergopher/dbtest/mongod"
)

// EnvHostIP is spelled out in this package so its documentation shows the
// name; the address is read in another package.
func TestEnvHostIPIsTheVariableTheAddressIsReadFrom(t *testing.T) {
	t.Setenv(mongod.EnvHostIP, "10.1.2.3")

	address, err := hostaddr.Resolve("tcp://build-host:2376", "")

	require.NoError(t, err, "an address set in the environment needs no lookup")
	assert.Equal(t, "10.1.2.3", address.Host, "setting the variable mongod names changes where containers are reached")
}

// Callers set the address by the name the documentation gives, so the test
// spells it out rather than using the constant.
func TestTheAddressIsReadFromDBTEST_HOST_IP(t *testing.T) {
	t.Setenv("DBTEST_HOST_IP", "10.4.5.6")

	address, err := hostaddr.Resolve("tcp://build-host:2376", "")

	require.NoError(t, err, "an address set in the environment needs no lookup")
	assert.Equal(t, "10.4.5.6", address.Host, "containers are reached at the address given")
}
