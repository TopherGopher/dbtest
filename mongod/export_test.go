package mongod

import (
	"github.com/tophergopher/mongotest/internal/container"
	"github.com/tophergopher/mongotest/internal/pull"
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

// AddressInDoubt reports whether the error advises correcting the address.
func AddressInDoubt(e *NotReadyError) bool { return addressInDoubt(e.LastErr) }

// The pull hooks live with the pulls; see the internal/pull package.
var (
	PullWaiters        = pull.Waiters
	SetPullWait        = pull.SetWait
	ForgetSpentLimits  = pull.ForgetSpentLimits
	SetRateLimitMemory = pull.SetRateLimitMemory
)

// ExitOutputLimit is how much of an exited container's output is kept.
const ExitOutputLimit = container.ExitOutputLimit

// NotReadyAdvice is the advice a start that ran out of time gives, after
// lastErr.
func NotReadyAdvice(lastErr error) string { return config{}.notReadyAdvice(lastErr) }
