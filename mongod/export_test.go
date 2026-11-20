package mongod

import (
	"strings"

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
