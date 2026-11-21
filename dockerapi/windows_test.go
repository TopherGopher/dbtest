package dockerapi

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tophergopher/dbtest/dockerclient"
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
	t.Run("a podman machine socket under LOCALAPPDATA is found too", func(t *testing.T) {
		local := filepath.Join("C:", "Users", "me", "AppData", "Local")
		sock := filepath.Join(local, "Temp", "podman", "podman-machine-default-api.sock")
		p := windowsProbe{
			getenv:    env(map[string]string{"LOCALAPPDATA": local}),
			listening: func(string) bool { return false },
			dialable:  func(path string) bool { return path == sock },
			glob: func(pattern string) []string {
				if ok, _ := filepath.Match(pattern, sock); ok {
					return []string{sock}
				}
				return nil
			},
		}
		got, err := windowsFallback(p)
		require.NoError(t, err, "the socket under LOCALAPPDATA answers")
		assert.Equal(t, "unix://"+sock, got, "LOCALAPPDATA's Temp is searched when TEMP is unset")
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
	saved := resolveDaemon
	t.Cleanup(func() { resolveDaemon = saved })

	resolveDaemon = func() (dockerclient.Daemon, error) { return dockerclient.Daemon{Host: "tcp://build-host:2375"}, nil }
	got, err := discover()
	require.NoError(t, err, "a tcp address")
	assert.Equal(t, "tcp://build-host:2375", got.Host, "a dialable address is kept")

	savedProbe := probeWindows
	t.Cleanup(func() { probeWindows = savedProbe })
	probeWindows = func() windowsProbe {
		return windowsProbe{getenv: env(nil), listening: func(string) bool { return false },
			dialable: func(string) bool { return false }, glob: func(string) []string { return nil }}
	}
	resolveDaemon = func() (dockerclient.Daemon, error) {
		return dockerclient.Daemon{Host: "npipe:////./pipe/docker_engine"}, nil
	}
	_, err = discover()
	assert.ErrorIs(t, err, ErrConnectionFailed, "a named pipe is replaced by an endpoint this client can dial, or an error saying so")
}

// A pipe the caller chose, such as Podman's, is not swapped for whatever
// answers on Docker Desktop's port: that could be another daemon. It is
// left for parsing to reject, with advice.
func TestAChosenPipeIsNotReplaced(t *testing.T) {
	saved := resolveDaemon
	t.Cleanup(func() { resolveDaemon = saved })
	resolveDaemon = func() (dockerclient.Daemon, error) {
		return dockerclient.Daemon{Host: "npipe:////./pipe/podman-machine-default"}, nil
	}

	got, err := discover()

	require.NoError(t, err, "resolving gives the pipe back")
	assert.Equal(t, "npipe:////./pipe/podman-machine-default", got.Host, "a chosen pipe is kept, so the caller learns this client cannot open it")
}
