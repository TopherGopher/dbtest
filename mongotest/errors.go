package mongotest

import "errors"

// ErrNotConnected is returned when the client could not connect to the
// server. The error wraps the driver's. A start's other errors come from the
// mongod package, such as mongod.ErrNotReady, and from the dockerclient
// package, such as dockerclient.ErrConnectionFailed.
var ErrNotConnected = errors.New("mongotest: cannot connect to the server")
