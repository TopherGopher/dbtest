package pgtest

import (
	"errors"
	"fmt"
)

// ErrNotConnected is returned when a client could not connect to the server.
// It wraps pgx's error, which never holds the password. Run and Start can
// also return errors from the postgres package, such as postgres.ErrNotReady,
// and from the dockerclient package, such as dockerclient.ErrConnectionFailed.
var ErrNotConnected = errors.New("pgtest: cannot connect to the server")

// ErrEmptyURI is returned by Attach for an empty connection string, which
// pgx would otherwise fill in from the PG* environment variables.
var ErrEmptyURI = errors.New("pgtest: the URI given to Attach is empty. Pass a connection string, such as the value of DATABASE_URL")

// ErrConnectionsHeld is returned by Stop, and reported when a test ends,
// when the code under test still holds a connection. The error matches it
// and gives the count.
var ErrConnectionsHeld = errors.New("pgtest: connections are still in use")

// heldError says how many connections were still in use when a pool was
// closed. It matches ErrConnectionsHeld.
type heldError struct{ count int32 }

func (e *heldError) Error() string {
	connections := fmt.Sprintf("%d connections were", e.count)
	if e.count == 1 {
		connections = "1 connection was"
	}
	return "pgtest: " + connections + " still in use when the clients were closed. " +
		"Close every Rows, Scan every QueryRow, and commit or roll back every Tx"
}

func (e *heldError) Is(target error) bool { return target == ErrConnectionsHeld }
