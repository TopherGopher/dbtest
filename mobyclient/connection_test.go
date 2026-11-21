package mobyclient_test

import (
	"crypto/x509"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"

	mobydocker "github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tophergopher/dbtest/dockerapi"
	"github.com/tophergopher/dbtest/dockerclient"
	"github.com/tophergopher/dbtest/mobyclient"
)

// clientsFromEnv builds each client from the environment, so that the same
// failure can be checked through both.
var clientsFromEnv = map[string]func() (dockerclient.Client, error){
	"dockerapi": func() (dockerclient.Client, error) { return dockerapi.FromEnv() },
	"mobyclient": func() (dockerclient.Client, error) {
		return mobyclient.New()
	},
}

// closedPort returns a TCP address in DOCKER_HOST form where nothing
// listens.
func closedPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "reserving a port")
	addr := l.Addr().String()
	require.NoError(t, l.Close(), "closing the port so nothing listens on it")
	return "tcp://" + addr
}

// staleSocket returns the DOCKER_HOST address of a Unix socket file with
// nothing listening on it, as a stopped daemon can leave behind.
func staleSocket(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Docker on Windows listens on a named pipe, not a Unix socket")
	}
	// Not t.TempDir(), whose path includes the test name and can pass
	// macOS's 104-byte limit on a socket path.
	dir, err := os.MkdirTemp("", "dbtest")
	require.NoError(t, err, "making a directory for the socket")
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	l, err := net.Listen("unix", sock)
	require.NoError(t, err, "creating the socket")
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	require.NoError(t, l.Close(), "closing the socket and leaving its file behind")
	return "unix://" + sock
}

// A daemon that cannot be reached is reported as ErrConnectionFailed by
// either client, never as a daemon that answered badly.
func TestAnUnreachableDaemonIsAConnectionFailure(t *testing.T) {
	cases := map[string]func(t *testing.T) string{
		"a TCP port where nothing listens":  closedPort,
		"a socket with no daemon behind it": staleSocket,
	}
	for name, host := range cases {
		for client, build := range clientsFromEnv {
			t.Run(name+"/"+client, func(t *testing.T) {
				clearDockerEnv(t)
				t.Setenv("DOCKER_HOST", host(t))
				c, err := build()
				require.NoError(t, err, "a client for the address")

				_, err = c.ImageInspect(t.Context(), "mongo:8")

				assert.ErrorIs(t, err, dockerclient.ErrConnectionFailed, "the daemon was never reached")
				assert.NotErrorIs(t, err, dockerclient.ErrDaemonResponse, "nothing answered, so no response was misread")
			})
		}
	}
}

// A daemon whose certificate the configured CA did not sign is a failure to
// connect, and the certificate problem stays in the chain.
func TestAnUntrustedDaemonCertificateIsAConnectionFailure(t *testing.T) {
	for client, build := range clientsFromEnv {
		t.Run(client, func(t *testing.T) {
			clearDockerEnv(t)
			t.Setenv("DOCKER_HOST", tlsServer(t))
			t.Setenv("DOCKER_CERT_PATH", writeClientCerts(t))
			t.Setenv("DOCKER_TLS_VERIFY", "1")
			c, err := build()
			require.NoError(t, err, "a client with TLS from the environment")

			_, err = c.ImageInspect(t.Context(), "mongo:8")

			assert.ErrorIs(t, err, dockerclient.ErrConnectionFailed, "no request was sent to an untrusted daemon")
			assert.NotErrorIs(t, err, dockerclient.ErrDaemonResponse, "no response was misread")
			var unknownCA x509.UnknownAuthorityError
			assert.ErrorAs(t, err, &unknownCA, "the certificate problem is kept as the cause")
		})
	}
}

// failingTransport fails every request with err, without dialling.
type failingTransport struct{ err error }

func (f failingTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, f.err }

// failingMobyClient returns a mobyclient.Client whose every request fails
// in the transport with err, so the moby client decorates err exactly as it
// would a real one.
func failingMobyClient(t *testing.T, err error) *mobyclient.Client {
	t.Helper()
	moby, mErr := mobydocker.New(
		mobydocker.WithHost("unix:///var/run/docker.sock"),
		mobydocker.WithHTTPClient(&http.Client{Transport: failingTransport{err}}),
	)
	require.NoError(t, mErr, "a moby client whose requests fail")
	c, mErr := mobyclient.New(mobyclient.WithMobyClient(moby))
	require.NoError(t, mErr, "wrapping the moby client")
	return c
}

// The moby client drops the cause of a denied socket and of a dial that
// timed out. Both are still connection failures, and a denied socket is
// still a permission problem, as it is through dockerapi.
func TestADeniedOrTimedOutDialIsAConnectionFailure(t *testing.T) {
	denied := &net.OpError{Op: "dial", Net: "unix", Err: os.NewSyscallError("connect", syscall.EACCES)}
	timedOut := &net.OpError{Op: "dial", Net: "unix", Err: os.ErrDeadlineExceeded}

	_, err := failingMobyClient(t, denied).ImageInspect(t.Context(), "mongo:8")
	assert.ErrorIs(t, err, dockerclient.ErrConnectionFailed, "a denied socket was never used")
	assert.ErrorIs(t, err, fs.ErrPermission, "a denied socket is reported as a permission problem")

	_, err = failingMobyClient(t, timedOut).ImageInspect(t.Context(), "mongo:8")
	assert.ErrorIs(t, err, dockerclient.ErrConnectionFailed, "a dial that timed out never reached the daemon")
	assert.NotErrorIs(t, err, fs.ErrPermission, "a timeout is not a permission problem")
}

// The moby client calls any failure to complete a request a connection
// failure. One that happens after the daemon was reached, such as a broken
// upload, must not be reported as an unreachable daemon.
func TestAFailedRequestIsNotAConnectionFailure(t *testing.T) {
	broken := errors.New("archive/tar: write too long")

	_, err := failingMobyClient(t, broken).ImageInspect(t.Context(), "mongo:8")

	assert.NotErrorIs(t, err, dockerclient.ErrConnectionFailed, "the failure was not in reaching the daemon")
	assert.ErrorIs(t, err, dockerclient.ErrDaemonResponse, "the caller still gets a sentinel to branch on")
	assert.ErrorIs(t, err, broken, "the cause stays in the chain")
}
