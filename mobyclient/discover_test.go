package mobyclient_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tophergopher/mongotest/dockerapi"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockermock"
	"github.com/tophergopher/mongotest/mobyclient"
)

// clearDockerEnv empties every variable that steers discovery and points
// DOCKER_CONFIG at an empty directory, which it returns, so that only what
// a test sets is seen.
func clearDockerEnv(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_API_VERSION", "DOCKER_CERT_PATH", "DOCKER_TLS_VERIFY"} {
		t.Setenv(name, "")
	}
	dir := t.TempDir()
	t.Setenv("DOCKER_CONFIG", dir)
	return dir
}

// writeContext saves a docker context named name, reaching host, in the
// config directory, as `docker context create` does.
func writeContext(t *testing.T, configDir, name, host string) {
	t.Helper()
	sum := sha256.Sum256([]byte(name))
	dir := filepath.Join(configDir, "contexts", "meta", hex.EncodeToString(sum[:]))
	require.NoError(t, os.MkdirAll(dir, 0o755), "creating the context's directory")
	meta := `{"Name":"` + name + `","Endpoints":{"docker":{"Host":"` + host + `"}}}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o644), "writing the context")
}

// Whichever client a caller picks, the same environment reaches the same
// daemon.
func TestEveryClientFindsTheSameDaemon(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, configDir string)
		want  string // "" where the answer depends on this machine's sockets
	}{
		{"DOCKER_HOST", func(t *testing.T, _ string) {
			t.Setenv("DOCKER_HOST", "tcp://build-host:2375")
		}, "tcp://build-host:2375"},
		{"DOCKER_CONTEXT", func(t *testing.T, dir string) {
			writeContext(t, dir, "remote", "tcp://10.1.2.3:2375")
			t.Setenv("DOCKER_CONTEXT", "remote")
		}, "tcp://10.1.2.3:2375"},
		{"currentContext in config.json", func(t *testing.T, dir string) {
			writeContext(t, dir, "colima", "unix:///home/me/.colima/default/docker.sock")
			require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"currentContext":"colima"}`), 0o644), "writing config.json")
		}, "unix:///home/me/.colima/default/docker.sock"},
		{"nothing set", func(*testing.T, string) {}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup(t, clearDockerEnv(t))

			stdlib, err := dockerapi.FromEnv()
			require.NoError(t, err, "dockerapi finds a daemon address")
			fromEnv, err := mobyclient.FromEnv()
			require.NoError(t, err, "mobyclient.FromEnv finds a daemon address")
			bare, err := mobyclient.New()
			require.NoError(t, err, "mobyclient.New finds a daemon address")

			if tc.want != "" {
				assert.Equal(t, tc.want, stdlib.Host(), "dockerapi reaches the configured daemon")
			}
			assert.Equal(t, stdlib.Host(), fromEnv.Host(), "mobyclient.FromEnv reaches the daemon dockerapi does")
			assert.Equal(t, stdlib.Host(), bare.Host(), "so does mobyclient.New with no options")
		})
	}
}

// Discovery that cannot read the Docker configuration fails, rather than
// quietly reaching some other daemon.
func TestUnreadableDockerConfigurationIsAnInvalidArgument(t *testing.T) {
	configDir := clearDockerEnv(t)
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{not json`), 0o644), "writing a broken config.json")

	_, err := mobyclient.New()

	assert.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "a broken config.json is a configuration problem to fix")
}

// A host the caller gives is used as it is: discovery does not run, so
// neither DOCKER_HOST nor a broken configuration gets in the way.
func TestAGivenHostIsUsedInsteadOfDiscovery(t *testing.T) {
	configDir := clearDockerEnv(t)
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{not json`), 0o644), "writing a broken config.json")
	t.Setenv("DOCKER_CONTEXT", "ghost")
	t.Setenv("DOCKER_HOST", "tcp://from-env:2375")

	given, err := mobyclient.New(mobyclient.WithHost("tcp://given:2375"))
	require.NoError(t, err, "a client with WithHost")
	assert.Equal(t, "tcp://given:2375", given.Host(), "WithHost wins over DOCKER_HOST")
}

// requestedPaths returns the paths, as sent, of the requests a daemon
// double received.
func requestedPaths(d *dockermock.Daemon) []string {
	var paths []string
	for _, r := range d.Requests() {
		paths = append(paths, r.RawPath)
	}
	return paths
}

func TestDockerAPIVersionPinsTheVersion(t *testing.T) {
	for name, build := range map[string]func() (*mobyclient.Client, error){
		"FromEnv": mobyclient.FromEnv,
		"New":     func() (*mobyclient.Client, error) { return mobyclient.New() },
	} {
		t.Run(name, func(t *testing.T) {
			clearDockerEnv(t)
			d := dockermock.NewDaemon()
			t.Cleanup(d.Close)
			t.Setenv("DOCKER_HOST", d.Host())
			t.Setenv("DOCKER_API_VERSION", "1.41")

			c, err := build()
			require.NoError(t, err, "a client for the daemon double")
			_, _ = c.ImageInspect(t.Context(), "mongo:8")

			require.NotEmpty(t, d.Requests(), "the client sent a request")
			for _, path := range requestedPaths(d) {
				assert.True(t, strings.HasPrefix(path, "/v1.41/"), "every request uses the pinned version, got %s", path)
			}
		})
	}
}

func TestDockerCertPathWithoutItsFilesIsAnInvalidArgument(t *testing.T) {
	for name, build := range map[string]func() (*mobyclient.Client, error){
		"FromEnv": mobyclient.FromEnv,
		"New":     func() (*mobyclient.Client, error) { return mobyclient.New() },
	} {
		t.Run(name, func(t *testing.T) {
			clearDockerEnv(t)
			t.Setenv("DOCKER_HOST", "tcp://build-host:2376")
			t.Setenv("DOCKER_CERT_PATH", t.TempDir()) // holds no ca.pem

			_, err := build()

			assert.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "a DOCKER_CERT_PATH without its files is a configuration problem")
		})
	}
}

// writeClientCerts writes a ca.pem, cert.pem and key.pem that have nothing
// to do with the server's certificate, so the server is trusted only when
// verification is off.
func writeClientCerts(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err, "generating a key")
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "unrelated"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err, "creating a certificate")
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err, "encoding the key")
	dir := t.TempDir()
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	for file, data := range map[string][]byte{
		"ca.pem":   certPEM,
		"cert.pem": certPEM,
		"key.pem":  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, file), data, 0o600), "writing %s", file)
	}
	return dir
}

func TestDockerTLSVerifyChecksTheServer(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	host := "tcp://" + strings.TrimPrefix(srv.URL, "https://")

	inspect := func(t *testing.T, verify string) error {
		clearDockerEnv(t)
		t.Setenv("DOCKER_HOST", host)
		t.Setenv("DOCKER_CERT_PATH", writeClientCerts(t))
		t.Setenv("DOCKER_TLS_VERIFY", verify)
		c, err := mobyclient.New()
		require.NoError(t, err, "a client with TLS from the environment")
		_, err = c.ImageInspect(t.Context(), "mongo:8")
		return err
	}

	var unknownCA x509.UnknownAuthorityError
	assert.ErrorAs(t, inspect(t, "1"), &unknownCA, "with DOCKER_TLS_VERIFY set, a server the CA did not sign is refused")
	assert.NoError(t, inspect(t, ""), "without it, the connection is encrypted but the server is not checked")
}

// tlsServer is a daemon double that speaks only TLS, with a certificate no
// test CA signed. It returns the server's address in DOCKER_HOST form.
func tlsServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	return "tcp://" + strings.TrimPrefix(srv.URL, "https://")
}

// As with the docker CLI, DOCKER_TLS_VERIFY without DOCKER_CERT_PATH takes
// the certificates from the Docker config directory. Sending plain HTTP
// instead would hand the requests, credentials included, to whoever answers.
func TestDockerTLSVerifyAloneUsesTheConfigDirectory(t *testing.T) {
	configDir := clearDockerEnv(t)
	certs := writeClientCerts(t)
	for _, file := range []string{"ca.pem", "cert.pem", "key.pem"} {
		data, err := os.ReadFile(filepath.Join(certs, file))
		require.NoError(t, err, "reading %s", file)
		require.NoError(t, os.WriteFile(filepath.Join(configDir, file), data, 0o600), "copying %s", file)
	}
	t.Setenv("DOCKER_HOST", tlsServer(t))
	t.Setenv("DOCKER_TLS_VERIFY", "1")

	c, err := mobyclient.New()
	require.NoError(t, err, "a client with TLS from the config directory")
	_, err = c.ImageInspect(t.Context(), "mongo:8")

	var unknownCA x509.UnknownAuthorityError
	assert.ErrorAs(t, err, &unknownCA, "the server is checked against the CA in the config directory")
}

// TLS from the environment applies to a tcp:// host given with WithHost
// too, as it does for dockerapi.
func TestEnvironmentTLSAppliesToAGivenHost(t *testing.T) {
	clearDockerEnv(t)
	host := tlsServer(t)
	t.Setenv("DOCKER_CERT_PATH", writeClientCerts(t))
	t.Setenv("DOCKER_TLS_VERIFY", "1")

	c, err := mobyclient.New(mobyclient.WithHost(host))
	require.NoError(t, err, "a client for a given host")
	_, err = c.ImageInspect(t.Context(), "mongo:8")

	var unknownCA x509.UnknownAuthorityError
	assert.ErrorAs(t, err, &unknownCA, "the given host is reached over verified TLS")
}

// TLS settings in the environment are for a tcp:// daemon. A local socket
// found by discovery is reached without them.
func TestEnvironmentTLSIsNotUsedOnASocket(t *testing.T) {
	clearDockerEnv(t)
	d := dockermock.NewDaemon()
	t.Cleanup(d.Close)
	if !strings.HasPrefix(d.Host(), "unix://") {
		t.Skip("the daemon double listens on TCP on this platform")
	}
	t.Setenv("DOCKER_HOST", d.Host())
	t.Setenv("DOCKER_CERT_PATH", writeClientCerts(t))
	t.Setenv("DOCKER_TLS_VERIFY", "1")

	c, err := mobyclient.New()
	require.NoError(t, err, "a client for a socket")
	_, _ = c.ImageInspect(t.Context(), "mongo:8")

	assert.NotEmpty(t, d.Requests(), "the request reaches the socket")
}
