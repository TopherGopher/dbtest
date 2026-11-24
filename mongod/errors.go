package mongod

import (
	"errors"
	"fmt"
	"net"
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
	// ErrNotReady: mongod was not ready before the start timeout passed or
	// the context ended. An error that matches it also matches the
	// context's error.
	ErrNotReady = container.ErrNotReady
	// ErrLoopbackOnly: when the wait ended, the only mongod running was
	// listening on the container's own loopback, where no published port
	// reaches.
	//
	// Deprecated: nothing returns this. A mongod that listens only on the
	// container's loopback now ends in ErrNotReady, with nothing answering
	// at the address. It will be removed in the next release that breaks
	// compatibility.
	ErrLoopbackOnly = errors.New("mongod is listening only on the container's own loopback, " +
		"which a published port cannot reach; it needs --bind_ip_all, which the image's entrypoint normally adds")
	// ErrUnresolvedHost: the address to reach the container on could not be
	// worked out. Set WithHostIP.
	ErrUnresolvedHost = hostaddr.ErrUnresolved
	// ErrContainerExited: the container stopped or was removed before mongod
	// was ready, so waiting longer would not have helped.
	ErrContainerExited = container.ErrExited
	// ErrNoSuchVersion: the image publishes no such tag.
	ErrNoSuchVersion = errors.New("the image publishes no tag for that version")
	// ErrAuthNotEnforced: the container was created with credentials but its
	// mongod still did not enforce them when the start timeout ended. The
	// container is removed, not returned. The error also matches ErrNotReady.
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
	OpPullImage      = container.OpPullImage
	OpCreate         = container.OpCreate
	OpCopyKeyFile    = "copy the replica set key file into the container"
	OpTLSCertificate = "put a TLS certificate in the container"
	OpStart          = container.OpStart
	OpInspect        = container.OpInspect
	OpInitiate       = "initiate the replica set"
)

// StartError says which step of a start failed. Op is one of OpPullImage,
// OpCreate, OpCopyKeyFile, OpTLSCertificate, OpStart, OpInspect and
// OpInitiate. Name is the container, empty for a pull by PullImage. Image is
// the image reference and Err the cause. If the start timeout cut a step
// short, its StartError is the LastErr of a [NotReadyError].
type StartError = container.StartError

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

// UnpublishedPortError names a container port with no host port: Port, the
// container port such as "27017/tcp", and the container's Name and ID. It
// matches ErrNoPublishedPort.
type UnpublishedPortError = container.UnpublishedPortError

// NotReadyError reports that mongod was not ready in time. It matches
// ErrNotReady, the context error that ended the start, and the last failure
// the start saw. Name, ID, Host and Port say where it waited. ID is empty
// and Port zero if time ran out before the container was created or its
// port published. HostSource says what chose Host, such as WithHostIP.
// Timeout is the start timeout. ContextEnded is true if the caller's
// context, not the start timeout, ended the wait. Cause is the context's error. LastErr is the last
// failure, such as the [StartError] of a step cut short.
type NotReadyError = container.NotReadyError

// notReadyAdvice says what to do when the start timeout passed, given the
// last failure the start saw.
func (c config) notReadyAdvice(lastErr error) string {
	switch {
	case errors.Is(lastErr, ErrAuthNotEnforced):
		return "" // the error from the credentials check says what to do
	case !addressInDoubt(lastErr):
		return allowLonger
	case c.network != "":
		return "The address came from " + c.hostSource + ". " + allowLonger + ". " + c.publishInsteadAdvice()
	case c.hostSource != "":
		return "The address came from " + c.hostSource + ". " + allowLonger + ", or correct the address with WithHostIP or " + EnvHostIP
	}
	return allowLonger + ", or correct the address with WithHostIP or " + EnvHostIP
}

// publishInsteadAdvice says how to publish the port when containers on the
// shared network cannot reach each other, naming the address to use when it
// is known.
func (c config) publishInsteadAdvice() string {
	when := "If containers on " + c.network + " cannot reach each other, as on a bridge with icc=false, publish the port instead: set "
	if c.publishHost == "" {
		return when + "WithHostIP or " + EnvHostIP + " to the Docker host's address as this container sees it, usually its default gateway"
	}
	return when + `WithHostIP("` + c.publishHost + `") or ` + EnvHostIP + "=" + c.publishHost
}

// allowLonger is the advice for a start that may only have been slow.
const allowLonger = "Allow longer with WithStartTimeout or " + EnvStartTimeout

// addressInDoubt reports whether the last attempt failed in a way a wrong
// address would explain: nothing answered there, or something that is not
// mongod did. Advice about the address would mislead after any other failure.
func addressInDoubt(lastErr error) bool {
	return errors.Is(lastErr, errNoReply) || errors.Is(lastErr, errNotMongod)
}

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
	return fmt.Sprintf("container %s (%s) was created with credentials, but its mongod answered a client that gave none, "+
		"so anyone can use it. The image, or a flag passed with WithMongodArgs, turned authentication off. "+
		"Use an image that enforces the credentials, remove the flag, or pass WithoutAuth to accept a mongod anyone can use",
		e.Name, container.ShortID(e.ID))
}

func (e *AuthNotEnforcedError) Is(target error) bool { return target == ErrAuthNotEnforced }

// ContainerExitedError reports that the container stopped, or was removed,
// before mongod was ready. It matches ErrContainerExited. Name and ID say
// which container. Status is "exited",
// "dead" or "removed". ExitCode is set only when HasExitCode is true, which
// is when Status is not "removed". Output is the end of what the container
// wrote, at most 8 KiB, and usually says why mongod stopped. Err is what
// the readiness check last saw, if anything.
type ContainerExitedError = container.ExitedError

// addr joins a host and port, bracketing an IPv6 address.
func addr(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}
