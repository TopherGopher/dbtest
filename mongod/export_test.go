package mongod

// This file is compiled only into the tests. It lets a test outside the
// package assert on a private error or decision, where it would otherwise
// have to read the wording of a message.

// Why a hello did not find mongod ready.
var (
	ErrNoReply      = errNoReply
	ErrNotMongod    = errNotMongod
	ErrHelloRefused = errHelloRefused
)

// AddressInDoubt reports whether the error advises correcting the address.
func (e *NotReadyError) AddressInDoubt() bool { return e.addressInDoubt() }

// ExitOutputLimit is how much of an exited container's output is kept.
const ExitOutputLimit = exitOutputLimit
