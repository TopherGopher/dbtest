package mongod_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/mongod"
)

// mentions reports whether any argument of a command contains s.
func mentions(cmd []string, s string) bool {
	return slices.ContainsFunc(cmd, func(arg string) bool { return strings.Contains(arg, s) })
}

func TestExecRunsTheCommandInTheContainer(t *testing.T) {
	m, _ := readyMock(t)
	c := mustStart(t, m, mongod.DefaultOptions())
	var ranIn string
	var ran []string
	m.ExecFunc = func(_ context.Context, id string, cmd ...string) (dockerclient.ExecResult, error) {
		ranIn, ran = id, cmd
		return dockerclient.ExecResult{Stdout: "out", Stderr: "err", ExitCode: 3}, nil
	}

	result, err := c.Exec(context.Background(), "ls", "/data/db")

	require.NoError(t, err, "a command that ran is a result, whatever its exit code")
	assert.Equal(t, c.ID(), ranIn, "the command runs in this container")
	assert.Equal(t, []string{"ls", "/data/db"}, ran, "exactly as given")
	assert.Equal(t, dockerclient.ExecResult{Stdout: "out", Stderr: "err", ExitCode: 3}, result, "the caller sees what the command produced")
}

func TestRunScriptReturnsWhatTheShellPrinted(t *testing.T) {
	m, _ := readyMock(t)
	c := mustStart(t, m, mongod.DefaultOptions())
	commands := shell(m, dockerclient.ExecResult{Stdout: "1\n"}, nil)

	out, err := c.RunScript(context.Background(), "db.runCommand({ping:1}).ok")

	require.NoError(t, err, "a script that succeeds is not an error")
	assert.Equal(t, "1", out, "the value the script printed, without the line break")
	require.Len(t, *commands, 1, "one shell is run")
	assert.Equal(t, "mongosh", (*commands)[0][0], "the current shell is used when the image has it")
	assert.True(t, mentions((*commands)[0], "db.runCommand({ping:1}).ok"), "and it evaluates the caller's script")
}

func TestRunScriptUsesTheLegacyShellWhenThatIsAllTheImageHas(t *testing.T) {
	m, _ := readyMock(t)
	c := mustStart(t, m, mongod.DefaultOptions())
	tried := shellsThatAnswer(m, map[string]dockerclient.ExecResult{"mongosh": notInTheImage, "mongo": {Stdout: "1"}})

	out, err := c.RunScript(context.Background(), "db.runCommand({ping:1}).ok")

	require.NoError(t, err, "an image from before MongoDB 5.0 can still run a script")
	assert.Equal(t, "1", out, "with the legacy shell's output")
	assert.Equal(t, []string{"mongosh", "mongo"}, *tried, "which is tried only when the current one is missing")
}

func TestAScriptThatFailsIsAScriptError(t *testing.T) {
	m, _ := readyMock(t)
	c := mustStart(t, m, mongod.DefaultOptions())
	shell(m, dockerclient.ExecResult{ExitCode: 1, Stdout: "partial", Stderr: "Uncaught Error: boom"}, nil)

	out, err := c.RunScript(context.Background(), "throw new Error('boom')")

	require.ErrorIs(t, err, mongod.ErrScriptFailed, "a failed script matches the sentinel")
	var scriptErr *mongod.ScriptError
	require.ErrorAs(t, err, &scriptErr, "and carries what the shell said")
	assert.Equal(t, 1, scriptErr.ExitCode, "the shell's exit code")
	assert.Equal(t, "Uncaught Error: boom", scriptErr.Stderr, "what it wrote to standard error")
	assert.Equal(t, "partial", scriptErr.Stdout, "and to standard output")
	assert.Equal(t, "mongosh", scriptErr.Shell, "and which shell ran it")
	assert.Empty(t, out, "no output is returned beside an error")
}

// The legacy shell writes what went wrong to standard output, and a shell
// can write any amount: the error quotes the start of it, and the field
// keeps the whole.
func TestAScriptErrorQuotesTheShellButNeverAtLength(t *testing.T) {
	m, _ := readyMock(t)
	c := mustStart(t, m, mongod.DefaultOptions())
	long := strings.Repeat("€", 5000)
	shell(m, dockerclient.ExecResult{ExitCode: 252, Stdout: long}, nil)

	_, err := c.RunScript(context.Background(), "throw new Error('boom')")

	var scriptErr *mongod.ScriptError
	require.ErrorAs(t, err, &scriptErr, "the script failed")
	assert.Equal(t, long, scriptErr.Stdout, "the field holds everything the shell wrote")
	quoted := strings.Count(err.Error(), "€")
	assert.Positive(t, quoted, "a shell that wrote only to standard output is quoted from there")
	assert.Less(t, quoted, 5000/10, "but no shell can make the error as long as it likes")
	assert.True(t, utf8.ValidString(err.Error()), "and the quote is cut between characters, not through one")
}

func TestRunScriptOnAnImageWithNoShell(t *testing.T) {
	m, _ := readyMock(t)
	c := mustStart(t, m, mongod.DefaultOptions())
	shellsThatAnswer(m, map[string]dockerclient.ExecResult{"mongosh": notInTheImage, "mongo": notRunnable})

	_, err := c.RunScript(context.Background(), "1")

	assert.ErrorIs(t, err, mongod.ErrNoShell, "the image has nothing to run a script with")
	assert.NotErrorIs(t, err, mongod.ErrScriptFailed, "which is not the script's fault")
}

func TestRunScriptReportsADaemonThatCouldNotRunTheShell(t *testing.T) {
	m, _ := readyMock(t)
	c := mustStart(t, m, mongod.DefaultOptions())
	refused := errors.New("daemon unreachable")
	shell(m, dockerclient.ExecResult{}, refused)

	_, err := c.RunScript(context.Background(), "1")

	assert.ErrorIs(t, err, refused, "the daemon's own error reaches the caller")
	assert.NotErrorIs(t, err, mongod.ErrScriptFailed, "the script never ran, so it did not fail")
}

func TestRunScriptNeverPutsThePasswordOnTheCommandLine(t *testing.T) {
	const password = "s3cret-marker"
	m, _ := authMock(t)
	c := mustStart(t, m, mongod.DefaultOptions().WithAuth("admin", password))
	commands := shell(m, dockerclient.ExecResult{Stdout: "1"}, nil)

	_, err := c.RunScript(context.Background(), "db.runCommand({ping:1}).ok")

	require.NoError(t, err, "the script runs")
	require.Len(t, *commands, 1, "in one shell")
	assert.False(t, mentions((*commands)[0], password),
		"the shell reads the password inside the container: a command line is visible to every user of the daemon's host")
}

// The shell has to log in on a container with credentials and not try to on
// one without, because mongod refuses most commands from a shell that has not
// and refuses a login when it has no users. Logging in must also not take a
// name the caller's script uses. The legacy shell reads the credentials
// another way, so it is tried too.
func TestIntegrationRunScriptAnswersWithAndWithoutCredentials(t *testing.T) {
	legacy := mongod.ImagePublicECR.WithVersion("4.4")
	for name, opts := range map[string]mongod.Options{
		"without credentials":              mongod.DefaultOptions(),
		"with credentials":                 mongod.DefaultOptions().WithAuth("admin", "s3cret"),
		"legacy shell without credentials": mongod.DefaultOptions().WithMongoImage(legacy),
		"legacy shell with credentials":    mongod.DefaultOptions().WithMongoImage(legacy).WithAuth("admin", "s3cret"),
	} {
		t.Run(name, func(t *testing.T) {
			docker, ctx := liveDocker(t)
			key, value := runLabel(t)
			requireImage(t, ctx, docker, legacy)
			c, err := mongod.StartWithOptions(ctx, opts.WithDocker(docker).WithLabel(key, value))
			require.NoError(t, err, "mongod integration: cannot start a container to run a script in")
			t.Cleanup(func() { _ = c.Stop(context.Background()) })

			out, err := c.RunScript(ctx, "const env = db.runCommand({ping:1}).ok; env")

			require.NoError(t, err, "the script runs")
			assert.Equal(t, "1", out, "and its value is what RunScript returns")
		})
	}
}

func TestIntegrationAScriptThatThrowsIsAScriptError(t *testing.T) {
	docker, ctx := liveDocker(t)
	key, value := runLabel(t)
	c, err := mongod.StartWithOptions(ctx, mongod.DefaultOptions().WithDocker(docker).WithLabel(key, value))
	require.NoError(t, err, "mongod integration: cannot start a container to run a script in")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	_, err = c.RunScript(ctx, "throw new Error('boom')")

	require.ErrorIs(t, err, mongod.ErrScriptFailed, "a script that throws has failed")
	var scriptErr *mongod.ScriptError
	require.ErrorAs(t, err, &scriptErr, "and the error carries what the shell said")
	assert.NotZero(t, scriptErr.ExitCode, "the shell exits non-zero")
	assert.Contains(t, scriptErr.Stderr+scriptErr.Stdout, "boom", "and says what the script threw")
}
