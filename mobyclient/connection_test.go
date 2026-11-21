package mobyclient_test

import (
	"crypto/x509"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
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
// upload, must not be reported as an unreachable daemon, even when its
// message mentions a permission.
func TestAFailedRequestIsNotAConnectionFailure(t *testing.T) {
	for name, broken := range map[string]error{
		"a broken upload":                 errors.New("archive/tar: write too long"),
		"a failure that names permission": errors.New("reading the build context: permission denied"),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := failingMobyClient(t, broken).ImageInspect(t.Context(), "mongo:8")

			assert.NotErrorIs(t, err, dockerclient.ErrConnectionFailed, "the failure was not in reaching the daemon")
			assert.NotErrorIs(t, err, fs.ErrPermission, "only a denied socket is a permission problem")
			assert.ErrorIs(t, err, dockerclient.ErrDaemonResponse, "the caller still gets a sentinel to branch on")
			assert.ErrorIs(t, err, broken, "the cause stays in the chain")
		})
	}
}

// failingClients returns each client with every request failing in the
// transport with err, and the API version pinned so that no version request
// fails first.
func failingClients(t *testing.T, err error) map[string]dockerclient.Client {
	t.Helper()
	stdlib, sErr := dockerapi.New(
		dockerapi.WithHost("unix:///var/run/docker.sock"),
		dockerapi.WithAPIVersion("1.44"),
		dockerapi.WithHTTPClient(&http.Client{Transport: failingTransport{err}}),
	)
	require.NoError(t, sErr, "a dockerapi client whose requests fail")
	return map[string]dockerclient.Client{"dockerapi": stdlib, "mobyclient": failingMobyClient(t, err)}
}

// A connection the daemon closes partway through a request, as it does when
// it rejects an upload early, was reached: it is not a daemon to start.
func TestAConnectionThatBreaksDuringARequestIsNotAConnectionFailure(t *testing.T) {
	for name, cause := range map[string]syscall.Errno{"broken pipe": syscall.EPIPE, "connection reset": syscall.ECONNRESET} {
		broken := &net.OpError{Op: "write", Net: "unix", Err: os.NewSyscallError("write", cause)}
		for client, c := range failingClients(t, broken) {
			t.Run(name+"/"+client, func(t *testing.T) {
				_, err := c.ImageInspect(t.Context(), "mongo:8")

				assert.NotErrorIs(t, err, dockerclient.ErrConnectionFailed, "the daemon was reached before the connection broke")
				assert.ErrorIs(t, err, dockerclient.ErrDaemonResponse, "the request reached the daemon and could not be completed")
				assert.ErrorIs(t, err, cause, "the cause stays in the chain")
				var resp *dockerclient.ResponseError
				require.ErrorAs(t, err, &resp, "the failure is a ResponseError")
				assert.NotEmpty(t, resp.Fix, "it carries its own advice, not the hint about API versions")
			})
		}
	}
}

// A dial that fails is still a daemon that was never reached.
func TestAFailedDialIsStillAConnectionFailure(t *testing.T) {
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
	for client, c := range failingClients(t, refused) {
		t.Run(client, func(t *testing.T) {
			_, err := c.ImageInspect(t.Context(), "mongo:8")

			assert.ErrorIs(t, err, dockerclient.ErrConnectionFailed, "a refused dial never reached the daemon")
		})
	}
}

// tlsAlertServer is a TLS port that is not served by Go's HTTP server: it
// answers anything with a TLS alert record and hangs up. It returns its
// address in DOCKER_HOST form.
func tlsAlertServer(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "listening for the client")
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			// A fatal protocol_version alert, as a TLS server sends to a
			// client that does not speak TLS.
			_, _ = conn.Write([]byte{0x15, 0x03, 0x01, 0x00, 0x02, 0x02, 0x46})
			_ = conn.Close()
		}
	}()
	return "tcp://" + l.Addr().String()
}

// Plain HTTP sent to a daemon that only accepts TLS never reaches its API,
// so it is a connection failure, whichever way the TLS port answers.
func TestPlainHTTPToATLSDaemonIsAConnectionFailure(t *testing.T) {
	cases := map[string]func(t *testing.T) string{
		"Go's HTTP server answers with 400": tlsServer,
		"the port answers with a TLS alert": tlsAlertServer,
	}
	for name, host := range cases {
		for client, build := range clientsFromEnv {
			t.Run(name+"/"+client, func(t *testing.T) {
				clearDockerEnv(t)
				t.Setenv("DOCKER_HOST", host(t))
				c, err := build()
				require.NoError(t, err, "a client with no TLS settings")

				_, err = c.ImageInspect(t.Context(), "mongo:8")

				assert.ErrorIs(t, err, dockerclient.ErrConnectionFailed, "the daemon only speaks TLS, so no request reached it")
				assert.NotErrorIs(t, err, dockerclient.ErrDaemonResponse, "no daemon response was misread")
			})
		}
	}
}

// refusingProxy is a socket proxy that refuses every request with status 403
// and body. It returns its address in DOCKER_HOST form.
func refusingProxy(t *testing.T, contentType, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return "tcp://" + srv.Listener.Addr().String()
}

// A socket proxy that refuses a request is reported as a proxy's refusal of
// that request, not as the daemon refusing credentials. A 403 the daemon
// sends itself, as JSON, is still the daemon's.
func TestARefusalByAProxyIsToldFromTheDaemons(t *testing.T) {
	cases := []struct {
		name, contentType, body string
		fromProxy               bool
	}{
		{"an HTML page", "text/html", "<html><body><h1>403 Forbidden</h1>\nRequest forbidden by administrative rules.\n</body></html>\n", true},
		{"an empty body", "", "", true},
		{"the daemon's own JSON", "application/json", `{"message":"authorization denied by plugin authz"}`, false},
	}
	for _, tc := range cases {
		for client, build := range clientsFromEnv {
			t.Run(tc.name+"/"+client, func(t *testing.T) {
				clearDockerEnv(t)
				t.Setenv("DOCKER_HOST", refusingProxy(t, tc.contentType, tc.body))
				t.Setenv("DOCKER_API_VERSION", "1.44")
				c, err := build()
				require.NoError(t, err, "a client for the proxy")

				_, _, err = c.ContainerCreate(t.Context(), "mongo", dockerclient.ContainerConfig{Image: "mongo:8"})

				assert.ErrorIs(t, err, dockerclient.ErrUnauthorized, "a refusal still matches ErrUnauthorized")
				var status *dockerclient.StatusError
				require.ErrorAs(t, err, &status, "a refusal is a StatusError")
				assert.Equal(t, tc.fromProxy, status.FromProxy, "the error says whether a proxy or the daemon refused")
				assert.Equal(t, http.MethodPost, status.Method, "the error names the refused method")
				assert.Equal(t, "/containers/create", status.Path, "the error names the refused path")
			})
		}
	}
}
