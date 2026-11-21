// mongotest/errors.go and mongotestv2/errors.go differ only in the
// driver version they use. A change to one belongs in the other too.

package mongotestv2

import "errors"

// ErrNotConnected is returned when the client could not connect to the
// server. It wraps the driver's error. Run and Start can also return errors
// from the mongod package, such as mongod.ErrNotReady, and from the
// dockerclient package, such as dockerclient.ErrConnectionFailed.
var ErrNotConnected = errors.New("mongotestv2: cannot connect to the server")
