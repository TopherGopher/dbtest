package dockerapi

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// On Windows the daemon's default is a named pipe, which this client cannot
// open, so it looks for an endpoint it can dial instead.
func TestANamedPipeFallsBackToAnEndpointThisClientCanDial(t *testing.T) {
	// Paths are built with filepath.Join so the separators match whichever
	// OS runs the test; on Windows itself these are backslashed.
	winTemp := filepath.Join("C:", "Users", "me", "AppData", "Local", "Temp")
	apiSock := filepath.Join(winTemp, "podman", "podman-machine-default-api.sock")
	probe := func(tcp bool, sockets ...string) windowsProbe {
		live := map[string]bool{}
		for _, s := range sockets {
			live[s] = true
		}
		return windowsProbe{
			getenv:    env(map[string]string{"TEMP": winTemp}),
			listening: func(addr string) bool { return tcp && addr == dockerDesktopTCP },
			dialable:  func(path string) bool { return live[path] },
			glob: func(pattern string) []string {
				if ok, _ := filepath.Match(pattern, apiSock); ok {
					return []string{apiSock}
				}
				return nil
			},
		}
	}

	t.Run("Docker Desktop's TCP endpoint comes first", func(t *testing.T) {
		got, err := windowsFallback(probe(true, apiSock))
		require.NoError(t, err, "the TCP endpoint answers")
		assert.Equal(t, "tcp://"+dockerDesktopTCP, got, "docker is preferred over podman")
	})
	t.Run("then a podman machine socket", func(t *testing.T) {
		got, err := windowsFallback(probe(false, apiSock))
		require.NoError(t, err, "the podman machine socket answers")
		assert.Equal(t, "unix://"+apiSock, got, "podman 5.3 and later expose a socket this client can dial")
	})
	t.Run("with neither, the caller is told what to set up", func(t *testing.T) {
		_, err := windowsFallback(probe(false))
		require.ErrorIs(t, err, ErrConnectionFailed, "having no endpoint to dial is a connection problem")
		assert.Same(t, ErrNoWindowsEndpoint, err, "the predeclared ErrNoWindowsEndpoint is returned so callers can compare it")
	})
}

// Only a named pipe sends discovery to the fallback; any other address is
// used as found.
func TestOnlyANamedPipeIsReplaced(t *testing.T) {
	saved := resolveHost
	t.Cleanup(func() { resolveHost = saved })

	resolveHost = func() (string, error) { return "tcp://build-host:2375", nil }
	got, err := resolveHostFromEnv()
	require.NoError(t, err, "a tcp address")
	assert.Equal(t, "tcp://build-host:2375", got, "a dialable address is kept")

	resolveHost = func() (string, error) { return "npipe:////./pipe/docker_engine", nil }
	_, err = resolveHostFromEnv()
	assert.ErrorIs(t, err, ErrConnectionFailed, "a named pipe is replaced by an endpoint this client can dial, or an error saying so")
}
