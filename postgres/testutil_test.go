package postgres_test

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tophergopher/dbtest/dockerclient"
	"github.com/tophergopher/dbtest/dockermock"
	"github.com/tophergopher/dbtest/postgres"
)

// containerID is the id the mocked daemon gives every container.
const containerID = "3f1a9c4b7e2d5a8f0b6c3e9d1a4f7b2c5e8d0a3f6b9c2e5d8a1f4b7c0e3d6a9f"

// serverMessage frames a message as the server does: its type, then its
// length, which counts itself and the body but not the type.
func serverMessage(kind byte, body string) []byte {
	msg := []byte{kind}
	msg = binary.BigEndian.AppendUint32(msg, uint32(len(body)+4))
	return append(msg, body...)
}

// askPassword stands in for a PostgreSQL server that accepts connections:
// it reads the startup message and asks for a SCRAM-SHA-256 password.
func askPassword(conn net.Conn) {
	defer conn.Close()
	if readStartup(conn) {
		_, _ = conn.Write(serverMessage('R', "\x00\x00\x00\x0aSCRAM-SHA-256\x00\x00"))
	}
}

// startingUp stands in for a server that is not yet accepting connections.
func startingUp(conn net.Conn) {
	defer conn.Close()
	if readStartup(conn) {
		_, _ = conn.Write(serverMessage('E', "SFATAL\x00C57P03\x00Mthe database system is starting up\x00\x00"))
	}
}

// silent reads the startup message and then says nothing, until the probe
// gives up and closes the connection.
func silent(conn net.Conn) {
	defer conn.Close()
	if readStartup(conn) {
		_, _ = io.Copy(io.Discard, conn)
	}
}

// hangUp is what Docker does on a published port while nothing listens
// behind it.
func hangUp(conn net.Conn) { _ = conn.Close() }

// readStartup reads one startup message and reports whether it was whole.
func readStartup(conn net.Conn) bool {
	var length [4]byte
	if _, err := io.ReadFull(conn, length[:]); err != nil {
		return false
	}
	rest := int64(binary.BigEndian.Uint32(length[:])) - 4
	_, err := io.CopyN(io.Discard, conn, rest)
	return err == nil
}

// serve opens a loopback socket, hands each connection to handle, and
// returns the port.
func serve(t testing.TB, handle func(net.Conn)) int {
	t.Helper()
	return serveAt(t, "127.0.0.1", handle)
}

// serveAt is serve on a given loopback address.
func serveAt(t testing.TB, address string, handle func(net.Conn)) int {
	t.Helper()
	ln, err := net.Listen("tcp", net.JoinHostPort(address, "0"))
	require.NoError(t, err, "readiness connects to a real socket")
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go handle(conn)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// servingMock returns a mocked daemon whose container is running, with the
// given stand-in for the server behind its published port, and that port.
func servingMock(t testing.TB, standIn func(net.Conn)) (*dockermock.Mock, int) {
	t.Helper()
	port := serve(t, standIn)
	return &dockermock.Mock{
		ContainerCreateFunc: func(context.Context, string, dockerclient.ContainerConfig) (string, []string, error) {
			return containerID, nil, nil
		},
		ContainerInspectFunc: func(context.Context, string) (dockerclient.ContainerInspect, error) {
			return running(port), nil
		},
	}, port
}

// readyMock is servingMock with a server that accepts connections.
func readyMock(t testing.TB) (*dockermock.Mock, int) {
	t.Helper()
	return servingMock(t, askPassword)
}

// running is what the daemon reports for a running container whose
// server port is published on hostPort.
func running(hostPort int) dockerclient.ContainerInspect {
	return dockerclient.ContainerInspect{
		ID:    containerID,
		State: dockerclient.ContainerState{Status: "running", Running: true},
		NetworkSettings: dockerclient.NetworkSettings{Ports: map[string][]dockerclient.PortBinding{
			"5432/tcp": {{HostIP: "127.0.0.1", HostPort: strconv.Itoa(hostPort)}},
		}},
	}
}

// start starts a container against a mocked daemon and stops it when the
// test ends.
func start(t testing.TB, m *dockermock.Mock, opts postgres.Options) (*postgres.Container, error) {
	t.Helper()
	c, err := postgres.StartWithOptions(context.Background(), opts.WithDocker(m))
	if err == nil {
		t.Cleanup(func() { _ = c.Stop(context.Background()) })
	}
	return c, err
}

// startWithin is start under a caller's deadline.
func startWithin(t testing.TB, m *dockermock.Mock, opts postgres.Options, d time.Duration) (*postgres.Container, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	c, err := postgres.StartWithOptions(ctx, opts.WithDocker(m))
	if err == nil {
		t.Cleanup(func() { _ = c.Stop(context.Background()) })
	}
	return c, err
}

// mustStart is start for a test that is about something after the start.
func mustStart(t testing.TB, m *dockermock.Mock, opts postgres.Options) *postgres.Container {
	t.Helper()
	c, err := start(t, m, opts)
	require.NoError(t, err, "the start must succeed")
	return c
}

// created returns the one create request a mocked daemon received, and the
// name it was given.
func created(t testing.TB, m *dockermock.Mock) (string, dockerclient.ContainerConfig) {
	t.Helper()
	calls := m.CallsTo("ContainerCreate")
	require.Len(t, calls, 1, "exactly one container is created")
	name, _ := calls[0].Args[0].(string)
	cfg, ok := calls[0].Args[1].(dockerclient.ContainerConfig)
	require.True(t, ok, "the create call records its request")
	return name, cfg
}

// logLine is one line a start logged: its level, and its keys and values.
type logLine struct {
	level string
	attrs map[string]any
}

// levelLogger keeps the level and attributes of every line, and not the
// message, which is free to change.
type levelLogger struct {
	mu    sync.Mutex
	lines []logLine
}

func (l *levelLogger) log(level string, keysAndValues []any) {
	attrs := map[string]any{}
	for i := 0; i+1 < len(keysAndValues); i += 2 {
		key, _ := keysAndValues[i].(string)
		attrs[key] = keysAndValues[i+1]
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, logLine{level: level, attrs: attrs})
}

func (l *levelLogger) Debug(_ string, kv ...any) { l.log("debug", kv) }
func (l *levelLogger) Info(_ string, kv ...any)  { l.log("info", kv) }
func (l *levelLogger) Warn(_ string, kv ...any)  { l.log("warn", kv) }
func (l *levelLogger) Error(_ string, kv ...any) { l.log("error", kv) }

// all returns every line logged.
func (l *levelLogger) all() []logLine {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]logLine(nil), l.lines...)
}

// about returns the lines logged at a level that carry a key with a value.
func (l *levelLogger) about(level, key string, value any) []logLine {
	var found []logLine
	for _, line := range l.at(level) {
		if line.attrs[key] == value {
			found = append(found, line)
		}
	}
	return found
}

// with returns the lines logged at a level that carry a key.
func (l *levelLogger) with(level, key string) []logLine {
	var found []logLine
	for _, line := range l.at(level) {
		if _, ok := line.attrs[key]; ok {
			found = append(found, line)
		}
	}
	return found
}

// at returns the lines logged at a level.
func (l *levelLogger) at(level string) []logLine {
	l.mu.Lock()
	defer l.mu.Unlock()
	var found []logLine
	for _, line := range l.lines {
		if line.level == level {
			found = append(found, line)
		}
	}
	return found
}
