package mongod

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockermock"
)

// These tests are inside the package because what a shell said reaches a
// caller only as the words of the error.

func TestWhatAFailedShellSaidReachesTheCaller(t *testing.T) {
	const said = "MongoServerError: not authorized on admin"
	m := &dockermock.Mock{ExecFunc: func(context.Context, string, ...string) (dockerclient.ExecResult, error) {
		return dockerclient.ExecResult{ExitCode: 1, Stderr: "\n" + said + "\n", Stdout: "Current Mongosh Log ID: 1a2b"}, nil
	}}
	c := &Container{id: "3f1a9c4b7e2d", name: "orders-db", docker: m, logger: dockerclient.NopLogger()}

	err := c.initiateReplicaSet(context.Background())

	require.ErrorIs(t, err, ErrNotInitiated, "the set was not initiated")
	var failed *shellError
	require.ErrorAs(t, err, &failed, "because a shell ran and failed")
	assert.Equal(t, "mongosh", failed.shell, "the error says which shell")
	assert.Equal(t, 1, failed.exitCode, "how it exited")
	assert.Equal(t, said, failed.output, "and what it wrote to standard error, which is where a shell explains itself")
}

func TestAShellsWordsAreQuotedButNeverAtLength(t *testing.T) {
	long := strings.Repeat("\u20ac", 5000)

	err := shellFailed("mongo", dockerclient.ExecResult{ExitCode: 252, Stdout: long})

	var failed *shellError
	require.ErrorAs(t, err, &failed, "a shell that failed is reported")
	assert.NotEmpty(t, failed.output, "a shell that wrote only to standard output is quoted from there")
	assert.LessOrEqual(t, len(failed.output), maxShellOutputLen, "but no shell can make the error as long as it likes")
	assert.True(t, strings.HasPrefix(long, failed.output), "what is kept is the start of what it wrote")
	assert.True(t, utf8.ValidString(failed.output), "cut between characters, not through one")
}
