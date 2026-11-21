package container

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/tophergopher/dbtest/dockerclient"
)

// Sentinels for a start that failed. Image packages re-export them under
// their own names, so their text is public API.
var (
	// ErrNoPublishedPort: the container port has no host port to connect to.
	ErrNoPublishedPort = errors.New("container port is not published on the host")
	// ErrNotReady: the container was not ready before the start timeout
	// passed or the context ended. An error that matches it also matches
	// the context's error.
	ErrNotReady = errors.New("the container did not become ready in time")
)

// The steps of a start that every image has, as StartError.Op names them.
const (
	OpPullImage = "pull the image"
	OpCreate    = "create the container"
	OpStart     = "start the container"
	OpInspect   = "inspect the container"
)

// StartError says which step of a start failed, and wraps its cause. If the
// start timeout cut a step short, its StartError is the LastErr of a
// NotReadyError. Image packages re-export it, so its fields and message are
// public API.
type StartError struct {
	Op    string // the step that failed, such as OpCreate
	Name  string // the container name, or "" for a pull outside a start
	Image string // the image reference
	Err   error
}

func (e *StartError) Error() string {
	if e.Name == "" {
		return fmt.Sprintf("cannot %s (%s): %v", e.Op, e.Image, e.Err)
	}
	return fmt.Sprintf("cannot %s (container %s, image %s): %v", e.Op, e.Name, e.Image, e.Err)
}

func (e *StartError) Unwrap() error { return e.Err }

// UnpublishedPortError names a container port with no host port. It matches
// ErrNoPublishedPort.
type UnpublishedPortError struct {
	Port string // the container port, such as "27017/tcp"
	Name string
	ID   string
}

func (e *UnpublishedPortError) Error() string {
	return fmt.Sprintf("container %s (%s) has no host port for %s: the daemon did not publish it. "+
		"Check that the daemon allows publishing ports, as a rootless or locked-down daemon may not",
		e.Name, ShortID(e.ID), e.Port)
}

func (e *UnpublishedPortError) Is(target error) bool { return target == ErrNoPublishedPort }

// noNetworkAddressError reports a started container with no address on the
// network it was asked to join. A real daemon always gives it one, so the
// likely cause is a Docker client that does not pass the network on or
// report it back. It matches dockerclient.ErrDaemonResponse.
type noNetworkAddressError struct {
	network, name, id string
}

func (e *noNetworkAddressError) Error() string {
	return fmt.Sprintf("container %s (%s) has no IPv4 address on network %s, which it was asked to join. "+
		"If you pass your own Docker client with WithDocker, check that it sends HostConfig.NetworkMode on create "+
		"and reports NetworkSettings.Networks on inspect. Or publish the port instead: "+
		"set WithHostIP or DBTEST_HOST_IP to the Docker host's address",
		e.name, ShortID(e.id), e.network)
}

func (e *noNetworkAddressError) Is(target error) bool {
	return target == dockerclient.ErrDaemonResponse
}

// NotReadyError reports that the container was not ready in time. It
// matches ErrNotReady, the context error that ended the start, and the last
// failure the start saw. Image packages re-export it.
type NotReadyError struct {
	Name string
	// ID is empty, and Port zero, if the time ran out before the daemon
	// had created the container or published its port.
	ID string
	// Host and Port are the address that was dialled. HostSource says what
	// chose the host, since a wrong address looks just like a slow start.
	Host       string
	Port       int
	HostSource string
	Timeout    time.Duration
	// ContextEnded says the context passed to the start ended the wait, not
	// the start timeout, so a longer timeout would not have helped.
	ContextEnded bool
	// Cause is the context's error. LastErr is the last failed readiness
	// check, or the error of a step that was cut short, or nil.
	Cause   error
	LastErr error
	// Running is how many starts in this process were under way when this
	// one ended, itself included unless it was still waiting for a slot.
	Running int
	// advice is what to do once the start timeout passed, from the image's
	// package, which knows the options it offers.
	advice string
}

// where names the container, with as much of its id and address as the
// start had learned.
func (e *NotReadyError) where() string {
	where := "container " + e.Name
	if e.ID != "" {
		where += " (" + ShortID(e.ID) + ")"
	}
	if e.Port != 0 {
		where += " at " + net.JoinHostPort(e.Host, strconv.Itoa(e.Port))
	}
	return where
}

func (e *NotReadyError) Error() string {
	where := e.where()
	if e.ContextEnded {
		return fmt.Sprintf("the caller's context ended before %s was ready: %v%s. If that context has a deadline, make it later",
			where, e.Cause, e.lastFailure())
	}
	msg := fmt.Sprintf("%s was not ready within %s%s", where, e.Timeout, e.lastFailure())
	if e.advice == "" {
		return msg
	}
	return msg + ". " + e.advice
}

// lastFailure returns ": " and the last failure, or "" if there was none.
func (e *NotReadyError) lastFailure() string {
	if e.LastErr == nil {
		return ""
	}
	return fmt.Sprintf(": %v", e.LastErr)
}

func (e *NotReadyError) Unwrap() []error {
	if e.LastErr == nil {
		return []error{e.Cause}
	}
	return []error{e.Cause, e.LastErr}
}

func (e *NotReadyError) Is(target error) bool { return target == ErrNotReady }
