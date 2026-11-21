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

// defaultTLSConfig returns the TLS settings for a tcp endpoint with no
// WithTLSConfig: those of the docker context the host was discovered
// through, else those from the environment, else TLS against the system's
// roots for an https:// address, else none.
func defaultTLSConfig(ep endpoint, discovered bool) (*tls.Config, error) {
	if discovered {
		dir, err := dockerclient.ContextTLSDir()
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

// contextTLSFix is what to do about a docker context's TLS file that
// cannot be used.
const contextTLSFix = "update the current context with `docker context update <name> --docker \"host=...,ca=...,cert=...,key=...\"` " +
	"(`docker context ls` shows which is current), or set DOCKER_CONTEXT=default to stop using it"

// tlsConfigFromContext loads the TLS files in a docker context's directory,
// or returns err from finding it. As with the docker CLI, ca.pem is
// optional.
func tlsConfigFromContext(dir string, err error) (*tls.Config, error) {
	if err != nil {
		return nil, err
	}
	return tlsConfigFromDir(dir, false, func(problem, _ string) error {
		return invalidArg("docker context TLS files", dir, problem, contextTLSFix)
	})
}

// tlsConfigFromEnv builds a tls.Config from DOCKER_TLS_VERIFY and
// DOCKER_CERT_PATH. It returns nil when DOCKER_TLS_VERIFY is not set. As
// with the docker CLI, ca.pem is required.
func tlsConfigFromEnv(getenv func(string) string) (*tls.Config, error) {
	if getenv("DOCKER_TLS_VERIFY") == "" {
		return nil, nil
	}
	dir := getenv("DOCKER_CERT_PATH")
	if dir == "" {
		dir = dockerclient.ConfigDir()
	}
	return tlsConfigFromDir(dir, true, func(problem, fix string) error {
		return invalidArg("DOCKER_CERT_PATH", dir, problem, fix)
	})
}

// tlsConfigFromDir builds a tls.Config from the ca.pem, cert.pem and key.pem
// in dir. Without ca.pem the system's roots are trusted, unless caRequired;
// without both cert.pem and key.pem no client certificate is sent. invalid
// builds the error for a file that cannot be used, given the problem and a
// fix for DOCKER_CERT_PATH.
func tlsConfigFromDir(dir string, caRequired bool, invalid func(problem, fix string) error) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	caPEM, err := os.ReadFile(filepath.Join(dir, "ca.pem"))
	switch {
	case errors.Is(err, fs.ErrNotExist) && !caRequired:
	case err != nil:
		return nil, invalid("ca.pem could not be read ("+err.Error()+")",
			"point DOCKER_CERT_PATH at the directory holding the daemon's ca.pem, and cert.pem and key.pem if it wants a client certificate, or unset DOCKER_TLS_VERIFY")
	default:
		cfg.RootCAs = x509.NewCertPool()
		if !cfg.RootCAs.AppendCertsFromPEM(caPEM) {
			return nil, invalid("no certificates were found in ca.pem", "replace ca.pem with the daemon's PEM-encoded CA certificate")
		}
	}
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if !bothExist(certPath, keyPath) {
		return cfg, nil
	}
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, invalid("cert.pem and key.pem do not form a valid key pair ("+err.Error()+")",
			"regenerate the client certificate, or remove cert.pem and key.pem to connect without one")
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
