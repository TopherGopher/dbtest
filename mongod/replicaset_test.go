package mongod_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockermock"
	"github.com/tophergopher/mongotest/mongod"
)

// shell makes a mocked daemon record the commands run inside the container,
// and answer each with result.
func shell(m *dockermock.Mock, result dockerclient.ExecResult, err error) (commands *[][]string) {
	commands = new([][]string)
	m.ExecFunc = func(_ context.Context, _ string, cmd ...string) (dockerclient.ExecResult, error) {
		*commands = append(*commands, cmd)
		return result, err
	}
	return commands
}

func TestWithReplicaSetStartsMongodAsAMember(t *testing.T) {
	m, _ := readyMock(t)

	c := mustStart(t, m, mongod.WithReplicaSet("rs0"))

	name, passed := flagValue(created(t, m).Cmd, "--replSet")
	require.True(t, passed, "mongod is started as a replica set member")
	assert.Equal(t, "rs0", name, "of the set that was asked for")
	assert.Equal(t, "rs0", c.ReplicaSet(), "which the container reports")
}

func TestWithReplicaSetInitiatesTheSet(t *testing.T) {
	m, _ := readyMock(t)
	commands := shell(m, dockerclient.ExecResult{}, nil)

	mustStart(t, m, mongod.WithReplicaSet("rs0"))

	assert.Len(t, *commands, 1, "a member that is never initiated accepts no writes, so Start initiates it")
}

// A command line is visible to every user of the daemon's host, so the
// password must reach the shell another way.
func TestThePasswordIsNotOnTheCommandLineThatInitiatesTheSet(t *testing.T) {
	m, _ := authMock(t)
	commands := shell(m, dockerclient.ExecResult{}, nil)

	mustStart(t, m, mongod.WithReplicaSet("rs0"), mongod.WithAuth("admin", "s3cret-marker"))

	require.Len(t, *commands, 1, "the set is initiated")
	for _, argument := range (*commands)[0] {
		assert.NotContains(t, argument, "s3cret-marker", "no argument carries the password")
	}
}

func TestAStartFailsWhenTheSetCannotBeInitiated(t *testing.T) {
	cases := []struct {
		name   string
		result dockerclient.ExecResult
		err    error
	}{
		{name: "the shell reports a failure", result: dockerclient.ExecResult{ExitCode: 1, Stderr: "MongoServerError: not authorized"}},
		{name: "the image has no shell to run", result: dockerclient.ExecResult{ExitCode: 127, Stdout: "exec: mongosh: executable file not found"}},
		{name: "the daemon cannot run anything", err: errors.New("the daemon refused the exec")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := readyMock(t)
			shell(m, tc.result, tc.err)

			_, err := start(t, m, mongod.WithReplicaSet("rs0"))

			require.ErrorIs(t, err, mongod.ErrNotInitiated, "a member that accepts no writes is not returned as ready")
			var failed *mongod.StartError
			require.ErrorAs(t, err, &failed, "the error says which step failed")
			assert.Equal(t, mongod.OpInitiate, failed.Op, "and it was the initiation")
			assert.Len(t, m.CallsTo("ContainerRemove"), 1, "the container is removed")
		})
	}
}

func TestAnInitiationThatOutlastsTheStartTimeoutIsNotReady(t *testing.T) {
	m, _ := readyMock(t)
	m.ExecFunc = func(ctx context.Context, _ string, _ ...string) (dockerclient.ExecResult, error) {
		<-ctx.Done() // a primary that is never elected
		return dockerclient.ExecResult{}, ctx.Err()
	}

	started := time.Now()
	_, err := start(t, m, mongod.WithReplicaSet("rs0"), mongod.WithStartTimeout(150*time.Millisecond))

	require.ErrorIs(t, err, mongod.ErrNotReady, "a set that never accepts writes is a start that ran out of time")
	assert.ErrorIs(t, err, context.DeadlineExceeded, "which the error says")
	assert.NotErrorIs(t, err, mongod.ErrNotInitiated, "it is not evidence that the image lacks a shell")
	assert.Less(t, time.Since(started), 5*time.Second, "and the wait and the initiation share the one timeout")
}

func TestAnEmptyReplicaSetNameIsRefused(t *testing.T) {
	m, _ := readyMock(t)

	_, err := start(t, m, mongod.WithReplicaSet(""))

	require.ErrorIs(t, err, mongod.ErrEmptyReplicaSet, "a caller who asked for a replica set must not get a standalone mongod")
	assert.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "it is an option mistake like the others")
	assert.Empty(t, m.CallsTo("ContainerCreate"), "and nothing is created")
}
