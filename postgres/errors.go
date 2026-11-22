package postgres

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/tophergopher/dbtest/dockerclient"
	"github.com/tophergopher/dbtest/internal/container"
	"github.com/tophergopher/dbtest/internal/hostaddr"
	"github.com/tophergopher/dbtest/internal/pull"
	"github.com/tophergopher/dbtest/internal/reaper"
)

// Sentinels to match with errors.Is. The typed errors below carry the
// details and say what to do.
var (
	// ErrNoPublishedPort: the container port has no host port to connect to.
	ErrNoPublishedPort = container.ErrNoPublishedPort
	// ErrNotReady: the server was not ready before the start timeout passed
	// or the context ended. An error that matches it also matches the
	// context's error.
	ErrNotReady = container.ErrNotReady
	// ErrUnresolvedHost: the address to reach the container on could not be
	// worked out. Set WithHostIP.
	ErrUnresolvedHost = hostaddr.ErrUnresolved
	// ErrContainerExited: the container stopped or was removed before the
	// server was ready, so waiting longer would not have helped.
	ErrContainerExited = container.ErrExited
	// ErrReap: ReapRunningContainers could not remove every container. The
	// error also matches each removal's own error.
	ErrReap = reaper.ErrReap
)

// Errors for an option value that is empty. Each also matches
// dockerclient.ErrInvalidArgument.
var (
	// ErrEmptyImage: WithImage was given an empty reference.
	ErrEmptyImage error = &optionError{option: "WithImage", problem: "the reference is empty",
		fix: `Pass one such as WithImage("postgres:17"), or leave the option out to get the default`}
	// ErrEmptyHostIP: WithHostIP was given an empty address.
	ErrEmptyHostIP error = &optionError{option: "WithHostIP", problem: "the address is empty",
		fix: `Pass one such as WithHostIP("172.17.0.1"), or leave the option out to have it worked out`}
)

// optionError reports an option or environment variable a start cannot
// use. It matches dockerclient.ErrInvalidArgument.
type optionError struct {
	option  string // the option or variable at fault, such as "WithPort"
	problem string
	fix     string
}

func (e *optionError) Error() string {
	return "postgres: " + e.option + ": " + e.problem + ". " + e.fix
}

func (e *optionError) Is(target error) bool { return target == dockerclient.ErrInvalidArgument }

func invalidImage(source, ref string) error {
	return &optionError{option: source, problem: strconv.Quote(ref) + " is not an image reference",
		fix: `Write [registry/]repository[:tag] in lower case, such as "postgres:17-alpine", or only a version such as "17"`}
}

func invalidPort(port int) error {
	return &optionError{option: "WithPort", problem: strconv.Itoa(port) + " is outside the range 0 to 65535",
		fix: "Pass a port from 1 to 65535, or leave the option out to let the daemon choose"}
}

func invalidStartTimeout(d time.Duration) error {
	return &optionError{option: "WithStartTimeout", problem: d.String() + " is negative",
		fix: "Pass a positive duration such as WithStartTimeout(90*time.Second), or leave the option out"}
}

func invalidPullTimeout(d time.Duration) error {
	return &optionError{option: "WithPullTimeout", problem: d.String() + " is negative",
		fix: "Pass a positive duration such as WithPullTimeout(20*time.Minute), or leave the option out"}
}

// environmentError reports an unusable value in an environment variable.
func environmentError(name, value, problem string) error {
	return &optionError{option: name, problem: fmt.Sprintf("%q cannot be used: %s", value, problem),
		fix: "Correct the variable, or unset it to use the default"}
}

// The steps of a start, as StartError.Op names them.
const (
	OpPullImage = container.OpPullImage
	OpCreate    = container.OpCreate
	OpStart     = container.OpStart
	OpInspect   = container.OpInspect
)

// StartError says which step of a start failed. Op is one of OpPullImage,
// OpCreate, OpStart and OpInspect. Name is the container, empty for a pull
// by PullImage. Image is the image reference and Err the cause. If the
// start timeout cut a step short, its StartError is the LastErr of a
// [NotReadyError].
type StartError = container.StartError

// UnpublishedPortError names a container port with no host port: Port, the
// container port "5432/tcp", and the container's Name and ID. It matches
// ErrNoPublishedPort.
type UnpublishedPortError = container.UnpublishedPortError

// NotReadyError reports that the server was not ready in time. It matches
// ErrNotReady, the context error that ended the start, and the last failure
// the start saw. Name, ID, Host and Port say where it waited. ID is empty
// and Port zero if time ran out before the container was created or its
// port published. HostSource says what chose Host, such as WithHostIP.
// Timeout is the start timeout. ContextEnded is true if the caller's
// context, not the start timeout, ended the wait. Cause is the context's
// error. LastErr is the last failure, such as the [StartError] of a step
// cut short.
type NotReadyError = container.NotReadyError

// ContainerExitedError reports that the container stopped, or was removed,
// before the server was ready. It matches ErrContainerExited. Name and ID
// say which container. Status is "exited", "dead" or "removed". ExitCode is
// set only when HasExitCode is true, which is when Status is not "removed".
// Output is the end of what the container wrote, at most 8 KiB, and usually
// says why the server stopped. Err is what the readiness check last saw, if
// anything.
type ContainerExitedError = container.ExitedError

// UnresolvedHostError reports that this process is in a container and the
// address of the daemon's host could not be found from inside it. It
// matches ErrUnresolvedHost and dockerclient.ErrInvalidArgument.
type UnresolvedHostError = hostaddr.UnresolvedError

// ReapError is what ReapRunningContainers returns when a container could not
// be removed, and TeardownError is its entry for one container: read Name
// for the container to remove by hand.
type (
	ReapError     = reaper.ReapError
	TeardownError = reaper.TeardownError
)

// pullTimeoutError is a pull the pull timeout cut short, with what a caller
// of this package can do about it. It matches context.DeadlineExceeded and
// the daemon's own error.
type pullTimeoutError struct {
	err     *pull.TimeoutError
	inStart bool // the pull was inside a start, where PullImage first is a way out
}

func (e *pullTimeoutError) Error() string {
	msg := e.err.Error() + ". Allow longer with WithPullTimeout"
	if e.inStart {
		msg += ", or fetch the image first with PullImage"
	}
	return msg
}

func (e *pullTimeoutError) Unwrap() error { return e.err }

// notReadyAdvice says what to do when the start timeout passed, given the
// last failure the start saw and whether other starts filled the limit on
// starts at once.
func (c config) notReadyAdvice(lastErr error, crowded bool) string {
	switch {
	case crowded:
		return crowdedAdvice
	case !addressInDoubt(lastErr):
		return allowLonger
	case c.hostSource != "":
		return "The address came from " + c.hostSource + ". " + allowLonger + ", or correct the address with WithHostIP or " + EnvHostIP
	}
	return allowLonger + ", or correct the address with WithHostIP or " + EnvHostIP
}

// allowLonger is the advice for a start that may only have been slow.
const allowLonger = "Allow longer with WithStartTimeout or " + EnvStartTimeout

// crowdedAdvice is the advice for a start that ran out of time while it and
// others filled the limit. A slow server looks just like a wrong address, so
// the address is not mentioned.
const crowdedAdvice = "The other starts may have slowed this one. " +
	"Lower the limit on starts at once with postgres.SetMaxConcurrentStarts, or allow longer with WithStartTimeout or " + EnvStartTimeout

// addressInDoubt reports whether the last attempt failed in a way a wrong
// address would explain: nothing answered there, or something that is not
// PostgreSQL did.
func addressInDoubt(lastErr error) bool {
	return errors.Is(lastErr, errNoReply) || errors.Is(lastErr, errNotPostgres)
}
