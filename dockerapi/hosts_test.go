package dockerapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseHost(t *testing.T) {
	good := []struct {
		in     string
		scheme string
		addr   string
		tls    bool
	}{
		{in: "unix:///var/run/docker.sock", scheme: "unix", addr: "/var/run/docker.sock"},
		{in: "unix:///tmp/x.sock", scheme: "unix", addr: "/tmp/x.sock"},
		{in: "tcp://127.0.0.1:2375", scheme: "tcp", addr: "127.0.0.1:2375"},
		{in: "tcp://docker.example.com", scheme: "tcp", addr: "docker.example.com:2375"},
		{in: "http://10.0.0.5:2375", scheme: "tcp", addr: "10.0.0.5:2375"},
		{in: "https://10.0.0.5", scheme: "tcp", addr: "10.0.0.5:2376", tls: true},
		{in: "tcp://host:", scheme: "tcp", addr: "host:2375"},
		{in: "tcp://:2375", scheme: "tcp", addr: "localhost:2375"},
		{in: "tcp://[::1]:2375", scheme: "tcp", addr: "[::1]:2375"},
		{in: "tcp://[::1]", scheme: "tcp", addr: "[::1]:2375"},
	}
	for _, tc := range good {
		ep, err := parseHost(tc.in)
		require.NoError(t, err, "%q is a valid docker host", tc.in)
		assert.Equal(t, tc.scheme, ep.scheme, "%q: dial network", tc.in)
		assert.Equal(t, tc.addr, ep.addr, "%q: dial address (default ports filled in)", tc.in)
		assert.Equal(t, tc.tls, ep.tls, "%q: only https:// implies TLS", tc.in)
	}
	bad := []struct{ in, wantSub string }{
		{"npipe:////./pipe/docker_engine", "Docker Desktop"},
		{"ssh://user@host", "not supported"},
		{"unix://", "socket path"},
		{"http://:", "no address"},
		{"tcp://:0:", "not host:port"},
		{"tcp://a]0", "not host:port"},
		{"tcp://ho st", "docker host"},
		{"", "empty"},
		{"just-a-hostname", "no scheme"},
	}
	for _, tc := range bad {
		_, err := parseHost(tc.in)
		require.ErrorIs(t, err, ErrInvalidArgument, "%q must be rejected as an invalid argument", tc.in)
		assert.Contains(t, err.Error(), tc.wantSub, "%q: the error must explain the problem", tc.in)
	}
	_, err := parseHost("")
	assert.Same(t, ErrEmptyHost, err, "an empty host returns the predeclared ErrEmptyHost so callers can compare it")
}

// env returns a getenv function backed by a map.
func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// On Windows, with nothing configured or answering, the shared resolver
// leaves the choice to the client. This client cannot open the named pipe
// that is the default there, so it says how to give it an endpoint.
func TestNothingFoundOnWindowsIsErrNoWindowsEndpoint(t *testing.T) {
	saved := resolveHost
	t.Cleanup(func() { resolveHost = saved })
	resolveHost = func() (string, error) { return "", nil }

	_, err := FromEnv()

	require.ErrorIs(t, err, ErrConnectionFailed, "having no endpoint to dial is a connection problem")
	assert.Same(t, ErrNoWindowsEndpoint, err, "the predeclared ErrNoWindowsEndpoint is returned so callers can compare it")
}
