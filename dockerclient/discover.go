package dockerclient

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

// dockerConfigFile is the part of ~/.docker/config.json we read.
type dockerConfigFile struct {
	CurrentContext string `json:"currentContext"`
}

// contextMeta is the part of a docker context's meta.json we read.
type contextMeta struct {
	Endpoints map[string]contextEndpoint `json:"Endpoints"`
}

// contextEndpoint is one endpoint inside a docker context.
type contextEndpoint struct {
	Host string `json:"Host"`
	// SkipTLSVerify tells the docker CLI to use TLS without checking the
	// daemon's certificate.
	SkipTLSVerify bool `json:"SkipTLSVerify"`
}

// lookup carries the environment and the probes discovery needs. Tests
// supply their own so resolution never depends on the machine running them.
type lookup struct {
	goos   string
	getenv func(string) string
	// dialable reports whether a daemon is listening on a unix socket path.
	dialable func(path string) bool
	// glob expands a socket path pattern, for the podman machine paths that
	// carry the machine name.
	glob func(pattern string) []string
	// uid is the effective user id, used to guess the runtime directory
	// when XDG_RUNTIME_DIR is not set.
	uid int
}

// realLookup probes the machine this process is running on.
func realLookup() lookup {
	return lookup{goos: runtime.GOOS, getenv: os.Getenv, dialable: socketDialable, glob: globPaths, uid: os.Getuid()}
}

// ResolveHost returns the address of the Docker daemon, in DOCKER_HOST
// form, such as unix:///var/run/docker.sock. Like the docker CLI, it uses
// DOCKER_HOST if set. Otherwise it uses the docker context named by
// DOCKER_CONTEXT, or by currentContext in the Docker config file, unless
// that context is "default". It returns an InvalidArgumentError when the
// config file or that context cannot be read.
//
// When neither names a daemon, it returns the first of these with a daemon
// answering:
// rootless Docker's socket under XDG_RUNTIME_DIR, /var/run/docker.sock,
// rootless Podman's socket, /run/podman/podman.sock, and on macOS a podman
// machine's socket. When none answers it returns /var/run/docker.sock, so
// the failure comes when a client connects, as a connection error naming
// that socket. On Windows it skips the search and returns Docker's named
// pipe, as the docker CLI does. A client that uses this address should also
// use the TLS files ContextTLSDir finds.
func ResolveHost() (string, error) {
	return resolveHostWith(realLookup(), configDir(os.Getenv))
}

// resolveHost is resolveHostWith against the real machine, for callers that
// only need to vary the environment.
func resolveHost(getenv func(string) string, configDir string) (string, error) {
	l := realLookup()
	l.getenv = getenv
	return resolveHostWith(l, configDir)
}

// ConfigDir returns the Docker config directory: DOCKER_CONFIG, else the
// .docker directory in the user's home directory. It holds config.json and
// the docker contexts. When DOCKER_TLS_VERIFY is set without
// DOCKER_CERT_PATH, the TLS files are read from it too.
func ConfigDir() string { return configDir(os.Getenv) }

// configDir is ConfigDir with the environment passed in, for tests.
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

// resolveHostWith applies the discovery order: DOCKER_HOST, then
// DOCKER_CONTEXT or currentContext from config.json (resolved through the
// contexts store), then the platform default.
//
// The socket probe is part of that last step only. Configuration is the user
// telling us where the daemon is, so a socket that happens to exist locally
// must never overrule it.
func resolveHostWith(l lookup, configDir string) (string, error) {
	if h := l.getenv("DOCKER_HOST"); h != "" {
		return h, nil
	}
	name, err := chosenContext(l.getenv, configDir)
	if err != nil {
		return "", err
	}
	if name != "" {
		return hostFromContext(configDir, name)
	}
	return defaultHostForLookup(l)
}

// chosenContext names the docker context discovery takes the daemon from:
// DOCKER_CONTEXT, else currentContext in config.json. It returns "" when
// DOCKER_HOST is set or the context is "default", since then no context is
// used.
func chosenContext(getenv func(string) string, configDir string) (string, error) {
	if getenv("DOCKER_HOST") != "" {
		return "", nil
	}
	name := getenv("DOCKER_CONTEXT")
	if name == "" {
		var err error
		if name, err = currentContext(configDir); err != nil {
			return "", err
		}
	}
	if name == "default" {
		return "", nil
	}
	return name, nil
}

// ContextTLSDir returns the directory of the docker context that ResolveHost
// uses, which holds its TLS files: ca.pem, and cert.pem and key.pem if the
// daemon wants a client certificate. It returns "" when ResolveHost uses no
// context, or the context has no TLS files. Use these files in place of
// DOCKER_TLS_VERIFY and DOCKER_CERT_PATH, as the docker CLI does. The
// context's SkipTLSVerify setting is not reported, because dbtest's clients
// always verify. It returns an InvalidArgumentError when the configuration
// cannot be read, or when a tcp:// context sets SkipTLSVerify, has no TLS
// files and DOCKER_TLS_VERIFY is unset: the docker CLI would reach it over
// TLS without verifying it.
func ContextTLSDir() (string, error) {
	return contextTLSDir(os.Getenv, configDir(os.Getenv))
}

// contextTLSDir is ContextTLSDir with the environment passed in.
func contextTLSDir(getenv func(string) string, configDir string) (string, error) {
	name, err := chosenContext(getenv, configDir)
	if err != nil || name == "" {
		return "", err
	}
	dir := filepath.Join(configDir, "contexts", "tls", contextID(name), "docker")
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return "", refuseSkipVerify(getenv, configDir, name)
	}
	// Stat on ca.pem, not on the directory, because only it needs the
	// directory to be searchable. Going on without the files would send a
	// remote daemon's requests in the clear, or trust the wrong roots, so a
	// directory that cannot be read is an error.
	if _, err := os.Stat(filepath.Join(dir, "ca.pem")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", InvalidArgument("docker context", name, "its TLS files in "+dir+" could not be read ("+err.Error()+")",
			"fix the directory's permissions, or update the context with `docker context update "+name+"`")
	}
	return dir, nil
}

// refuseSkipVerify returns an error when a context with no TLS files skips
// TLS verification for a plain tcp:// address, and DOCKER_TLS_VERIFY does
// not turn TLS on. The docker CLI reaches such a context over TLS without
// checking the daemon's certificate. dbtest never skips the check, and must
// not send the requests in the clear instead.
func refuseSkipVerify(getenv func(string) string, configDir, name string) error {
	if getenv("DOCKER_TLS_VERIFY") != "" {
		return nil
	}
	ep, err := contextDockerEndpoint(configDir, name)
	if err != nil || !skipsVerifyInTheClear(ep) {
		return err
	}
	return InvalidArgument("docker context", name, "it skips TLS verification, which dbtest never does",
		"give the context the daemon's CA with `docker context update "+name+" --docker host=<address>,ca=<path>`, or set DOCKER_HOST")
}

// skipsVerifyInTheClear reports whether an endpoint skips TLS verification
// at an address dbtest would otherwise reach without TLS.
func skipsVerifyInTheClear(ep contextEndpoint) bool {
	return ep.SkipTLSVerify && plainTCP(ep.Host)
}

// contextID is the name of a context's directories in the contexts store:
// the hex SHA-256 of its name.
func contextID(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:])
}

// defaultUnixSocket is where a Docker daemon listens by default.
const defaultUnixSocket = "unix:///var/run/docker.sock"

// windowsPipe is the Docker daemon's default address on Windows.
const windowsPipe = "npipe:////./pipe/docker_engine"

// defaultHostForLookup returns the platform default when nothing is
// configured: Docker's named pipe on Windows, and elsewhere the first
// well-known socket with a daemon answering, so that a machine running only
// Podman works with no configuration at all.
func defaultHostForLookup(l lookup) (string, error) {
	if l.goos == "windows" {
		return windowsPipe, nil
	}
	if host, ok := firstListeningSocket(l); ok {
		return host, nil
	}
	return defaultUnixSocket, nil
}

// firstListeningSocket returns the first candidate socket with a daemon
// answering on it.
func firstListeningSocket(l lookup) (string, bool) {
	if l.dialable == nil {
		return "", false
	}
	for _, path := range socketCandidates(l) {
		if l.dialable(path) {
			return "unix://" + path, true
		}
	}
	return "", false
}

// currentContext reads currentContext from <configDir>/config.json. A missing
// file means no context is selected.
func currentContext(configDir string) (string, error) {
	path := filepath.Join(configDir, "config.json")
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", InvalidArgument("docker config file", path, "it could not be read ("+err.Error()+")", "fix the file permissions, or set DOCKER_HOST to bypass context lookup")
	}
	var cfg dockerConfigFile
	if err := json.Unmarshal(b, &cfg); err != nil {
		return "", InvalidArgument("docker config file", path, "it is not valid JSON ("+err.Error()+")", "repair the file, or set DOCKER_HOST to bypass context lookup")
	}
	return cfg.CurrentContext, nil
}

// hostFromContext resolves a named docker context to its docker endpoint's
// address.
func hostFromContext(configDir, name string) (string, error) {
	ep, err := contextDockerEndpoint(configDir, name)
	return ep.Host, err
}

// contextDockerEndpoint reads a named docker context's docker endpoint from
// <configDir>/contexts/meta/<sha256(name)>/meta.json.
func contextDockerEndpoint(configDir, name string) (contextEndpoint, error) {
	path := filepath.Join(configDir, "contexts", "meta", contextID(name), "meta.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return contextEndpoint{}, InvalidArgument("docker context", name, "its metadata file "+path+" could not be read ("+err.Error()+")",
			"run `docker context ls` to see the contexts that exist, `docker context use <name>` to switch, or set DOCKER_HOST directly")
	}
	var meta contextMeta
	if err := json.Unmarshal(b, &meta); err != nil {
		return contextEndpoint{}, InvalidArgument("docker context", name, "its metadata file "+path+" is not valid JSON ("+err.Error()+")", "fix the file, or remove the context with `docker context rm "+name+"` and create it again, or set DOCKER_HOST directly")
	}
	ep, ok := meta.Endpoints["docker"]
	if !ok || ep.Host == "" {
		return contextEndpoint{}, InvalidArgument("docker context", name, "it has no docker endpoint in "+path, "give it one with `docker context update "+name+" --docker host=...`, or set DOCKER_HOST directly")
	}
	return ep, nil
}
