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
	"github.com/tophergopher/dbtest/dockerclient"
	"github.com/tophergopher/dbtest/dockermock"
	"github.com/tophergopher/dbtest/mongod"
)

// The error reports what the last completed poll found. A poll the timeout
// cuts short says nothing new, so it does not replace that; a poll that
// completes does, so a stale finding is not reported.
func TestTheErrorReportsWhatTheLastCompletedPollFound(t *testing.T) {
	refusal := answerWithError("InterruptedAtShutdown", "shutting down")

	t.Run("a poll cut short keeps what the one before found", func(t *testing.T) {
		var connections atomic.Int32
		m, _ := servingMock(t, func(conn net.Conn) {
			if connections.Add(1) == 1 {
				refusal(conn)
				return
			}
			_, _ = io.Copy(io.Discard, conn) // reads, and never answers
		})

		_, err := start(t, m, mongod.DefaultOptions().WithStartTimeout(80*time.Millisecond))

		assert.ErrorIs(t, err, mongod.ErrHelloRefused, "the last poll's refusal is kept")
	})

	t.Run("a completed poll replaces what the one before found", func(t *testing.T) {
		var connections atomic.Int32
		m, _ := servingMock(t, func(conn net.Conn) {
			if connections.Add(1) == 1 {
				refusal(conn)
				return
			}
			_ = conn.Close() // mongod went away: Docker accepts and hangs up
		})

		_, err := start(t, m, mongod.DefaultOptions().WithStartTimeout(150*time.Millisecond))

		require.ErrorIs(t, err, mongod.ErrNoReply, "the last poll found nothing answering")
		assert.NotErrorIs(t, err, mongod.ErrHelloRefused, "so the earlier refusal is not reported")
		var notReady *mongod.NotReadyError
		require.ErrorAs(t, err, &notReady, "the error is a NotReadyError")
		assert.True(t, mongod.AddressInDoubt(notReady), "nothing answering is what a wrong address looks like")
	})
}

// A connection string with credentials must not hide an open database. A
// mongod that stays open until the start timeout is not returned.
func TestAMongodThatIgnoresItsCredentialsIsRefused(t *testing.T) {
	m, _ := servingMock(t, openMongod)

	_, err := start(t, m, mongod.DefaultOptions().WithStartTimeout(300*time.Millisecond).WithAuth("admin", "secret"))

	require.ErrorIs(t, err, mongod.ErrAuthNotEnforced, "an open mongod is not returned")
	assert.ErrorIs(t, err, mongod.ErrNotReady, "it was waited on, in case it was an image's set-up mongod")
	var open *mongod.AuthNotEnforcedError
	require.ErrorAs(t, err, &open, "the error names the container")
	assert.Equal(t, containerID, open.ID, "by the id the daemon gave it")
	assert.Len(t, m.CallsTo("ContainerRemove"), 1, "the open container is removed, not left running")
}

// An image first runs a mongod without authentication to create the user.
// Where a runtime lets the published port reach it, that mongod answers
// the credentials check, and the start waits for the one that replaces it.
func TestAnOpenSetUpMongodIsWaitedPast(t *testing.T) {
	var connections atomic.Int32
	m, _ := servingMock(t, func(conn net.Conn) {
		if connections.Add(1) <= 2 {
			openMongod(conn) // the set-up mongod
			return
		}
		enforcingMongod(conn) // the final one
	})

	_, err := start(t, m, mongod.DefaultOptions().WithStartTimeout(10*time.Second).WithAuth("admin", "secret"))

	assert.NoError(t, err, "the final mongod enforces the credentials, so the start succeeds")
}

// Readiness is decided by asking mongod, so a daemon that will not list a
// container's processes, as some socket proxies refuse to, does not matter.
func TestStartDoesNotNeedTheProcessList(t *testing.T) {
	refused := errors.New("the proxy refused GET /containers/{id}/top")
	cases := []struct {
		name string
		mock func(testing.TB) (*dockermock.Mock, int)
		opts mongod.Options
		top  func(context.Context, string) (dockerclient.Top, error)
	}{
		{"without credentials", readyMock, mongod.DefaultOptions(),
			func(context.Context, string) (dockerclient.Top, error) { return dockerclient.Top{}, refused }},
		{"with credentials", authMock, mongod.DefaultOptions().WithAuth("admin", "secret"),
			func(context.Context, string) (dockerclient.Top, error) { return dockerclient.Top{}, refused }},
		{"with credentials set in a configuration file", authMock, mongod.DefaultOptions().WithAuth("admin", "secret"),
			func(context.Context, string) (dockerclient.Top, error) {
				return processes("mongod --config /etc/mongod.conf --bind_ip_all"), nil
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := tc.mock(t)
			m.ContainerTopFunc = tc.top

			_, err := start(t, m, tc.opts.WithStartTimeout(10*time.Second))

			assert.NoError(t, err, "an enforcing mongod is ready")
		})
	}
}

// helloThen is a stand-in for mongod that answers hello, and every other
// command as other does.
func helloThen(other func(conn net.Conn) bool) func(net.Conn) {
	return func(conn net.Conn) {
		defer conn.Close()
		if !answerOne(conn, func(string) string { return okField }) {
			return
		}
		for other(conn) {
		}
	}
}

// When mongod's answer to the check of its credentials says neither yes nor
// no, it is asked again; if that never settles, the start is not ready.
func TestAnUnsettledCredentialsCheckIsNotReady(t *testing.T) {
	cases := map[string]func(net.Conn) bool{
		"an error other than Unauthorized": func(conn net.Conn) bool {
			return answerOne(conn, func(string) string {
				return notOKField + stringField("errmsg", "shutting down") + stringField("codeName", "InterruptedAtShutdown")
			})
		},
		"a hang-up":   func(net.Conn) bool { return false },
		"not a reply": func(conn net.Conn) bool { answerWithGarbage(conn); return false },
	}
	for name, other := range cases {
		t.Run(name, func(t *testing.T) {
			m, _ := servingMock(t, helloThen(other))

			_, err := start(t, m, mongod.DefaultOptions().WithStartTimeout(300*time.Millisecond).WithAuth("admin", "secret"))

			require.ErrorIs(t, err, mongod.ErrNotReady, "an unsettled check is waited on, until the timeout")
			assert.NotErrorIs(t, err, mongod.ErrAuthNotEnforced, "an unsettled check is not a mongod that ignores its credentials")
			assert.ErrorIs(t, err, mongod.ErrAuthUnknown, "the error says the credentials check was what never settled")
			var notReady *mongod.NotReadyError
			require.ErrorAs(t, err, &notReady, "the error is a NotReadyError")
			assert.False(t, mongod.AddressInDoubt(notReady), "mongod answered at that address, so advice to correct it would mislead")
			assert.Len(t, m.CallsTo("ContainerRemove"), 1, "the container is removed")
		})
	}
}

// One unsettled answer does not end the start: the next poll asks again.
func TestACredentialsCheckThatSettlesLaterIsReady(t *testing.T) {
	var asked atomic.Int32
	m, _ := servingMock(t, helloThen(func(conn net.Conn) bool {
		if asked.Add(1) <= 2 {
			return false // hang up the first times
		}
		return answerOne(conn, func(string) string {
			return notOKField + int32Field("code", 13) + stringField("codeName", "Unauthorized")
		})
	}))

	_, err := start(t, m, mongod.DefaultOptions().WithStartTimeout(10*time.Second).WithAuth("admin", "secret"))

	assert.NoError(t, err, "a refused command means credentials are enforced")
}

// A failed hello whose inspect also fails says nothing about the container,
// so the wait goes on.
func TestAContainerThatCannotBeInspectedIsWaitedOn(t *testing.T) {
	m := notReadyMock(t)
	var inspected atomic.Int32
	running := m.ContainerInspectFunc
	m.ContainerInspectFunc = func(ctx context.Context, id string) (dockerclient.ContainerInspect, error) {
		if inspected.Add(1) == 1 {
			return running(ctx, id) // the inspect that reads the published port
		}
		return dockerclient.ContainerInspect{}, errors.New("the daemon is busy")
	}

	_, err := start(t, m, mongod.DefaultOptions().WithStartTimeout(200*time.Millisecond))

	require.ErrorIs(t, err, mongod.ErrNotReady, "nothing showed the container had gone, so the start waited")
	assert.NotErrorIs(t, err, mongod.ErrContainerExited, "an inspect that failed is not a container that exited")
}

// Docker accepts connections on a published port soon after the container
// starts, and hangs up while nothing listens behind it, so a connection
// alone says nothing about mongod.
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
	assert.True(t, *healthy, "returned only once the image reported healthy")
}

func TestAHealthCheckThatNeverPassesIsNotReady(t *testing.T) {
	m, port := readyMock(t)
	health(m, port, math.MaxInt)

	_, err := start(t, m, mongod.DefaultOptions().WithStartTimeout(80*time.Millisecond))

	assert.ErrorIs(t, err, mongod.ErrNotReady, "the image never said its mongod was usable")
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
	assert.ErrorIs(t, err, mongod.ErrNotMongod, "and the error says that something else answered")
	var notReady *mongod.NotReadyError
	require.ErrorAs(t, err, &notReady, "the error is a NotReadyError")
	assert.True(t, mongod.AddressInDoubt(notReady), "the error says how to correct the address")
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
	assert.ErrorIs(t, err, mongod.ErrHelloRefused, "the server's own refusal reaches the caller")
	var notReady *mongod.NotReadyError
	require.ErrorAs(t, err, &notReady, "the error is a NotReadyError")
	assert.False(t, mongod.AddressInDoubt(notReady), "the error does not blame the address")
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
			var notReady *mongod.NotReadyError
			require.ErrorAs(t, err, &notReady, "the error is a NotReadyError")
			assert.False(t, mongod.AddressInDoubt(notReady), "the error does not blame the address")
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

			assert.NoError(t, err, "no check, nothing to wait for")
		})
	}
}
