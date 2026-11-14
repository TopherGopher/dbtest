package mongod

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/internal/reaper"
)

// Sentinels to match with errors.Is. The typed errors below carry the
// details and say what to do.
var (
	// ErrNoPublishedPort: the container port has no host port to connect to.
	ErrNoPublishedPort = errors.New("container port is not published on the host")
	// ErrNotReady: mongod was not ready before the start timeout passed or
	// the context ended. The error also matches the context's error.
	ErrNotReady = errors.New("mongod did not become ready in time")
	// ErrLoopbackOnly: when the wait ended, the only mongod running was
	// listening on the container's own loopback, where no published port
	// reaches. It accompanies ErrNotReady.
	ErrLoopbackOnly = errors.New("mongod is listening only on the container's own loopback, " +
		"which a published port cannot reach; it needs --bind_ip_all, which the image's entrypoint normally adds")
	// ErrUnresolvedHost: the address to reach the container on could not be
	// worked out. Set WithHostIP.
	ErrUnresolvedHost = errors.New("cannot work out the address to reach the container on")
	// ErrContainerExited: the container stopped or was removed before mongod
	// was ready, so waiting longer would not have helped.
	ErrContainerExited = errors.New("the container exited before mongod was ready")
	// ErrNoSuchVersion: the image publishes no such tag.
	ErrNoSuchVersion = errors.New("the image publishes no tag for that version")
	// ErrAuthNotEnforced: the container was created with credentials but its
	// mongod does not ask for them. The container is removed, not returned.
	ErrAuthNotEnforced = errors.New("mongod is not enforcing the credentials it was created with")
	// ErrNotInitiated: WithReplicaSet could not initiate the set. Initiating
	// runs mongosh inside the container, so the image must have it.
	ErrNotInitiated = errors.New("the replica set could not be initiated, which needs mongosh in the image")
	// ErrReap: ReapRunningContainers could not remove every container. The
	// error also matches each removal's own error.
	ErrReap = reaper.ErrReap
	// ErrUnsupportedForImage: the chosen image cannot honour an option.
	ErrUnsupportedForImage = errors.New("the image does not support an option that was set")
)

// Errors for an option value that is empty or contradicts another. Each also
// matches dockerclient.ErrInvalidArgument.
var (
	// ErrEmptyImage: WithImage or ParseMongoImage was given an empty reference.
	ErrEmptyImage error = &optionError{option: "WithImage", problem: "the reference is empty",
		fix: `Pass one such as WithImage("mongo:8"), or leave the option out to get the default`}
	// ErrIncompleteCredentials: only one of username and password was set.
	ErrIncompleteCredentials error = &optionError{option: "WithAuth", problem: "only one of the username and the password is set",
		fix: `Pass both, as in WithAuth("admin", "secret"), or neither`}
	// ErrUnusableCredentials: a username or password holds a backslash, a
	// backtick or "${". MongoDB's community image pastes the credentials
	// into a script, where those change the password or create no user.
	ErrUnusableCredentials error = &optionError{option: "WithAuth", problem: "a username or password contains a backslash, a backtick or ${",
		fix: "Choose credentials without them: MongoDB's own images cannot create that user"}
	// ErrEmptyReplicaSet: WithReplicaSet was given an empty name.
	ErrEmptyReplicaSet error = &optionError{option: "WithReplicaSet", problem: "the name is empty",
		fix: `Pass one such as WithReplicaSet("rs0"), or leave the option out for a standalone mongod`}
	// ErrEmptyHostIP: WithHostIP was given an empty address.
	ErrEmptyHostIP error = &optionError{option: "WithHostIP", problem: "the address is empty",
		fix: `Pass one such as WithHostIP("172.17.0.1"), or leave the option out to have it worked out`}
)

// optionError reports an option or environment variable Start cannot use.
// It matches dockerclient.ErrInvalidArgument.
type optionError struct {
	option  string // the option or variable at fault, such as "WithAuth"
	problem string
	fix     string
}

func (e *optionError) Error() string {
	return "mongod: " + e.option + ": " + e.problem + ". " + e.fix
}

func (e *optionError) Is(target error) bool { return target == dockerclient.ErrInvalidArgument }

// errNotADuration is why a MONGOTEST_START_TIMEOUT value is refused.
var errNotADuration = errors.New(`it is not a positive duration such as "90s" or "2m"`)

// Why an image cannot honour an option, and what to do instead.
const (
	reasonPreconfiguredReplicaSet = "it already runs a single-node replica set under a name it generates, " +
		"so remove the option and read the name from the server"
	reasonCommandIsEntrypoint = "it has no entrypoint, so the flags would replace the program it runs; remove the option"
)

func invalidPort(port int) error {
	return &optionError{option: "WithPort", problem: strconv.Itoa(port) + " is outside the range 0 to 65535",
		fix: "Pass a port from 1 to 65535, or leave the option out to let the daemon choose"}
}

func invalidStartTimeout(d time.Duration) error {
	return &optionError{option: "WithStartTimeout", problem: d.String() + " is negative",
		fix: "Pass a positive duration such as WithStartTimeout(90*time.Second), or leave the option out"}
}

// environmentError reports an unusable value in an environment variable. A
// cause that is itself an invalid argument gives only its problem, so the
// advice is not said twice.
func environmentError(name, value string, cause error) error {
	return &optionError{option: name, problem: fmt.Sprintf("%q cannot be used: %s", value, problemOf(cause)),
		fix: "Correct the variable, or unset it to use the default"}
}

// problemOf returns what is wrong with a value, without the advice an
// invalid-argument error carries for the option it was written for.
func problemOf(cause error) string {
	var fromDocker *dockerclient.InvalidArgumentError
	var own *optionError
	switch {
	case errors.As(cause, &fromDocker):
		return fromDocker.Problem
	case errors.As(cause, &own):
		return own.problem
	}
	return cause.Error()
}

// The steps of a start, as StartError.Op names them.
const (
	OpPullImage   = "pull the image"
	OpCreate      = "create the container"
	OpCopyKeyFile = "copy the replica set key file into the container"
	OpStart       = "start the container"
	OpInspect     = "inspect the container"
	OpInitiate    = "initiate the replica set"
)

// StartError says which step of a start failed. It wraps the daemon's error.
type StartError struct {
	Op    string // one of the Op constants
	Name  string // the container name
	Image string // the image reference
	Err   error
}

func (e *StartError) Error() string {
	return fmt.Sprintf("mongod: cannot %s (container %s, image %s): %v", e.Op, e.Name, e.Image, e.Err)
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
	return fmt.Sprintf("mongod: container %s (%s) has no host port for %s: the daemon did not publish it",
		e.Name, shortID(e.ID), e.Port)
}

func (e *UnpublishedPortError) Is(target error) bool { return target == ErrNoPublishedPort }

// NotReadyError reports that mongod was not ready in time. It matches
// ErrNotReady, the context error that ended the wait, and the last failure
// the wait saw.
type NotReadyError struct {
	Name string
	ID   string
	// Host and Port are the address that was dialled. HostSource says what
	// chose the host, since a wrong address looks just like a slow start.
	Host       string
	Port       int
	HostSource string
	Timeout    time.Duration
	// ContextEnded says the context passed to Start ended the wait, not the
	// start timeout, so a longer timeout would not have helped.
	ContextEnded bool
	// Cause is the context's error. LastErr is the last failed attempt, or nil.
	Cause   error
	LastErr error
}

func (e *NotReadyError) Error() string {
	where := fmt.Sprintf("container %s (%s) at %s", e.Name, shortID(e.ID), addr(e.Host, e.Port))
	if e.ContextEnded {
		return fmt.Sprintf("mongod: the context passed to Start ended before %s was ready: %v%s. If it has a deadline, make it later",
			where, e.Cause, e.lastFailure())
	}
	msg := fmt.Sprintf("mongod: %s was not ready within %s%s", where, e.Timeout, e.lastFailure())
	if e.HostSource != "" {
		msg += ". The address came from " + e.HostSource
	}
	return msg + ". Allow longer with WithStartTimeout, or correct the address with WithHostIP or " + EnvHostIP
}

// lastFailure is what the last attempt found, ready to follow a message.
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

// UnresolvedHostError reports that this process is in a container and the
// address of the daemon's host could not be found from inside it. It matches
// ErrUnresolvedHost and dockerclient.ErrInvalidArgument.
type UnresolvedHostError struct {
	DockerHost string // the daemon address the client reported
	Evidence   string // what showed this process is in a container
	Err        error
}

func (e *UnresolvedHostError) Error() string {
	return fmt.Sprintf("mongod: cannot work out where containers started through %s are reachable from here: "+
		"this process looks containerised (%s), and the gateway lookup failed: %v. Set %s, or pass WithHostIP, to the address of the daemon's host",
		e.DockerHost, e.Evidence, e.Err, EnvHostIP)
}

func (e *UnresolvedHostError) Unwrap() error { return e.Err }

func (e *UnresolvedHostError) Is(target error) bool {
	return target == ErrUnresolvedHost || target == dockerclient.ErrInvalidArgument
}

// NoSuchVersionError reports a version the image's repository cannot have.
// It matches ErrNoSuchVersion and dockerclient.ErrInvalidArgument.
type NoSuchVersionError struct {
	Image MongoImage // as resolved, including the version at fault
}

func (e *NoSuchVersionError) Error() string {
	return fmt.Sprintf("mongod: %s publishes no tag %q: every tag there names an OS variant. Pass a tag such as %q",
		e.Image.Repository, e.Image.Version, e.Image.defaultTag())
}

func (e *NoSuchVersionError) Is(target error) bool {
	return target == ErrNoSuchVersion || target == dockerclient.ErrInvalidArgument
}

// UnsupportedForImageError reports an option the image cannot honour. It
// matches ErrUnsupportedForImage and dockerclient.ErrInvalidArgument.
type UnsupportedForImageError struct {
	Image  MongoImage
	Option string // the helper to remove, such as "WithReplicaSet"
	Reason string
}

func (e *UnsupportedForImageError) Error() string {
	return fmt.Sprintf("mongod: %s does not support %s: %s", e.Image.Repository, e.Option, e.Reason)
}

func (e *UnsupportedForImageError) Is(target error) bool {
	return target == ErrUnsupportedForImage || target == dockerclient.ErrInvalidArgument
}

// AuthNotEnforcedError reports a mongod running without the authentication
// its container was created to have. It matches ErrAuthNotEnforced.
type AuthNotEnforcedError struct {
	Name string
	ID   string
}

func (e *AuthNotEnforcedError) Error() string {
	return fmt.Sprintf("mongod: container %s (%s) was created with credentials, but its mongod runs without --auth: "+
		"the image did not create a root user from its environment. "+
		"Use an image that does, or pass WithoutAuth to accept a mongod anyone can use",
		e.Name, shortID(e.ID))
}

func (e *AuthNotEnforcedError) Is(target error) bool { return target == ErrAuthNotEnforced }

// ContainerExitedError reports that the container stopped, or was removed,
// before mongod was ready. It matches ErrContainerExited.
type ContainerExitedError struct {
	Name string
	ID   string
	// Status is "exited", "dead", or "removed".
	Status string
	// ExitCode is meaningful only when HasExitCode is true.
	ExitCode    int
	HasExitCode bool
	Err         error
}

func (e *ContainerExitedError) Error() string {
	if !e.HasExitCode {
		return fmt.Sprintf("mongod: container %s (%s) was removed before mongod was ready; something other than this Start removed it",
			e.Name, shortID(e.ID))
	}
	verb := "exited"
	if e.Status == "dead" {
		verb = "died"
	}
	return fmt.Sprintf("mongod: container %s (%s) %s with code %d before mongod was ready. "+
		"The usual cause is a flag or replica set name mongod rejects. "+
		"The container and its log are gone, so run the image by hand with the same flags to see why",
		e.Name, shortID(e.ID), verb, e.ExitCode)
}

func (e *ContainerExitedError) Unwrap() error { return e.Err }

func (e *ContainerExitedError) Is(target error) bool { return target == ErrContainerExited }

// shortID abbreviates a container id for a message, as the docker CLI does.
func shortID(id string) string {
	const length = 12
	if len(id) <= length {
		return id
	}
	return id[:length]
}

// addr joins a host and port, bracketing an IPv6 address.
func addr(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}
