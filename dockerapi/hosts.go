package dockerapi

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"

	"github.com/tophergopher/mongotest/dockerclient"
)

// resolveHost finds the daemon; tests replace it.
var resolveHost = dockerclient.ResolveHost

// resolveHostFromEnv finds the daemon as dockerclient.ResolveHost does.
// This client cannot open a named pipe, so in place of Docker's default one
// it finds an endpoint it can dial, or returns ErrNoWindowsEndpoint. A pipe
// the caller chose is kept, so that parsing it says it cannot be used.
func resolveHostFromEnv() (string, error) {
	host, err := resolveHost()
	if err != nil || host != dockerDefaultPipe {
		return host, err
	}
	return windowsFallback(probeWindows())
}

// dockerDefaultPipe is the Docker daemon's default address on Windows.
const dockerDefaultPipe = "npipe:////./pipe/docker_engine"

// tlsConfigFromEnv builds a tls.Config from DOCKER_TLS_VERIFY and
// DOCKER_CERT_PATH. It returns nil when DOCKER_TLS_VERIFY is not set.
func tlsConfigFromEnv(getenv func(string) string) (*tls.Config, error) {
	if getenv("DOCKER_TLS_VERIFY") == "" {
		return nil, nil
	}
	dir := getenv("DOCKER_CERT_PATH")
	if dir == "" {
		dir = dockerclient.ConfigDir()
	}
	caPath := filepath.Join(dir, "ca.pem")
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		return nil, invalidArg("DOCKER_CERT_PATH", dir, "DOCKER_TLS_VERIFY is set but "+caPath+" could not be read ("+err.Error()+")",
			"point DOCKER_CERT_PATH at the directory holding the daemon's ca.pem (and cert.pem/key.pem when client certificates are required), or unset DOCKER_TLS_VERIFY")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, invalidArg("DOCKER_CERT_PATH", dir, "no certificates were found in "+caPath, "make sure ca.pem is a PEM-encoded certificate")
	}
	cfg := &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	_, certErr := os.Stat(certPath)
	_, keyErr := os.Stat(keyPath)
	if certErr == nil && keyErr == nil {
		pair, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return nil, invalidArg("DOCKER_CERT_PATH", dir, "cert.pem and key.pem do not form a valid key pair ("+err.Error()+")", "regenerate the client certificate, or remove the pair to connect without one")
		}
		cfg.Certificates = []tls.Certificate{pair}
	}
	return cfg, nil
}
