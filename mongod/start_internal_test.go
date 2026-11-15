package mongod

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests are inside the package because what they protect reaches a
// caller only as the words of a log line or of a script.

func TestTheReadyLineExplainsAHiddenPasswordOnlyWhenThereIsOne(t *testing.T) {
	open, authenticated := &Container{}, &Container{username: "admin", password: "s3cret"}

	assert.Equal(t, readyMessage, open.readyMessage(), "a container with no credentials has no password to explain")
	assert.Equal(t, readyMessagePasswordHidden, authenticated.readyMessage(), "one with credentials says its logged URI is not the real one")
}

func TestTheShellLogsInOnlyWhenTheContainerHasCredentials(t *testing.T) {
	open, authenticated := &Container{}, &Container{username: "admin", password: "s3cret"}

	script := func(c *Container) string {
		command := c.shellCommand("mongosh", initiateScript)
		require.NotEmpty(t, command, "there is a command to run")
		return command[len(command)-1]
	}

	assert.Equal(t, initiateScript, script(open), "a mongod with no users refuses an attempt to log in, so none is made")
	assert.True(t, strings.HasPrefix(script(authenticated), authenticateScript), "a mongod with a root user is logged in to first")
	assert.True(t, strings.HasSuffix(script(authenticated), initiateScript), "and then given the same script")
}
