package mongod

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/tophergopher/mongotest/dockerclient"
)

// initiateScript makes mongod the only member of its replica set, then waits
// until it is primary, which is when it accepts writes. It checks the result
// itself: the legacy shell reports a failure in a value and carries on.
const initiateScript = `var r = rs.initiate(); if (!r.ok) throw new Error(tojson(r)); ` +
	`while (!db.hello().isWritablePrimary) sleep(50);`

// authenticateScript logs the shell in as the root user. It reads the
// credentials from the environment the container was created with, because
// a command line is visible to every user of the daemon's host. mongosh
// reads the environment through process.env and the legacy shell through
// _getEnv, so the script uses whichever it is given.
var authenticateScript = fmt.Sprintf(
	`var env = typeof process === "object" ? function (n) { return process.env[n]; } : _getEnv; `+
		`if (!db.getSiblingDB("admin").auth(env(%q), env(%q))) throw new Error("authentication failed"); `,
	rootCredentialEnv[0].username, rootCredentialEnv[0].password)

// shells are the programs that can run the script, the current one first.
// Images from MongoDB 6.0 have only mongosh, and images before 5.0 only the
// legacy mongo shell.
var shells = []string{"mongosh", "mongo"}

// The exit codes a container runtime reports for a program the image does
// not have: 127, and from some daemons 126. The wording of the message
// varies between runtimes, so only the code is trusted.
const (
	exitCannotRun     = 126
	exitNoSuchProgram = 127
)

// shellMissing reports whether an exit code says the shell never ran.
func shellMissing(exitCode int) bool {
	return exitCode == exitCannotRun || exitCode == exitNoSuchProgram
}

// maxShellOutputLen is the most of a shell's own words quoted in an error.
const maxShellOutputLen = 500

// errNoPrimary is why a start ran out of time during initiation, inside the
// NotReadyError a caller sees.
var errNoPrimary = errors.New("the replica set had not accepted writes yet")

// initiateReplicaSet runs initiateScript inside the container. A flag makes
// mongod a member of a set, but the set exists only once a client has sent
// the command that initiates it, and the shell in the image is the client
// that is already there.
func (c *Container) initiateReplicaSet(ctx context.Context) error {
	c.logger.Debug("Initiating the replica set, so that mongod accepts writes.", "container", c.name, "replicaSet", c.replicaSet)
	for _, shell := range shells {
		result, err := c.docker.Exec(ctx, c.id, c.shellCommand(shell, initiateScript)...)
		switch {
		case err != nil:
			return c.startError(OpInitiate, &notInitiatedError{reason: "the daemon could not run a shell inside the container", err: err})
		case result.ExitCode == 0:
			return nil
		case !shellMissing(result.ExitCode):
			return c.startError(OpInitiate, shellFailed(shell, result))
		}
	}
	return c.startError(OpInitiate, &notInitiatedError{reason: reasonNoShell})
}

// reasonNoShell is why an image with no MongoDB shell cannot have a replica
// set, and what to do about it.
const reasonNoShell = "the image has neither mongosh nor the legacy mongo shell, and Start runs one of them " +
	"inside the container to call rs.initiate(). Use an image that has one (every official mongo image from 4.4 does), " +
	"where a replica set adds about half a second to a start, or leave out WithReplicaSet for a standalone mongod"

// shellFailed reports a shell that ran and exited with an error, in its own
// words. It writes them to standard error.
func shellFailed(shell string, result dockerclient.ExecResult) error {
	output := cmp.Or(strings.TrimSpace(result.Stderr), strings.TrimSpace(result.Stdout))
	return &shellError{shell: shell, exitCode: result.ExitCode, output: truncate(output, maxShellOutputLen)}
}

// shellError is a shell that ran and failed, with what it wrote, cut to a
// length that is safe to quote. It matches ErrNotInitiated.
type shellError struct {
	shell    string
	exitCode int
	output   string
}

func (e *shellError) Error() string {
	return fmt.Sprintf("%s exited with code %d: %s", e.shell, e.exitCode, e.output)
}

func (e *shellError) Is(target error) bool { return target == ErrNotInitiated }

// notInitiatedError says why a replica set was not initiated. It matches
// ErrNotInitiated and whatever caused it.
type notInitiatedError struct {
	reason string
	err    error
}

func (e *notInitiatedError) Error() string {
	if e.err == nil {
		return e.reason
	}
	return e.reason + ": " + e.err.Error()
}

func (e *notInitiatedError) Unwrap() error { return e.err }

func (e *notInitiatedError) Is(target error) bool { return target == ErrNotInitiated }

// shellCommand returns the command that runs a script in one of the shells,
// as the root user when there is one. A mongod with no users refuses an
// attempt to authenticate, so the script only tries when there is.
func (c *Container) shellCommand(shell, script string) []string {
	if c.username != "" {
		script = authenticateScript + script
	}
	return []string{shell, "--quiet", "--norc", "--eval", script}
}
