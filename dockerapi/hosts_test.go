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
	// The fields, not the wording: net/url words some of these differently
	// from one Go release to the next.
	bad := []string{"npipe:////./pipe/docker_engine", "ssh://user@host", "unix://", "http://:",
		"tcp://:0:", "tcp://a]0", "tcp://ho st", "", "just-a-hostname"}
	for _, in := range bad {
		_, err := parseHost(in)
		require.ErrorIs(t, err, ErrInvalidArgument, "%q is refused", in)
		var invalid *InvalidArgumentError
		require.ErrorAs(t, err, &invalid, "%q: with the details", in)
		assert.Equal(t, "docker host", invalid.Argument, "%q: names the docker host", in)
		assert.Equal(t, in, invalid.Value, "%q: quotes the value", in)
		assert.NotEmpty(t, invalid.Fix, "%q: says what to do", in)
	}
	_, err := parseHost("")
	assert.Same(t, ErrEmptyHost, err, "the predeclared ErrEmptyHost")
}

// env returns a getenv function backed by a map.
func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}
