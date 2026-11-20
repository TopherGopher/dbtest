package mongotest

import (
	"errors"

	"github.com/tophergopher/dbtest/dockerclient"
)

// Sentinels to match with errors.Is. A start's other errors come from the
// mongod package, such as mongod.ErrNotReady.
var (
	// ErrNotConnected: the client could not connect to the server. The error
	// wraps the driver's.
	ErrNotConnected = errors.New("mongotest: cannot connect to the server")
	// ErrNoContainer: an instance from Attach was asked to do something only
	// a container it started can do, such as run a script.
	ErrNoContainer = errors.New("mongotest: the instance was attached to a running server and has no container; " +
		"start one with Run or Start to run scripts and commands")
	// ErrCommandFailed: a command or script run with
	// ExecCommandInMongoContainer or RunMongoScriptOnContainer exited
	// non-zero. The error gives the exit code.
	ErrCommandFailed = errors.New("mongotest: the command exited with a non-zero code")
)

// Error values from the package's earlier API. The two about Docker are the
// dockerclient sentinels, so errors.Is matches what a start returns.
var (
	// ErrFailedToConnectToDockerDaemon: no Docker daemon could be reached.
	ErrFailedToConnectToDockerDaemon = dockerclient.ErrConnectionFailed
	// ErrNoAvailablePorts: GetAvailablePort could not listen on a port.
	ErrNoAvailablePorts = errors.New("mongotest: no port is free to listen on")
	// ErrMongoContainerAlreadyRunning: the daemon reported a conflict, such as
	// a container name WithName gave already being in use. Remove that
	// container, or choose another name.
	ErrMongoContainerAlreadyRunning = dockerclient.ErrConflict
)
