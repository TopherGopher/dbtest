package dockerclient

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"time"
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
	// listening reports whether a daemon is answering on a TCP address.
	listening func(addr string) bool
	// glob expands a socket path pattern, for the podman machine paths that
	// carry the machine name.
	glob func(pattern string) []string
	// uid is the effective user id, used to guess the runtime directory
	// when XDG_RUNTIME_DIR is not set.
	uid int
}

// realLookup probes the machine this process is running on.
func realLookup() lookup {
	return lookup{goos: runtime.GOOS, getenv: os.Getenv, dialable: socketDialable, listening: tcpListening, glob: globPaths, uid: os.Getuid()}
}

// ResolveHost finds the daemon the way the docker CLI does: DOCKER_HOST,
// then the docker context named by DOCKER_CONTEXT or by currentContext in
// the Docker config file, then the first of the usual Docker and Podman
// sockets with a daemon answering on it. When none answers it returns the
// Docker socket all the same, so the failure comes when a client connects,
// with an error that names the address.
//
// On Windows the default is a named pipe, which not every client can open,
// so there it returns "" when nothing is configured or answering: the client
// uses its own default, or says it has none.
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

// configDir returns the Docker config directory: DOCKER_CONFIG, else
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

// dockerDesktopTCP is the optional TCP endpoint Docker Desktop for Windows
// exposes when "Expose daemon on tcp://localhost:2375 without TLS" is on.
const dockerDesktopTCP = "localhost:2375"

// defaultHostForLookup returns the platform default when nothing is
// configured.
//
// On Windows the daemon's default endpoint is a named pipe, so it probes
// Docker Desktop's optional TCP endpoint and Podman's socket first, and
// returns "" when neither answers.
//
// Everywhere else it looks for a daemon on the well-known socket paths, so
// that a machine running only Podman works with no configuration at all.
// When none of them answers it returns the Docker default anyway rather than
// an error, so the failure surfaces at dial time with a message that names
// the socket and says what to do about it.
func defaultHostForLookup(l lookup) (string, error) {
	if l.goos == "windows" {
		// Docker first: Docker Desktop's optional TCP endpoint.
		if l.listening != nil && l.listening(dockerDesktopTCP) {
			return "tcp://" + dockerDesktopTCP, nil
		}
		// Then Podman, which since 5.3 puts a real AF_UNIX socket on the
		// Windows filesystem alongside its named pipe. The pipe cannot be
		// dialled here; the socket can.
		if host, ok := firstListeningSocket(l); ok {
			return host, nil
		}
		return "", nil
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

// tcpListening reports whether something accepts connections on addr.
func tcpListening(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
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
