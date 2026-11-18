package mongod

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/internal/hostaddr"
	"github.com/tophergopher/mongotest/internal/pull"
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
	ErrUnresolvedHost = hostaddr.ErrUnresolved
	// ErrContainerExited: the container stopped or was removed before mongod
	// was ready, so waiting longer would not have helped.
	ErrContainerExited = errors.New("the container exited before mongod was ready")
	// ErrNoSuchVersion: the image publishes no such tag.
	ErrNoSuchVersion = errors.New("the image publishes no tag for that version")
	// ErrAuthNotEnforced: the container was created with credentials but its
	// mongod does not ask for them. The container is removed, not returned.
	ErrAuthNotEnforced = errors.New("mongod is not enforcing the credentials it was created with")
	// ErrNotInitiated: WithReplicaSet could not initiate the set. The error
	// says whether the image has no shell to do it with, or the shell failed.
	ErrNotInitiated = errors.New("the replica set could not be initiated")
	// ErrScriptFailed: the shell ran a script and exited non-zero. The error
	// is a *ScriptError holding the shell's exit code and output.
	ErrScriptFailed = errors.New("a script failed in the MongoDB shell")
	// ErrNoShell: the image has no MongoDB shell to run a script with.
	ErrNoShell = errors.New("the image has no MongoDB shell (mongosh or mongo)")
	// ErrReap: ReapRunningContainers could not remove every container. The
	// error also matches each removal's own error.
	ErrReap = reaper.ErrReap
	// ErrUnsupportedForImage: the chosen image cannot honour an option.
	ErrUnsupportedForImage = errors.New("the image does not support an option that was set")
)

// ReapError is what ReapRunningContainers returns when a container could not
// be removed, and TeardownError is its entry for one container: read Name
// for the container to remove by hand.
type (
	ReapError     = reaper.ReapError
	TeardownError = reaper.TeardownError
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

// Why a reference cannot name an image, inside the error that says where
// the reference came from.
var (
	errImageHasScheme = errors.New("it starts with a scheme, which an image reference does not have. " +
		`Leave the scheme out, as in "mirror.corp.example/mongo:8"`)
	errNotAnImage = errors.New("it is not an image reference. Write [registry/]repository[:tag] in lower case, " +
		`such as "mongo:8" or "mirror.corp.example/mongo:8", or only a version such as "8.0"`)
	errNotAVersion = errors.New("it is not a version docker can use as a tag. " +
		`Write one such as "8", "8.0.30" or "8.0-ubi9"`)
)

// imageRefError reports a reference that cannot name an image. It matches
// its reason and dockerclient.ErrInvalidArgument.
type imageRefError struct {
	source string // what gave the reference: an option, or a variable's name
	ref    string
	reason error
}

func (e *imageRefError) Error() string {
	return fmt.Sprintf("mongod: %s: %q cannot be used: %v", e.source, e.ref, e.reason)
}

func (e *imageRefError) Unwrap() error { return e.reason }

func (e *imageRefError) Is(target error) bool { return target == dockerclient.ErrInvalidArgument }

// errNotADuration is why a MONGOTEST_START_TIMEOUT value is refused.
var errNotADuration = errors.New(`it is not a positive duration such as "90s" or "2m"`)

// Why an image cannot honour an option, and what to do instead.
const (
	reasonPreconfiguredReplicaSet = "it already runs a single-node replica set under a name it generates, " +
		"so remove the option and read the name from the server"
	reasonCommandIsEntrypoint = "it has no entrypoint, so the flags would replace the program it runs; remove the option"
	reasonNoTLSFlags          = "it starts mongod itself and takes no flags, so TLS cannot be turned on; use another image or remove the option"
)

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

// environmentError reports an unusable value in an environment variable. A
// cause written for an option gives only its problem, so the advice for the
// option is not passed on to someone who set a variable.
func environmentError(name, value string, cause error) error {
	return &optionError{option: name, problem: fmt.Sprintf("%q cannot be used: %s", value, problemOf(cause)),
		fix: "Correct the variable, or unset it to use the default"}
}

// problemOf returns what is wrong with a value, without the advice an
// option's error carries.
func problemOf(cause error) string {
	var forOption *optionError
	if errors.As(cause, &forOption) {
		return forOption.problem
	}
	return cause.Error()
}

// The steps of a start, as StartError.Op names them.
const (
	OpPullImage      = "pull the image"
	OpCreate         = "create the container"
	OpCopyKeyFile    = "copy the replica set key file into the container"
	OpTLSCertificate = "put a TLS certificate in the container"
	OpStart          = "start the container"
	OpInspect        = "inspect the container"
	OpInitiate       = "initiate the replica set"
)

// StartError says which step of a start failed. It wraps the daemon's error.
// A step the start timeout cut short comes inside a [NotReadyError].
type StartError struct {
	Op    string // one of the Op constants
	Name  string // the container name, or "" when PullImage reports a pull
	Image string // the image reference
	Err   error
}

func (e *StartError) Error() string {
	if e.Name == "" {
		return fmt.Sprintf("mongod: cannot %s (image %s): %v", e.Op, e.Image, e.Err)
	}
	return fmt.Sprintf("mongod: cannot %s (container %s, image %s): %v", e.Op, e.Name, e.Image, e.Err)
}

func (e *StartError) Unwrap() error { return e.Err }

// pullTimeoutError is a pull the pull timeout cut short, with what a caller
// of this package can do about it. It matches context.DeadlineExceeded and
// the daemon's own error.
type pullTimeoutError struct {
	err *pull.TimeoutError
	// inStart says the pull happened inside Start, where fetching the image
	// first with PullImage is a way out.
	inStart bool
}

func (e *pullTimeoutError) Error() string {
	msg := e.err.Error() + ". Allow longer with WithPullTimeout"
	if e.inStart {
		msg += ", or fetch the image first with PullImage"
	}
	return msg
}

func (e *pullTimeoutError) Unwrap() error { return e.err }

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
// ErrNotReady, the context error that ended the start, and the last failure
// the start saw.
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
	// ContextEnded says the context passed to Start ended the wait, not the
	// start timeout, so a longer timeout would not have helped.
	ContextEnded bool
	// Cause is the context's error. LastErr is the last failed attempt to
	// reach mongod, or the [StartError] of a step that was cut short, or nil.
	Cause   error
	LastErr error
}

// where names the container, with as much of its id and address as the
// start had learned.
func (e *NotReadyError) where() string {
	where := "container " + e.Name
	if e.ID != "" {
		where += " (" + shortID(e.ID) + ")"
	}
	if e.Port != 0 {
		where += " at " + addr(e.Host, e.Port)
	}
	return where
}

func (e *NotReadyError) Error() string {
	where := e.where()
	if e.ContextEnded {
		return fmt.Sprintf("mongod: the context passed to Start ended before %s was ready: %v%s. If it has a deadline, make it later",
			where, e.Cause, e.lastFailure())
	}
	msg := fmt.Sprintf("mongod: %s was not ready within %s%s", where, e.Timeout, e.lastFailure())
	if !e.addressInDoubt() {
		return msg + ". Allow longer with WithStartTimeout"
	}
	if e.HostSource != "" {
		msg += ". The address came from " + e.HostSource
	}
	return msg + ". Allow longer with WithStartTimeout, or correct the address with WithHostIP or " + EnvHostIP
}

// addressInDoubt reports whether the last attempt failed in a way a wrong
// address would explain: nothing answered there, or something that is not
// mongod did. Advice about the address would mislead after any other failure.
func (e *NotReadyError) addressInDoubt() bool {
	return errors.Is(e.LastErr, errNoReply) || errors.Is(e.LastErr, errNotMongod)
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
// address of the daemon's host could not be found from inside it. Read
// Evidence for what showed this process is in a container, DockerHost for
// the daemon address, and Err for why the gateway lookup failed. It matches
// ErrUnresolvedHost and dockerclient.ErrInvalidArgument.
type UnresolvedHostError = hostaddr.UnresolvedError

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
	Option string // the option to remove, such as "WithReplicaSet"
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

// exitOutputLimit is the most bytes of an exited container's output that
// ContainerExitedError keeps, enough for mongod's last several log lines.
const exitOutputLimit = 8 << 10

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
	// Output is the end of what the container wrote to stdout and stderr,
	// at most 8 KiB, starting at a whole line. It usually says why mongod
	// stopped. It is empty if the container was removed or its log could
	// not be read.
	Output string
	Err    error
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
	return fmt.Sprintf("mongod: container %s (%s) %s with code %d before mongod was ready. %s",
		e.Name, shortID(e.ID), verb, e.ExitCode, e.why())
}

// why shows the end of what the container wrote, which usually says why it
// stopped, or the usual cause when there is nothing to show.
func (e *ContainerExitedError) why() string {
	if e.Output == "" {
		return "The usual cause is a flag or replica set name mongod rejects. " +
			"Its log was empty or could not be read, so run the image by hand with the same flags to see why"
	}
	return "The end of what mongod wrote:\n" + strings.TrimRight(e.Output, "\n")
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
