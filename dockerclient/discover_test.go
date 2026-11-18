package dockerclient

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// env returns a getenv function backed by a map.
func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func writeContext(t *testing.T, configDir, name, host string) {
	t.Helper()
	sum := sha256.Sum256([]byte(name))
	dir := filepath.Join(configDir, "contexts", "meta", hex.EncodeToString(sum[:]))
	require.NoError(t, os.MkdirAll(dir, 0o755), "creating the context meta directory fixture")
	meta := `{"Name":"` + name + `","Metadata":{},"Endpoints":{"docker":{"Host":"` + host + `","SkipTLSVerify":false}}}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o644), "writing meta.json fixture")
}

func TestResolveHostPrecedence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("default host is a named pipe on windows")
	}
	cfg := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(cfg, "config.json"), []byte(`{"currentContext":"colima"}`), 0o644), "writing config.json fixture")
	writeContext(t, cfg, "colima", "unix:///home/me/.colima/default/docker.sock")
	writeContext(t, cfg, "remote", "tcp://10.1.2.3:2376")

	t.Run("DOCKER_HOST wins over everything", func(t *testing.T) {
		got, err := resolveHost(env(map[string]string{"DOCKER_HOST": "tcp://1.2.3.4:2375", "DOCKER_CONTEXT": "remote"}), cfg)
		require.NoError(t, err, "resolution with DOCKER_HOST set")
		assert.Equal(t, "tcp://1.2.3.4:2375", got, "DOCKER_HOST must take precedence over DOCKER_CONTEXT and config.json")
	})
	t.Run("DOCKER_CONTEXT wins over currentContext", func(t *testing.T) {
		got, err := resolveHost(env(map[string]string{"DOCKER_CONTEXT": "remote"}), cfg)
		require.NoError(t, err, "resolution with DOCKER_CONTEXT set")
		assert.Equal(t, "tcp://10.1.2.3:2376", got, "DOCKER_CONTEXT must override currentContext from config.json")
	})
	t.Run("currentContext from config.json", func(t *testing.T) {
		got, err := resolveHost(env(nil), cfg)
		require.NoError(t, err, "resolution through config.json")
		assert.Equal(t, "unix:///home/me/.colima/default/docker.sock", got, "currentContext must resolve through contexts/meta")
	})
	t.Run("context named default means the default socket", func(t *testing.T) {
		got, err := resolveHost(env(map[string]string{"DOCKER_CONTEXT": "default"}), cfg)
		require.NoError(t, err, "resolution with the default context")
		assert.Equal(t, "unix:///var/run/docker.sock", got, "the 'default' context has no meta file and means the default socket")
	})
	t.Run("missing context metadata is a descriptive error", func(t *testing.T) {
		_, err := resolveHost(env(map[string]string{"DOCKER_CONTEXT": "ghost"}), cfg)
		require.ErrorIs(t, err, ErrInvalidArgument, "an unknown context is a configuration problem")
		assert.Contains(t, err.Error(), "ghost", "the error must name the context")
		assert.Contains(t, err.Error(), "meta.json", "the error must name the file that was missing")
		assert.Contains(t, err.Error(), "docker context ls", "the error must tell the user how to list contexts")
	})
	t.Run("nothing set uses the default socket", func(t *testing.T) {
		// Probes are injected so the result does not depend on what happens
		// to be listening on the machine running the tests.
		l := lookup{goos: "linux", getenv: env(nil), dialable: noSockets()}
		got, err := resolveHostWith(l, t.TempDir())
		require.NoError(t, err, "resolution with nothing configured")
		assert.Equal(t, "unix:///var/run/docker.sock", got, "the platform default applies when nothing is configured")
	})
	t.Run("unreadable config.json is a descriptive error", func(t *testing.T) {
		bad := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(bad, "config.json"), []byte(`{not json`), 0o644), "writing a broken config.json")
		_, err := resolveHost(env(nil), bad)
		require.ErrorIs(t, err, ErrInvalidArgument, "a broken config.json is a configuration problem")
		assert.Contains(t, err.Error(), "config.json", "the error must name the file")
		assert.Contains(t, err.Error(), "DOCKER_HOST", "the error must offer DOCKER_HOST as the bypass")
	})
}

func TestConfigDir(t *testing.T) {
	assert.Equal(t, "/etc/dockercfg", configDir(env(map[string]string{"DOCKER_CONFIG": "/etc/dockercfg"})), "DOCKER_CONFIG must be honoured")
	assert.Equal(t, filepath.Join("/home/me", ".docker"), configDir(env(map[string]string{"HOME": "/home/me"})), "without DOCKER_CONFIG the directory is $HOME/.docker")
}

func TestDefaultHostWindows(t *testing.T) {
	listening := func(addr string) bool { return addr == "localhost:2375" }
	got, err := defaultHostFor("windows", env(nil), nil, listening)
	require.NoError(t, err, "Docker Desktop's TCP endpoint answering must be enough on Windows")
	assert.Equal(t, "tcp://localhost:2375", got, "the probed Docker Desktop endpoint must be used")

	got, err = defaultHostFor("windows", env(nil), nil, func(string) bool { return false })
	require.NoError(t, err, "nothing listening on Windows leaves the choice to the client")
	assert.Empty(t, got, "so no address is returned")

	got, err = defaultHostFor("linux", env(nil), noSockets(), func(string) bool { t.Error("the TCP probe must not run on linux"); return false })
	require.NoError(t, err, "linux default")
	assert.Equal(t, "unix:///var/run/docker.sock", got, "linux falls back to the docker socket when nothing is listening")
	got, err = defaultHostFor("darwin", env(nil), noSockets(), nil)
	require.NoError(t, err, "darwin default")
	assert.Equal(t, "unix:///var/run/docker.sock", got, "darwin falls back to the docker socket when nothing is listening")
}

// defaultHostFor is defaultHostForLookup for tests that only vary the
// platform and the probes.
func defaultHostFor(goos string, getenv func(string) string, dialable func(path string) bool, listening func(addr string) bool) (string, error) {
	return defaultHostForLookup(lookup{goos: goos, getenv: getenv, dialable: dialable, listening: listening})
}
