package pgtest

import (
	"errors"
	"fmt"
)

// ErrNotConnected is returned when a client could not connect to the server.
// An error that matches it wraps pgx's error. pgx keeps the password out of
// its messages where it can, but a URI with a typo, such as an unescaped @ in
// the password, can show part of it.
//
// Start and StartWithOptions can also return errors from the postgres
// package, such as postgres.ErrNotReady, and from the dockerclient package,
// such as dockerclient.ErrConnectionFailed.
var ErrNotConnected = errors.New("pgtest: cannot connect to the server")

// ErrInvalidURI is returned by Attach for a URI that is empty or that pgx
// cannot read. The error never includes pgx's reason, which can quote part
// of the password.
var ErrInvalidURI = errors.New("pgtest: the URI given to Attach is empty or cannot be read. " +
	"Pass one such as postgres://user:password@host:5432/database, and if it comes from a variable such as DATABASE_URL, check that the variable is set")

// ErrConnectionsHeld means the code under test still held a connection when
// the clients were closed. Stop returns it, and Run and DatabaseFor fail the
// test with it. The message gives the count.
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
		"Close every Rows, Scan every QueryRow, commit or roll back every Tx, and release every Conn"
}

func (e *heldError) Is(target error) bool { return target == ErrConnectionsHeld }
