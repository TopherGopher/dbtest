package mongotest

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/tophergopher/mongotest/mongod"
	"go.mongodb.org/mongo-driver/mongo"
)

// The names in this file are the package's earlier API, kept so code
// written against it still compiles.

// TestConnection is a convenience alias for Instance; new code may prefer
// Instance.
type TestConnection = Instance

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
// the client to f, then removes the container, even if f fails the test or
// panics. It returns f's error joined with Stop's. New code may prefer Run.
func MongoClientWithContainer(f func(*mongo.Client) error) (err error) {
	inst, err := Start(context.Background())
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, inst.Stop(context.Background())) }()
	return f(inst.Client)
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
	if inst == nil || inst.Container == nil {
		return ""
	}
	return inst.Container.ID()
}

// container returns the instance's container, or ErrNoContainer.
func (inst *Instance) container() (*mongod.Container, error) {
	if inst == nil || inst.Container == nil {
		return nil, ErrNoContainer
	}
	return inst.Container, nil
}

// RunMongoScriptOnContainer is a convenience wrapper around
// Container.RunScript; new code may prefer Container.RunScript. On a script
// that fails, it returns what the shell wrote and an error matching
// ErrCommandFailed.
func (inst *Instance) RunMongoScriptOnContainer(js string) (string, error) {
	c, err := inst.container()
	if err != nil {
		return "", err
	}
	out, err := c.RunScript(context.Background(), js)
	var failed *mongod.ScriptError
	if errors.As(err, &failed) {
		return failed.Stdout + failed.Stderr, fmt.Errorf("%w: %w", ErrCommandFailed, err)
	}
	return out, err
}

// ExecCommandInMongoContainer is a convenience wrapper around
// Container.Exec; new code may prefer Container.Exec. It returns standard
// output followed by standard error, and on a non-zero exit code that output
// and an error matching ErrCommandFailed.
func (inst *Instance) ExecCommandInMongoContainer(cmd []string) (string, error) {
	c, err := inst.container()
	if err != nil {
		return "", err
	}
	result, err := c.Exec(context.Background(), cmd...)
	if err != nil {
		return "", err
	}
	output := result.Stdout + result.Stderr
	if result.ExitCode != 0 {
		return output, fmt.Errorf("%w: %s exited with code %d", ErrCommandFailed, strings.Join(cmd, " "), result.ExitCode)
	}
	return output, nil
}

// ReapRunningContainers is a convenience wrapper around
// mongod.ReapRunningContainers that discards its error; new code may prefer
// that function.
func ReapRunningContainers() { _ = mongod.ReapRunningContainers(context.Background()) }
