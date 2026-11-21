package mongod

import (
	"context"
	"strings"

	"github.com/tophergopher/dbtest/dockerclient"
	"github.com/tophergopher/dbtest/internal/hostaddr"

	"github.com/tophergopher/dbtest/internal/pull"
)

// This file is compiled only into the tests. It lets a test outside the
// package assert on a private error or decision, where it would otherwise
// have to read the wording of a message.

// Why a hello did not find mongod ready.
var (
	ErrNoReply      = errNoReply
	ErrNotMongod    = errNotMongod
	ErrHelloRefused = errHelloRefused
	ErrAuthUnknown  = errAuthUnknown
)

// AddressInDoubt reports whether the error tells the caller how to correct
// the address, by naming EnvHostIP.
func AddressInDoubt(err error) bool { return strings.Contains(err.Error(), EnvHostIP) }

// The pull hooks live with the pulls; see the internal/pull package.
var (
	PullWaiters        = pull.Waiters
	SetPullWait        = pull.SetWait
	ForgetSpentLimits  = pull.ForgetSpentLimits
	SetRateLimitMemory = pull.SetRateLimitMemory
)

// NotReadyAdvice is the advice a start that ran out of time gives, after
// lastErr.
func NotReadyAdvice(lastErr error) string { return config{}.notReadyAdvice(lastErr) }

// NetworkLookup finds the network a start shares with this process's
// container. A test replaces it to describe running in a container.
type NetworkLookup = func(context.Context, dockerclient.Client) (hostaddr.SharedNetwork, error)

// WithNetworkLookup replaces the lookup a start makes for a shared network.
func (o Options) WithNetworkLookup(lookup NetworkLookup) Options {
	o.findNetwork = lookup
	return o
}
