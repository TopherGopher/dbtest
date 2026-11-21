package dockerapi

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/tophergopher/dbtest/dockerclient"
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

// contextTLSDir finds the TLS files of the docker context discovery used;
// tests replace it.
var contextTLSDir = dockerclient.ContextTLSDir

// defaultTLSConfig returns the TLS settings for a tcp endpoint with no
// WithTLSConfig: those of the docker context the host was discovered
// through, else those from the environment, else TLS against the system's
// roots for an https:// address, else none.
func defaultTLSConfig(ep endpoint, discovered bool) (*tls.Config, error) {
	if discovered {
		dir, err := contextTLSDir()
		if err != nil || dir != "" {
			return tlsConfigFromContext(dir, err)
		}
	}
	cfg, err := tlsConfigFromEnv(os.Getenv)
	if cfg == nil && err == nil && ep.tls {
		cfg = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	return cfg, err
}

// tlsConfigFromContext loads the TLS files in a docker context's directory,
// or returns err from finding it.
func tlsConfigFromContext(dir string, err error) (*tls.Config, error) {
	if err != nil {
		return nil, err
	}
	return tlsConfigFromDir(dir, func(problem string) error {
		return invalidArg("docker context TLS files", dir, problem,
			"recreate the context with `docker context create`, giving it the daemon's ca.pem and, if it asks for one, a client cert.pem and key.pem")
	})
}

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
	// Unlike a docker context's, this ca.pem is required, as the docker CLI
	// requires it.
	caPath := filepath.Join(dir, "ca.pem")
	if _, err := os.Stat(caPath); err != nil {
		return nil, invalidArg("DOCKER_CERT_PATH", dir, "DOCKER_TLS_VERIFY is set but "+caPath+" could not be read ("+err.Error()+")",
			"point DOCKER_CERT_PATH at the directory holding the daemon's ca.pem (and cert.pem/key.pem when client certificates are required), or unset DOCKER_TLS_VERIFY")
	}
	return tlsConfigFromDir(dir, func(problem string) error {
		return invalidArg("DOCKER_CERT_PATH", dir, problem, "fix or replace the files, or unset DOCKER_TLS_VERIFY")
	})
}

// tlsConfigFromDir builds a tls.Config from the ca.pem, cert.pem and key.pem
// in dir. Without ca.pem the system's roots are trusted; without both
// cert.pem and key.pem no client certificate is sent. invalid builds the
// error for a file that cannot be used.
func tlsConfigFromDir(dir string, invalid func(problem string) error) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	caPath := filepath.Join(dir, "ca.pem")
	caPEM, err := os.ReadFile(caPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, invalid(caPath + " could not be read (" + err.Error() + ")")
	default:
		cfg.RootCAs = x509.NewCertPool()
		if !cfg.RootCAs.AppendCertsFromPEM(caPEM) {
			return nil, invalid("no certificates were found in " + caPath + "; it must be a PEM-encoded certificate")
		}
	}
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if !bothExist(certPath, keyPath) {
		return cfg, nil
	}
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, invalid("cert.pem and key.pem do not form a valid key pair (" + err.Error() + ")")
	}
	cfg.Certificates = []tls.Certificate{pair}
	return cfg, nil
}

// bothExist reports whether both files exist.
func bothExist(a, b string) bool {
	_, errA := os.Stat(a)
	_, errB := os.Stat(b)
	return errA == nil && errB == nil
}
