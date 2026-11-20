package mongod

import (
	"cmp"
	"context"
	"fmt"
	"strings"

	"github.com/tophergopher/dbtest/dockerclient"
)

// Exec runs a command inside the container and returns its output and exit
// code. A command that exits non-zero is a result, not an error.
func (c *Container) Exec(ctx context.Context, cmd ...string) (dockerclient.ExecResult, error) {
	result, err := c.docker.Exec(ctx, c.ID(), cmd...)
	if err != nil {
		return result, fmt.Errorf("mongod: cannot run a command in container %s: %w", c.Name(), err)
	}
	return result, nil
}

// RunScript runs JavaScript source in the image's MongoDB shell and returns
// what it printed, with leading and trailing white space removed. The shell
// prints the value of the last expression, formatted differently by each
// shell, so end with JSON.stringify(...) to parse the result. db is the test
// database; use db.getSiblingDB(name) for another.
//
// If the container has credentials, the script runs logged in as the MongoDB
// root user. If the shell exits non-zero, the error is a [*ScriptError]; if
// the image has no shell, it matches [ErrNoShell]. Ending ctx returns early
// but does not stop a script that is already running.
func (c *Container) RunScript(ctx context.Context, js string) (string, error) {
	result, err := c.runShell(ctx, js)
	switch {
	case err != nil:
		return "", fmt.Errorf("mongod: cannot run a script in container %s: %w", c.Name(), err)
	case result.ExitCode != 0:
		return "", fmt.Errorf("mongod: the script failed in container %s: %w", c.Name(), result.failure())
	}
	return strings.TrimSpace(result.Stdout), nil
}

// shellResult is a shell's output and exit code, and the name of the shell
// that produced them.
type shellResult struct {
	dockerclient.ExecResult
	shell string
}

func (r shellResult) failure() *ScriptError {
	return &ScriptError{Shell: r.shell, ExitCode: r.ExitCode, Stdout: r.Stdout, Stderr: r.Stderr}
}

// runShell runs a script in the first of the shells the image has.
//
// A script that itself exits with 126 or 127 looks like a missing shell, and
// on an image with both shells is run again in the legacy one.
func (c *Container) runShell(ctx context.Context, script string) (shellResult, error) {
	for _, shell := range shells {
		result, err := c.docker.Exec(ctx, c.ID(), c.shellCommand(shell, script)...)
		if err != nil {
			return shellResult{}, err
		}
		if !shellMissing(result.ExitCode) {
			return shellResult{ExecResult: result, shell: shell}, nil
		}
	}
	return shellResult{}, errNoShell
}

// shells lists the programs that can run a script, newest first. Images from
// MongoDB 6.0 have only mongosh, 5.0 images have both, and earlier images
// have only mongo.
var shells = []string{"mongosh", "mongo"}

// Exit codes a container runtime returns when the program is not in the
// image: 127, or 126 from some runtimes. Runtimes word the error
// differently, so only the code is checked.
const (
	exitCannotRun     = 126
	exitNoSuchProgram = 127
)

// shellMissing reports whether an exit code says the shell never ran.
func shellMissing(exitCode int) bool {
	return exitCode == exitCannotRun || exitCode == exitNoSuchProgram
}

// shellCommand returns the command that runs script in shell. If the
// container has a root user, the script logs in as that user first.
// Otherwise it does not try, because a mongod with no users rejects any login.
func (c *Container) shellCommand(shell, script string) []string {
	if c.username != "" {
		script = authenticateScript + script
	}
	cmd := []string{shell, "--quiet", "--norc"}
	if c.certs != nil {
		cmd = append(cmd, "--tls", "--tlsCAFile", tlsCAPath)
	}
	return append(cmd, "--eval", script)
}

// authenticateScript logs the shell in as the root user. It reads the
// credentials from the container's environment, so the password never
// appears on a command line, where any user on the daemon's host could see
// it. mongosh reads the environment through process.env and the legacy shell
// through _getEnv; the script uses whichever exists. It runs inside a
// function so that it declares nothing the caller's script might.
var authenticateScript = fmt.Sprintf(
	`(function () { var env = typeof process === "object" ? function (n) { return process.env[n]; } : _getEnv; `+
		`if (!db.getSiblingDB("admin").auth(env(%q), env(%q))) `+
		`throw new Error("cannot log in as the root user with the container's credentials"); })(); `,
	rootCredentialEnv[0].username, rootCredentialEnv[0].password)

// ScriptError reports that the shell exited non-zero while running a script.
// It matches ErrScriptFailed. The shell says why in Stderr, or in Stdout for
// the legacy mongo shell. The message quotes the start of that; the fields
// hold all of it.
type ScriptError struct {
	Shell    string // "mongosh", or "mongo" in an image before MongoDB 5.0
	ExitCode int
	Stdout   string
	Stderr   string
}

// maxShellOutputLen caps how many bytes of a shell's output an error quotes.
const maxShellOutputLen = 500

func (e *ScriptError) Error() string {
	msg := fmt.Sprintf("%s exited with code %d", e.Shell, e.ExitCode)
	said := cmp.Or(strings.TrimSpace(e.Stderr), strings.TrimSpace(e.Stdout))
	if said == "" {
		return msg
	}
	return msg + ": " + truncate(said, maxShellOutputLen)
}

func (e *ScriptError) Is(target error) bool { return target == ErrScriptFailed }

// errNoShell is why a script could not run: the image has neither shell.
var errNoShell error = &noShellError{}

// noShellError reports an image that has neither mongosh nor mongo. It
// matches ErrNoShell.
type noShellError struct{}

func (*noShellError) Error() string {
	return "the image has neither mongosh nor the legacy mongo shell. " +
		"Use an image that has one: every official mongo image from 4.4 does"
}

func (*noShellError) Is(target error) bool { return target == ErrNoShell }
