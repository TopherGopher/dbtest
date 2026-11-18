package dockerclient

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
// form, such as unix:///var/run/docker.sock. Like the docker CLI it uses
// DOCKER_HOST, then the docker context, other than "default", named by
// DOCKER_CONTEXT or by currentContext in the Docker config file. It returns
// an InvalidArgumentError when the config file or that context cannot be
// read.
//
// With neither, it returns the first of these with a daemon answering:
// rootless Docker's socket under XDG_RUNTIME_DIR, /var/run/docker.sock,
// rootless Podman's socket, /run/podman/podman.sock, and on macOS a podman
// machine's socket. When none answers it returns /var/run/docker.sock, so
// the failure comes when a client connects, as a connection error naming
// that socket. On Windows it returns Docker's named pipe instead, as the
// docker CLI does.
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
	name := l.getenv("DOCKER_CONTEXT")
	if name == "" {
		var err error
		name, err = currentContext(configDir)
		if err != nil {
			return "", err
		}
	}
	if name != "" && name != "default" {
		return hostFromContext(configDir, name)
	}
	return defaultHostForLookup(l)
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

// hostFromContext resolves a named docker context to its docker endpoint by
// reading <configDir>/contexts/meta/<sha256(name)>/meta.json.
func hostFromContext(configDir, name string) (string, error) {
	sum := sha256.Sum256([]byte(name))
	path := filepath.Join(configDir, "contexts", "meta", hex.EncodeToString(sum[:]), "meta.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return "", InvalidArgument("docker context", name, "its metadata file "+path+" could not be read ("+err.Error()+")",
			"run `docker context ls` to see the contexts that exist, `docker context use <name>` to switch, or set DOCKER_HOST directly")
	}
	var meta contextMeta
	if err := json.Unmarshal(b, &meta); err != nil {
		return "", InvalidArgument("docker context", name, "its metadata file "+path+" is not valid JSON ("+err.Error()+")", "recreate the context with `docker context create`, or set DOCKER_HOST directly")
	}
	ep, ok := meta.Endpoints["docker"]
	if !ok || ep.Host == "" {
		return "", InvalidArgument("docker context", name, "it has no docker endpoint in "+path, "recreate the context with `docker context create --docker host=...`, or set DOCKER_HOST directly")
	}
	return ep.Host, nil
}
