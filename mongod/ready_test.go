package mongod_test

import (
	"context"
	"errors"
	"io"
	"math"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockermock"
	"github.com/tophergopher/mongotest/mongod"
)

// startWith starts a container whose process listing is scripted. The
// published port always answers, as it does from the moment a real container
// is created, so only the listing decides whether the start succeeds.
func startWith(t *testing.T, opts mongod.Options, commands func(poll int) []string) error {
	t.Helper()
	m, _ := readyMock(t)
	polls := 0
	m.ContainerTopFunc = func(context.Context, string) (dockerclient.Top, error) {
		polls++
		return processes(commands(polls)...), nil
	}
	_, err := start(t, m, opts)
	return err
}

// always is a process listing that never changes.
func always(command string) func(int) []string {
	return func(int) []string { return []string{command} }
}

// The command lines are the ones each image was seen running during a start.
func TestStartCountsOnlyAMongodThePortCanReach(t *testing.T) {
	cases := []struct {
		name    string
		command string
		ready   bool
	}{
		{"official image serving", "mongod --bind_ip_all", true},
		{"serving with auth and a replica set", "mongod --auth --replSet rs0 --keyFile /etc/keyfile --bind_ip_all", true},
		{"Atlas Local serving", "mongod --replSet 142b --keyFile /data/configdb/keyfile --bind_ip_all --auth", true},
		{"an explicit wildcard address", "mongod --bind_ip 0.0.0.0", true},
		{"an address list reaching beyond loopback", "mongod --bind_ip=localhost,172.17.0.2", true},
		{"flags with one dash", "mongod -bind_ip_all", true},
		{"a configuration file alone", "mongod --config /etc/mongod.conf", true},
		{"a configuration file, short flag", "mongod -f /etc/mongod.conf", true},
		{"given by path", "/usr/bin/mongod --bind_ip_all", true},

		{"official image creating its root user", "mongod --bind_ip 127.0.0.1 --port 27017 --fork", false},
		{"community image creating its root user", "/usr/bin/mongod --bind_ip 127.0.0.1 --port 27017 --fork", false},
		{"temporary mongod that was given a config file", "mongod --auth --config /tmp/temp-config.json --bind_ip 127.0.0.1 --fork", false},
		{"Atlas Local initiating its replica set", "mongod --replSet 142b --keyFile /data/configdb/keyfile --transitionToAuth", false},
		{"the entrypoint stopping its temporary mongod", "mongod --bind_ip_all --shutdown", false},
		{"loopback addresses and a socket", "mongod --bind_ip localhost,::1,/tmp/mongod.sock", false},
		{"an address flag with no address", "mongod --bind_ip --quiet", false},
		{"no flags, so mongod's localhost default", "mongod", false},
		{"the entrypoint script, not mongod", "bash /usr/local/bin/docker-entrypoint.sh mongod", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			timeout := 5 * time.Second
			if !tc.ready {
				timeout = 60 * time.Millisecond
			}
			err := startWith(t, mongod.DefaultOptions().WithStartTimeout(timeout), always(tc.command))

			if tc.ready {
				assert.NoError(t, err, "a published port leads to this mongod")
				return
			}
			assert.ErrorIs(t, err, mongod.ErrNotReady, "nothing outside the container can reach this process")
		})
	}
}

func TestStartFindsTheCommandColumnInAnyListing(t *testing.T) {
	const serving = "mongod --bind_ip_all"
	cases := []struct {
		name string
		top  dockerclient.Top
	}{
		{"the command first", dockerclient.Top{
			Titles: []string{"COMMAND", "PID", "USER"}, Processes: [][]string{{serving, "1", "mongodb"}}}},
		{"a title in lower case", dockerclient.Top{
			Titles: []string{"pid", "command", "user"}, Processes: [][]string{{"1", serving, "mongodb"}}}},
		{"the ARGS title", dockerclient.Top{
			Titles: []string{"PID", "ARGS", "USER"}, Processes: [][]string{{"1", serving, "mongodb"}}}},
		{"no title it knows, so the last column", dockerclient.Top{
			Titles: []string{"PID", "WHAT"}, Processes: [][]string{{"1", serving}}}},
		{"no titles at all", dockerclient.Top{Processes: [][]string{{serving}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := readyMock(t)
			m.ContainerTopFunc = func(context.Context, string) (dockerclient.Top, error) { return tc.top, nil }

			_, err := start(t, m, mongod.DefaultOptions().WithStartTimeout(5*time.Second))

			assert.NoError(t, err, "a daemon may title and order the columns its own way")
		})
	}
}

func TestStartWaitsPastTheTemporaryMongod(t *testing.T) {
	sawFinal := false
	err := startWith(t, mongod.DefaultOptions().WithStartTimeout(10*time.Second), func(poll int) []string {
		if poll <= 3 {
			return []string{"bash docker-entrypoint.sh mongod", "mongod --bind_ip 127.0.0.1 --fork"}
		}
		sawFinal = true
		return []string{"mongod --bind_ip_all"}
	})

	require.NoError(t, err, "the final mongod arrives inside the timeout")
	assert.True(t, sawFinal, "the temporary mongod is about to be shut down, so it does not count")
}

func TestAMongodStuckOnLoopbackIsNamedInTheError(t *testing.T) {
	err := startWith(t, mongod.DefaultOptions().WithStartTimeout(60*time.Millisecond), always("mongod --bind_ip 127.0.0.1"))

	require.ErrorIs(t, err, mongod.ErrNotReady, "it never became reachable")
	assert.ErrorIs(t, err, mongod.ErrLoopbackOnly, "so the caller can tell this from a mongod that never started")
}

func TestALoopbackOnlyMongodIsStillNamedWhenTheTimeoutCutsAPollShort(t *testing.T) {
	m, _ := readyMock(t)
	polls := 0
	m.ContainerTopFunc = func(ctx context.Context, _ string) (dockerclient.Top, error) {
		polls++
		if polls == 1 {
			return processes("mongod --bind_ip 127.0.0.1"), nil
		}
		<-ctx.Done() // every later listing is still in flight when the timeout passes
		return dockerclient.Top{}, ctx.Err()
	}

	_, err := start(t, m, mongod.DefaultOptions().WithStartTimeout(80*time.Millisecond))

	assert.ErrorIs(t, err, mongod.ErrLoopbackOnly, "what the wait last learned is kept, not replaced by the timeout that ended it")
}

func TestStartRequiresAuthWhenCredentialsWereSet(t *testing.T) {
	cases := []struct {
		name     string
		command  string
		enforced bool
	}{
		{"--auth", "mongod --auth --bind_ip_all", true},
		{"a key file implies it", "mongod --replSet rs0 --keyFile /etc/keyfile --bind_ip_all", true},
		{"the image ignored the credentials", "mongod --bind_ip_all", false},
		{"a configuration file, which cannot be read to check", "mongod --config /etc/mongod.conf --bind_ip_all", false},
		{"Atlas Local without a root user", "mongod --replSet 1f3c --keyFile /data/keyfile --bind_ip_all --transitionToAuth", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			started := time.Now()
			err := startWith(t, mongod.DefaultOptions().WithStartTimeout(time.Minute).WithAuth("admin", "secret"), always(tc.command))

			if tc.enforced {
				assert.NoError(t, err, "this mongod makes clients authenticate")
				return
			}
			require.ErrorIs(t, err, mongod.ErrAuthNotEnforced, "an open mongod must not be returned behind a URI that claims credentials")
			assert.Less(t, time.Since(started), 10*time.Second, "this mongod will not change, so there is nothing to wait for")
		})
	}
}

// Docker accepts connections on a published port from the moment the
// container exists, so a connection alone says nothing about mongod.
func TestAPortThatOnlyAcceptsConnectionsIsNotReady(t *testing.T) {
	m, _ := readyMock(t)
	port := acceptOnlyPort(t)
	m.ContainerInspectFunc = func(context.Context, string) (dockerclient.ContainerInspect, error) {
		return running(port), nil
	}

	_, err := start(t, m, mongod.DefaultOptions().WithStartTimeout(80*time.Millisecond))

	assert.ErrorIs(t, err, mongod.ErrNotReady, "nothing answered a command, so no mongod is serving there")
}

func TestStartKeepsAskingUntilMongodAnswers(t *testing.T) {
	var connections atomic.Int32
	const unanswered = 3
	port := serve(t, func(conn net.Conn) {
		if connections.Add(1) <= unanswered {
			_ = conn.Close()
			return
		}
		answerCommand(conn)
	})
	m, _ := readyMock(t)
	m.ContainerInspectFunc = func(context.Context, string) (dockerclient.ContainerInspect, error) {
		return running(port), nil
	}

	_, err := start(t, m, mongod.DefaultOptions())

	require.NoError(t, err, "mongod answers inside the timeout")
	assert.Greater(t, connections.Load(), int32(unanswered), "the connections that were hung up on did not count")
}

// health scripts the health an image's own check reports: "starting" until
// the inspection numbered healthyFrom.
func health(m *dockermock.Mock, port, healthyFrom int) (healthy *bool) {
	healthy = new(bool)
	inspections := 0
	m.ContainerInspectFunc = func(context.Context, string) (dockerclient.ContainerInspect, error) {
		inspections++
		info := running(port)
		info.State.Health.Status = "starting"
		if inspections >= healthyFrom {
			info.State.Health.Status, *healthy = "healthy", true
		}
		return info, nil
	}
	return healthy
}

// Atlas Local answers commands from a mongod it is about to replace. Only
// its health check says the final one is serving.
func TestStartWaitsForTheHealthCheckAnImageDeclares(t *testing.T) {
	m, port := readyMock(t)
	healthy := health(m, port, 4)

	_, err := start(t, m, mongod.DefaultOptions())

	require.NoError(t, err, "the check passes inside the timeout")
	assert.True(t, *healthy, "a mongod that answers is not returned while its image says it is still starting")
}

func TestAHealthCheckThatNeverPassesIsNotReady(t *testing.T) {
	m, port := readyMock(t)
	health(m, port, math.MaxInt)

	_, err := start(t, m, mongod.DefaultOptions().WithStartTimeout(80*time.Millisecond))

	assert.ErrorIs(t, err, mongod.ErrNotReady, "the image never said its mongod was usable")
}

func TestAMongodThatWentAwayIsNotReportedAsStuckOnLoopback(t *testing.T) {
	err := startWith(t, mongod.DefaultOptions().WithStartTimeout(150*time.Millisecond), func(poll int) []string {
		if poll == 1 {
			return []string{"mongod --bind_ip 127.0.0.1 --fork"}
		}
		return []string{"bash docker-entrypoint.sh mongod"}
	})

	require.ErrorIs(t, err, mongod.ErrNotReady, "it never became reachable")
	assert.NotErrorIs(t, err, mongod.ErrLoopbackOnly, "when the wait ended no mongod was running, so advice about its bind address would mislead")
}

func TestAPortWhereSomethingOtherThanMongodAnswersIsNotReady(t *testing.T) {
	m, _ := readyMock(t)
	port := serve(t, func(conn net.Conn) {
		defer conn.Close()
		_, _ = io.CopyN(io.Discard, conn, 16)
		_, _ = conn.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
	})
	m.ContainerInspectFunc = func(context.Context, string) (dockerclient.ContainerInspect, error) {
		return running(port), nil
	}

	_, err := start(t, m, mongod.DefaultOptions().WithStartTimeout(80*time.Millisecond))

	require.ErrorIs(t, err, mongod.ErrNotReady, "a reply that is not mongod's does not make the address a mongod")
	assert.Contains(t, err.Error(), mongod.EnvHostIP, "the address may be the wrong one, so the error says how to correct it")
}

func TestAPortThatAnswersWithGarbageIsNotReady(t *testing.T) {
	m, _ := readyMock(t)
	port := serve(t, answerWithGarbage)
	m.ContainerInspectFunc = func(context.Context, string) (dockerclient.ContainerInspect, error) {
		return running(port), nil
	}

	_, err := start(t, m, mongod.DefaultOptions().WithStartTimeout(80*time.Millisecond))

	assert.ErrorIs(t, err, mongod.ErrNotReady, "bytes that are not a MongoDB reply do not count as one")
}

// A mongod that is shutting down still answers, with an error.
func TestAMongodThatAnswersWithAnErrorIsNotReady(t *testing.T) {
	m, _ := readyMock(t)
	port := serve(t, answerWithError("InterruptedAtShutdown", "interrupted at shutdown"))
	m.ContainerInspectFunc = func(context.Context, string) (dockerclient.ContainerInspect, error) {
		return running(port), nil
	}

	_, err := start(t, m, mongod.DefaultOptions().WithStartTimeout(80*time.Millisecond))

	require.ErrorIs(t, err, mongod.ErrNotReady, "a server that refuses commands is not ready for a test's")
	assert.Contains(t, err.Error(), "InterruptedAtShutdown", "the server's own reason reaches the caller")
	assert.NotContains(t, err.Error(), mongod.EnvHostIP, "a mongod answered, so the address is right and advice about it would mislead")
}

// A server from before the hello command says it does not know it, which
// only a serving mongod can do.
func TestAMongodTooOldToKnowTheCommandIsStillReady(t *testing.T) {
	m, _ := readyMock(t)
	port := serve(t, answerWithError("CommandNotFound", "no such command: 'hello'"))
	m.ContainerInspectFunc = func(context.Context, string) (dockerclient.ContainerInspect, error) {
		return running(port), nil
	}

	_, err := start(t, m, mongod.DefaultOptions())

	assert.NoError(t, err, "an old server is still a server")
}

func TestAFailureToAskTheDaemonIsNotBlamedOnTheAddress(t *testing.T) {
	refused := errors.New("EOF")
	failures := map[string]func(*dockermock.Mock, int){
		"listing the processes": func(m *dockermock.Mock, _ int) {
			m.ContainerTopFunc = func(context.Context, string) (dockerclient.Top, error) { return dockerclient.Top{}, refused }
		},
		"reading the health check": func(m *dockermock.Mock, port int) {
			inspections := 0
			m.ContainerInspectFunc = func(context.Context, string) (dockerclient.ContainerInspect, error) {
				inspections++
				if inspections > 1 {
					return dockerclient.ContainerInspect{}, refused
				}
				info := running(port)
				info.State.Health.Status = "starting"
				return info, nil
			}
		},
	}
	for name, fail := range failures {
		t.Run(name, func(t *testing.T) {
			m, port := readyMock(t)
			fail(m, port)

			_, err := start(t, m, mongod.DefaultOptions().WithStartTimeout(80*time.Millisecond))

			require.ErrorIs(t, err, mongod.ErrNotReady, "the start runs out of time")
			assert.ErrorIs(t, err, refused, "the daemon's own error is kept")
			assert.NotContains(t, err.Error(), mongod.EnvHostIP, "the daemon failed, not the address, so advice about the address would mislead")
		})
	}
}

func TestAPortThatNeverAnswersDoesNotHoldTheStartPastItsTimeout(t *testing.T) {
	m, _ := readyMock(t)
	port := serve(t, func(conn net.Conn) {
		defer conn.Close()
		_, _ = io.Copy(io.Discard, conn) // reads the command and says nothing
	})
	m.ContainerInspectFunc = func(context.Context, string) (dockerclient.ContainerInspect, error) {
		return running(port), nil
	}

	started := time.Now()
	_, err := start(t, m, mongod.DefaultOptions().WithStartTimeout(100*time.Millisecond))

	require.ErrorIs(t, err, mongod.ErrNotReady, "nothing answered")
	assert.Less(t, time.Since(started), time.Second, "and the wait for a reply ends with the start timeout, not after it")
}

func TestAnImageWithNoHealthCheckIsNotWaitedOn(t *testing.T) {
	for _, status := range []string{"", "none"} {
		t.Run("status "+strconv.Quote(status), func(t *testing.T) {
			m, port := readyMock(t)
			m.ContainerInspectFunc = func(context.Context, string) (dockerclient.ContainerInspect, error) {
				info := running(port)
				info.State.Health.Status = status
				return info, nil
			}

			_, err := start(t, m, mongod.DefaultOptions().WithStartTimeout(5*time.Second))

			assert.NoError(t, err, "this is what the daemon reports for an image that declares no check, so there is nothing to wait for")
		})
	}
}
