package mongod

import (
	"context"
	"errors"
)

// initiateScript makes mongod the only member of its replica set, then waits
// until it is primary, which is when it accepts writes. It checks the result
// itself: the legacy shell reports a failure in a value and carries on.
const initiateScript = `var r = rs.initiate(); if (!r.ok) throw new Error(tojson(r)); ` +
	`while (!db.hello().isWritablePrimary) sleep(50);`

// errNoPrimary is why a start ran out of time during initiation, inside the
// NotReadyError a caller sees.
var errNoPrimary = errors.New("the replica set had not accepted writes yet")

// initiateReplicaSet runs initiateScript inside the container. A flag makes
// mongod a member of a set, but the set exists only once a client has sent
// the command that initiates it, and the shell in the image is the client
// that is already there.
func (c *Container) initiateReplicaSet(ctx context.Context) error {
	c.logger.Debug("Initiating the replica set, so that mongod accepts writes.", "container", c.name, "replicaSet", c.replicaSet)
	result, err := c.runShell(ctx, initiateScript)
	switch {
	case errors.Is(err, ErrNoShell):
		return c.startError(OpInitiate, &notInitiatedError{reason: reasonNoShell, err: err})
	case err != nil:
		return c.startError(OpInitiate, &notInitiatedError{reason: "the daemon could not run a shell inside the container", err: err})
	case result.ExitCode != 0:
		return c.startError(OpInitiate, &notInitiatedError{reason: "the shell could not initiate it", err: result.failure()})
	}
	return nil
}

// reasonNoShell is why an image with no MongoDB shell cannot have a replica
// set, and the other way out. The cause that follows it says which image to
// use instead.
const reasonNoShell = "Start runs a shell inside the container to call rs.initiate(), " +
	"so leave out WithReplicaSet for a standalone mongod"

// notInitiatedError says why a replica set was not initiated. It matches
// ErrNotInitiated and whatever caused it.
type notInitiatedError struct {
	reason string
	err    error
}

func (e *notInitiatedError) Error() string {
	if e.err == nil {
		return e.reason
	}
	return e.reason + ": " + e.err.Error()
}

func (e *notInitiatedError) Unwrap() error { return e.err }

func (e *notInitiatedError) Is(target error) bool { return target == ErrNotInitiated }
