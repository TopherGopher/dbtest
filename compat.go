package mongotest

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"go.mongodb.org/mongo-driver/mongo"
)

// The names in this file are the package's earlier API, kept so code
// written against it still compiles.

// TestConnection is a convenience alias for Instance; new code may prefer
// Instance.
type TestConnection = Instance

// ErrCommandFailed: a command run with ExecCommandInMongoContainer exited
// non-zero. The error gives the exit code; Container.Exec returns it as a
// value.
var ErrCommandFailed = errors.New("mongotest: the command exited with a non-zero code")

// attachURI is the server NewTestConnection(false) connects to.
const attachURI = "mongodb://127.0.0.1:27017/?directConnection=true"

// NewTestConnection is a convenience wrapper around Start, or around Attach
// to a server on 127.0.0.1:27017 when spinupDockerContainer is false; new
// code may prefer Start or Run.
func NewTestConnection(spinupDockerContainer bool) (*TestConnection, error) {
	if !spinupDockerContainer {
		return Attach(context.Background(), attachURI)
	}
	return Start(context.Background())
}

// NewReplicaSetContainer is a convenience wrapper around StartWithOptions
// with WithReplicaSet; new code may prefer StartWithOptions. The set already
// has a primary when it returns.
func NewReplicaSetContainer(rsName string) (*TestConnection, error) {
	return StartWithOptions(context.Background(), DefaultOptions().WithReplicaSet(rsName))
}

// MongoClientWithContainer is a convenience wrapper around Start: it passes
// the client to f, then removes the container. It returns f's error joined
// with Stop's. New code may prefer Run.
func MongoClientWithContainer(f func(*mongo.Client) error) error {
	inst, err := Start(context.Background())
	if err != nil {
		return err
	}
	return errors.Join(f(inst.Client), inst.Stop(context.Background()))
}

// GetAvailablePort returns a TCP port that was free on 127.0.0.1 when it was
// called. Another process can take it before it is used, so new code may
// leave the port to Docker.
func GetAvailablePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrNoAvailablePorts, err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// KillMongoContainer is a convenience wrapper around Stop; new code may
// prefer Stop.
func (inst *Instance) KillMongoContainer() error {
	return inst.Stop(context.Background())
}

// MongoContainerID is a convenience wrapper around Container.ID; new code
// may prefer Container.ID. It is empty for an instance from Attach.
func (inst *Instance) MongoContainerID() string {
	if inst.Container == nil {
		return ""
	}
	return inst.Container.ID()
}

// RunMongoScriptOnContainer is a convenience wrapper around
// Container.RunScript; new code may prefer Container.RunScript.
func (inst *Instance) RunMongoScriptOnContainer(js string) (string, error) {
	if inst.Container == nil {
		return "", ErrNoContainer
	}
	return inst.Container.RunScript(context.Background(), js)
}

// ExecCommandInMongoContainer is a convenience wrapper around
// Container.Exec; new code may prefer Container.Exec. It returns standard
// output followed by standard error, and on a non-zero exit code that output
// and an error matching ErrCommandFailed.
func (inst *Instance) ExecCommandInMongoContainer(cmd []string) (string, error) {
	if inst.Container == nil {
		return "", ErrNoContainer
	}
	result, err := inst.Container.Exec(context.Background(), cmd...)
	if err != nil {
		return "", err
	}
	output := result.Stdout + result.Stderr
	if result.ExitCode != 0 {
		return output, fmt.Errorf("%w: %s exited with code %d", ErrCommandFailed, strings.Join(cmd, " "), result.ExitCode)
	}
	return output, nil
}
