package mongod

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
)

// initiateScript makes mongod the only member of its replica set, then waits
// until it is primary, which is when it accepts writes.
const initiateScript = `rs.initiate(); while (!db.hello().isWritablePrimary) sleep(50)`

// authenticateScript logs the shell in as the root user. It reads the
// credentials from the environment the container was created with, because
// a command line is visible to every user of the daemon's host.
var authenticateScript = fmt.Sprintf(`db.getSiblingDB("admin").auth(process.env.%s, process.env.%s); `,
	rootCredentialEnv[0].username, rootCredentialEnv[0].password)

// errNoPrimary is why a start ran out of time during initiation, inside the
// NotReadyError a caller sees.
var errNoPrimary = errors.New("the replica set had not accepted writes yet")

// initiateReplicaSet runs initiateScript inside the container. Initiating
// needs a MongoDB client, and the image's own mongosh is the one that is
// already there.
func (c *Container) initiateReplicaSet(ctx context.Context) error {
	c.logger.Debug("initiating the replica set", "container", c.name, "replicaSet", c.replicaSet)
	result, err := c.docker.Exec(ctx, c.id, c.mongosh(initiateScript)...)
	if err != nil {
		return c.startError(OpInitiate, fmt.Errorf("%w: %w", ErrNotInitiated, err))
	}
	if result.ExitCode != 0 {
		// The daemon reports a program it could not find on standard output.
		output := cmp.Or(strings.TrimSpace(result.Stderr), strings.TrimSpace(result.Stdout))
		return c.startError(OpInitiate, fmt.Errorf("%w: mongosh exited with code %d: %s",
			ErrNotInitiated, result.ExitCode, output))
	}
	return nil
}

// mongosh returns the command that runs a script in the container's shell,
// as the root user when there is one. A mongod with no users refuses an
// attempt to authenticate, so the script only tries when there is.
func (c *Container) mongosh(script string) []string {
	if c.username != "" {
		script = authenticateScript + script
	}
	return []string{"mongosh", "--quiet", "--norc", "--eval", script}
}
