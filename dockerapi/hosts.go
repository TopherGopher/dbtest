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

// resolveHostFromEnv finds the daemon the way the docker CLI does. On
// Windows, with nothing configured or answering, it explains how to give
// this client an endpoint it can dial, since it cannot open a named pipe.
func resolveHostFromEnv() (string, error) {
	host, err := resolveHost()
	if err == nil && host == "" {
		return "", ErrNoWindowsEndpoint
	}
	return host, err
}

// dockerDesktopTCP is the optional TCP endpoint Docker Desktop for Windows
// exposes when "Expose daemon on tcp://localhost:2375 without TLS" is on.
const dockerDesktopTCP = "localhost:2375"

// ErrNoWindowsEndpoint is returned on Windows when neither DOCKER_HOST nor a
// docker context is set and Docker Desktop's optional TCP endpoint is not
// listening.
var ErrNoWindowsEndpoint = &ConnectionError{
	Host: "tcp://" + dockerDesktopTCP,
	Problem: "no DOCKER_HOST is set, nothing is listening on Docker Desktop's optional TCP endpoint, " +
		"and no podman machine socket was found under TEMP; Windows named pipes (npipe://) are not supported by this client",
	Fix: `Either enable "Expose daemon on tcp://localhost:2375 without TLS" in Docker Desktop settings, ` +
		`or start a podman machine (podman 5.3 or newer exposes a socket this client can use), ` +
		`or set the DOCKER_HOST environment variable to a tcp:// endpoint (for example DOCKER_HOST=tcp://localhost:2375)`,
}

// configDir returns the Docker config directory, where the TLS files are
// looked for when DOCKER_CERT_PATH is not set: DOCKER_CONFIG, else
// $HOME/.docker.
func configDir(getenv func(string) string) string {
	if d := getenv("DOCKER_CONFIG"); d != "" {
		return d
	}
	home := getenv("HOME")
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = h
		}
	}
	return filepath.Join(home, ".docker")
}

// tlsConfigFromEnv builds a tls.Config from DOCKER_TLS_VERIFY and
// DOCKER_CERT_PATH. It returns nil when DOCKER_TLS_VERIFY is not set.
func tlsConfigFromEnv(getenv func(string) string) (*tls.Config, error) {
	if getenv("DOCKER_TLS_VERIFY") == "" {
		return nil, nil
	}
	dir := getenv("DOCKER_CERT_PATH")
	if dir == "" {
		dir = configDir(getenv)
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
