package mongod_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockermock"
	"github.com/tophergopher/mongotest/mongod"
)

// containerID is the id the mocked daemon gives every container.
const containerID = "3f1a9c4b7e2d5a8f0b6c3e9d1a4f7b2c5e8d0a3f6b9c2e5d8a1f4b7c0e3d6a9f"

// siblingGateway is an address that is not loopback, which makes Start
// publish on every interface.
const siblingGateway = "172.17.0.1"

// listenerPort opens a loopback socket where a stand-in for mongod answers
// commands, and returns its port.
func listenerPort(t testing.TB) int {
	t.Helper()
	return serve(t, answerCommand)
}

// acceptOnlyPort opens a loopback socket that accepts a connection and hangs
// up, which is what Docker does on a published port until mongod is
// listening behind it.
func acceptOnlyPort(t testing.TB) int {
	t.Helper()
	return serve(t, func(conn net.Conn) { _ = conn.Close() })
}

// serve opens a loopback socket, hands each connection to handle, and
// returns the port.
func serve(t testing.TB, handle func(net.Conn)) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "readiness connects to a real socket")
	t.Cleanup(func() { _ = ln.Close() })
	go acceptAll(ln, handle)
	return ln.Addr().(*net.TCPAddr).Port
}

// acceptAll hands every connection to handle until the listener closes.
func acceptAll(ln net.Listener, handle func(net.Conn)) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go handle(conn)
	}
}

// Fields of a reply, in BSON: ok as a double, and a string.
const (
	okField    = "\x01ok\x00\x00\x00\x00\x00\x00\x00\xf0\x3f"
	notOKField = "\x01ok\x00\x00\x00\x00\x00\x00\x00\x00\x00"
)

// stringField is a BSON string element.
func stringField(name, value string) string {
	length := binary.LittleEndian.AppendUint32(nil, uint32(len(value)+1))
	return "\x02" + name + "\x00" + string(length) + value + "\x00"
}

// answerCommand replies to one MongoDB command the way mongod does when all
// is well: with {ok: 1}.
func answerCommand(conn net.Conn) { answerWith(okField)(conn) }

// answerWithError replies the way mongod does when it refuses a command:
// with ok 0, a message and the name of the error.
func answerWithError(codeName, message string) func(net.Conn) {
	return answerWith(notOKField + stringField("errmsg", message) + stringField("codeName", codeName))
}

// answerWithGarbage reads the command and replies with bytes that are not a
// MongoDB message, as another service on the port, or a broken one, would.
func answerWithGarbage(conn net.Conn) {
	defer conn.Close()
	_, _ = io.CopyN(io.Discard, conn, 16)
	_, _ = conn.Write([]byte("\x8f\x02\x93\x11 not a mongod \x00\xff\xfe\x00\x01\x02\x03\x04\x05\x06"))
}

// answerWith returns a stand-in for mongod that replies to one command with
// a message of the same kind, addressed to the request, holding a document
// of the given fields. It hangs up on a command whose document is malformed.
func answerWith(fields string) func(net.Conn) {
	return func(conn net.Conn) { answer(conn, fields) }
}

// answer reads one command and writes the reply.
func answer(conn net.Conn, fields string) {
	defer conn.Close()
	const headerLen, opMsg = 16, 2013
	okDocument := fields + "\x00"

	var header [headerLen]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return
	}
	le := binary.LittleEndian
	body := make([]byte, int(le.Uint32(header[0:4]))-headerLen)
	if _, err := io.ReadFull(conn, body); err != nil {
		return
	}
	// mongod refuses a document whose declared length is not its real one.
	const flagsAndKindLen = 5
	if len(body) < flagsAndKindLen+4 {
		return
	}
	document := body[flagsAndKindLen:]
	if int(le.Uint32(document[0:4])) != len(document) {
		return
	}

	reply := le.AppendUint32(nil, uint32(headerLen+4+1+4+len(okDocument)))
	reply = le.AppendUint32(reply, 1)     // this message's own id
	reply = append(reply, header[4:8]...) // the id of the request it answers
	reply = le.AppendUint32(reply, opMsg) // the message kind
	reply = le.AppendUint32(reply, 0)     // no flags
	reply = append(reply, 0)              // one document follows
	reply = le.AppendUint32(reply, uint32(4+len(okDocument)))
	_, _ = conn.Write(append(reply, okDocument...))
}

// askHello sends one hello command to an address and reports whether a
// mongod answered it with ok. It is written apart from the package's own
// hello so that a mistake in one is not repeated in the other.
func askHello(address string) error {
	const headerLen, opMsg, requestID = 16, 2013, 7
	const document = "\x10hello\x00\x01\x00\x00\x00" + "\x02$db\x00\x06\x00\x00\x00admin\x00" + "\x00"
	const okElement = "\x01ok\x00\x00\x00\x00\x00\x00\x00\xf0\x3f"

	conn, err := net.DialTimeout("tcp", address, 2*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	le := binary.LittleEndian
	message := le.AppendUint32(nil, uint32(headerLen+4+1+4+len(document)))
	message = le.AppendUint32(message, requestID)
	message = le.AppendUint32(message, 0)     // answers nothing
	message = le.AppendUint32(message, opMsg) // the message kind
	message = le.AppendUint32(message, 0)     // no flags
	message = append(message, 0)              // one document follows
	message = le.AppendUint32(message, uint32(4+len(document)))
	if _, err := conn.Write(append(message, document...)); err != nil {
		return err
	}

	var header [headerLen]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return err
	}
	body := make([]byte, int(le.Uint32(header[0:4]))-headerLen)
	if _, err := io.ReadFull(conn, body); err != nil {
		return err
	}
	if le.Uint32(header[8:12]) != requestID || !bytes.Contains(body, []byte(okElement)) {
		return errors.New("the reply is not mongod saying ok to the hello")
	}
	return nil
}

// closedPort returns a loopback port nothing is listening on.
func closedPort(t testing.TB) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "the test needs a free port")
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close(), "and nothing listening on it")
	return port
}

// processes is a process listing with one row per command line.
func processes(commands ...string) dockerclient.Top {
	top := dockerclient.Top{Titles: []string{"PID", "CMD"}}
	for _, command := range commands {
		top.Processes = append(top.Processes, []string{"1", command})
	}
	return top
}

// readyMock returns a mocked daemon whose container is running a serving
// mongod, and the port it reports the container published, which something
// really listens on.
func readyMock(t testing.TB) (*dockermock.Mock, int) {
	t.Helper()
	port := listenerPort(t)
	return &dockermock.Mock{
		ContainerCreateFunc: func(context.Context, string, dockerclient.ContainerConfig) (string, []string, error) {
			return containerID, nil, nil
		},
		ContainerInspectFunc: func(context.Context, string) (dockerclient.ContainerInspect, error) {
			return running(port), nil
		},
		ContainerTopFunc: func(context.Context, string) (dockerclient.Top, error) {
			return processes("mongod --bind_ip_all"), nil
		},
	}, port
}

// authMock is readyMock for a container created with credentials: its mongod
// enforces them, which Start insists on.
func authMock(t testing.TB) (*dockermock.Mock, int) {
	t.Helper()
	m, port := readyMock(t)
	m.ContainerTopFunc = func(context.Context, string) (dockerclient.Top, error) {
		return processes("mongod --auth --bind_ip_all"), nil
	}
	return m, port
}

// running is what the daemon reports for a running container whose mongod
// port is published on hostPort.
func running(hostPort int) dockerclient.ContainerInspect {
	return dockerclient.ContainerInspect{
		ID:    containerID,
		State: dockerclient.ContainerState{Status: "running", Running: true},
		NetworkSettings: dockerclient.NetworkSettings{Ports: map[string][]dockerclient.PortBinding{
			"27017/tcp": {{HostIP: "127.0.0.1", HostPort: strconv.Itoa(hostPort)}},
		}},
	}
}

// start starts a container against a mocked daemon and stops it when the
// test ends. TestMain pins the dial address to loopback, so the result does
// not depend on where the tests run; WithHostIP still overrides it.
func start(t testing.TB, m *dockermock.Mock, opts mongod.Options) (*mongod.Container, error) {
	t.Helper()
	c, err := mongod.StartWithOptions(context.Background(), opts.WithDocker(m))
	if err == nil {
		t.Cleanup(func() { _ = c.Stop(context.Background()) })
	}
	return c, err
}

// mustStart is start for a test that is about something after the start.
func mustStart(t testing.TB, m *dockermock.Mock, opts mongod.Options) *mongod.Container {
	t.Helper()
	c, err := start(t, m, opts)
	require.NoError(t, err, "the start must succeed")
	return c
}

// created returns the one create request a mocked daemon received.
func created(t testing.TB, m *dockermock.Mock) dockerclient.ContainerConfig {
	t.Helper()
	calls := m.CallsTo("ContainerCreate")
	require.Len(t, calls, 1, "exactly one container is created")
	cfg, ok := calls[0].Args[1].(dockerclient.ContainerConfig)
	require.True(t, ok, "the create call records its request")
	return cfg
}

// createdName returns the name of the one container a mocked daemon created.
func createdName(t testing.TB, m *dockermock.Mock) string {
	t.Helper()
	calls := m.CallsTo("ContainerCreate")
	require.Len(t, calls, 1, "exactly one container is created")
	name, _ := calls[0].Args[0].(string)
	return name
}

// requestFor returns the create request a set of options produces. The
// daemon never reports a mongod, so the start ends on a short timeout: the
// request is built before anything is waited for, and nothing has to listen
// on an address this machine may not have.
func requestFor(t testing.TB, opts mongod.Options) dockerclient.ContainerConfig {
	t.Helper()
	m := &dockermock.Mock{
		ContainerCreateFunc: func(context.Context, string, dockerclient.ContainerConfig) (string, []string, error) {
			return containerID, nil, nil
		},
		ContainerInspectFunc: func(context.Context, string) (dockerclient.ContainerInspect, error) {
			return running(1), nil
		},
	}
	_, err := start(t, m, opts.WithStartTimeout(20*time.Millisecond))
	require.ErrorIs(t, err, mongod.ErrNotReady, "this daemon never reports a mongod")
	return created(t, m)
}

// flagValue returns the argument after a flag in a container command.
func flagValue(cmd []string, flag string) (string, bool) {
	for i, arg := range cmd {
		if arg == flag && i+1 < len(cmd) {
			return cmd[i+1], true
		}
	}
	return "", false
}

// parseURI parses a container's connection string.
func parseURI(t testing.TB, c *mongod.Container) *url.URL {
	t.Helper()
	parsed, err := url.Parse(c.URI())
	require.NoError(t, err, "the URI must be one a driver can parse")
	return parsed
}

// mustAtoi parses a number the daemon was given as a string.
func mustAtoi(t testing.TB, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	require.NoError(t, err, "%q must be a number", s)
	return n
}

// rootUser returns the root user a create request tells the image to
// create, or "" and "" if it asks for none. Images read one of two spellings
// of the variables, so both must be set and must agree.
func rootUser(t testing.TB, request dockerclient.ContainerConfig) (username, password string) {
	t.Helper()
	env := map[string]string{}
	for _, entry := range request.Env {
		key, value, _ := strings.Cut(entry, "=")
		env[key] = value
	}
	require.Equal(t, env["MONGO_INITDB_ROOT_USERNAME"], env["MONGODB_INITDB_ROOT_USERNAME"], "the official image and Atlas Local read different variables, so both carry the username")
	require.Equal(t, env["MONGO_INITDB_ROOT_PASSWORD"], env["MONGODB_INITDB_ROOT_PASSWORD"], "and both carry the password")
	return env["MONGO_INITDB_ROOT_USERNAME"], env["MONGO_INITDB_ROOT_PASSWORD"]
}
