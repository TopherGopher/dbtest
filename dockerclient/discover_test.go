package dockerclient

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
		l := lookup{goos: "linux", getenv: env(map[string]string{"DOCKER_CONTEXT": "default"}), dialable: noSockets()}
		got, err := resolveHostWith(l, cfg)
		require.NoError(t, err, "resolution with the default context")
		assert.Equal(t, "unix:///var/run/docker.sock", got, "the 'default' context has no meta file and means the default socket")
	})
	t.Run("missing context metadata is a descriptive error", func(t *testing.T) {
		_, err := resolveHost(env(map[string]string{"DOCKER_CONTEXT": "ghost"}), cfg)
		require.ErrorIs(t, err, ErrInvalidArgument, "an unknown context is a configuration problem")
		var invalid *InvalidArgumentError
		require.ErrorAs(t, err, &invalid, "the error says what was refused")
		assert.Equal(t, "ghost", invalid.Value, "the error must name the context")
		assert.Contains(t, err.Error(), "meta.json", "the error must name the file that was missing")
		assert.NotEmpty(t, invalid.Fix, "the error must tell the user what to do")
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
		var invalid *InvalidArgumentError
		require.ErrorAs(t, err, &invalid, "the error says what was refused")
		assert.NotEmpty(t, invalid.Fix, "the error must say what to do")
	})
}

func TestConfigDir(t *testing.T) {
	assert.Equal(t, "/etc/dockercfg", configDir(env(map[string]string{"DOCKER_CONFIG": "/etc/dockercfg"})), "DOCKER_CONFIG must be honoured")
	assert.Equal(t, filepath.Join("/home/me", ".docker"), configDir(env(map[string]string{"HOME": "/home/me"})), "without DOCKER_CONFIG the directory is $HOME/.docker")
}

func TestDefaultHost(t *testing.T) {
	got, err := defaultHostFor("windows", env(nil), noSockets())
	require.NoError(t, err, "windows default")
	assert.Equal(t, "npipe:////./pipe/docker_engine", got, "on windows the default is Docker's named pipe, as for the docker CLI")
	got, err = defaultHostFor("linux", env(nil), noSockets())
	require.NoError(t, err, "linux default")
	assert.Equal(t, "unix:///var/run/docker.sock", got, "linux falls back to the docker socket when nothing is listening")
	got, err = defaultHostFor("darwin", env(nil), noSockets())
	require.NoError(t, err, "darwin default")
	assert.Equal(t, "unix:///var/run/docker.sock", got, "darwin falls back to the docker socket when nothing is listening")
}

// defaultHostFor is defaultHostForLookup for tests that only vary the
// platform and the probes.
func defaultHostFor(goos string, getenv func(string) string, dialable func(path string) bool) (string, error) {
	return defaultHostForLookup(lookup{goos: goos, getenv: getenv, dialable: dialable})
}

// A context's meta.json is whatever is on disk, so reading its docker
// endpoint must never panic, and must either fail as an invalid argument or
// give an address.
func FuzzContextDockerEndpoint(f *testing.F) {
	for _, seed := range []string{
		`{"Name":"remote","Endpoints":{"docker":{"Host":"tcp://build-host:2376","SkipTLSVerify":true}}}`,
		`{"Endpoints":{"docker":{"Host":""}}}`,
		`{"Endpoints":{"docker":{"SkipTLSVerify":"yes"}}}`,
		`{"Endpoints":null}`,
		`[]`, ``, `{`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, meta string) {
		configDir := t.TempDir()
		dir := filepath.Join(configDir, "contexts", "meta", contextID("remote"))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o644); err != nil {
			t.Fatal(err)
		}
		ep, err := contextDockerEndpoint(configDir, "remote")
		if err != nil && !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("meta.json %q failed with %v, which is not an invalid argument", meta, err)
		}
		if err == nil && ep.Host == "" {
			t.Fatalf("meta.json %q gave no address and no error", meta)
		}
	})
}
