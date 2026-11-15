package mongotest

import (
	"context"
	"errors"

	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/mongod"
)

// Error values from the package's earlier API. The two Docker ones are the
// dockerclient sentinels a start now returns, so errors.Is matches them.
var (
	// ErrFailedToConnectToDockerDaemon: no Docker daemon could be reached.
	ErrFailedToConnectToDockerDaemon = dockerclient.ErrConnectionFailed
	// ErrNoAvailablePorts: GetAvailablePort could not listen on a port.
	ErrNoAvailablePorts = errors.New("mongotest: no port is free to listen on")
	// ErrMongoContainerAlreadyRunning: a container with the name asked for
	// already exists. Remove it, or choose another name with WithName.
	ErrMongoContainerAlreadyRunning = dockerclient.ErrConflict
)

// ReapRunningContainers is a convenience wrapper around
// mongod.ReapRunningContainers that discards its error; new code may prefer
// that function.
func ReapRunningContainers() { _ = mongod.ReapRunningContainers(context.Background()) }
