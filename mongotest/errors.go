// mongotest/errors.go and mongotestv2/errors.go are copies. They differ only
// in the package's name. A change to one belongs in the other too.

package mongotest

import "errors"

// ErrNotConnected is returned when the client could not connect to the
// server. It wraps the driver's error. Run and Start can also return errors
// from the mongod package, such as mongod.ErrNotReady, and from the
// dockerclient package, such as dockerclient.ErrConnectionFailed.
var ErrNotConnected = errors.New("mongotest: cannot connect to the server")
