package dockerapi

import (
	"net"
	"os"
	"path/filepath"
	"time"
)

// dockerDesktopTCP is the optional TCP endpoint Docker Desktop for Windows
// exposes when "Expose daemon on tcp://localhost:2375 without TLS" is on.
const dockerDesktopTCP = "localhost:2375"

// ErrNoWindowsEndpoint is returned by FromEnv on Windows when the daemon's
// address is Docker's named pipe, which this client cannot open, and
// neither substitute answers: Docker Desktop's optional TCP endpoint or a
// podman machine socket.
var ErrNoWindowsEndpoint = &ConnectionError{
	Host: "tcp://" + dockerDesktopTCP,
	Problem: "the daemon's address is Docker's Windows named pipe, which mongotest's default Docker client (dockerapi) cannot open; " +
		"nothing is listening on Docker Desktop's optional TCP endpoint, and no podman machine socket answered " +
		"in the Windows temporary directory",
	Fix: `Enable "Expose daemon on tcp://localhost:2375 without TLS" in Docker Desktop settings, ` +
		`or start a podman machine (Podman 5.3 or later creates a socket this client can use), ` +
		`or set DOCKER_HOST to a tcp:// endpoint, or use the mobyclient package, which can open the pipe`,
}

// windowsProbe carries what the named-pipe fallback looks at. Tests supply
// their own so the result does not depend on the machine running them.
type windowsProbe struct {
	getenv    func(string) string
	listening func(addr string) bool
	dialable  func(path string) bool
	glob      func(pattern string) []string
}

// realWindowsProbe looks at the machine this process runs on.
func realWindowsProbe() windowsProbe {
	return windowsProbe{getenv: os.Getenv, listening: tcpListening, dialable: socketDialable, glob: globPaths}
}

// windowsFallback finds an endpoint this client can dial in place of a
// named pipe: Docker Desktop's optional TCP endpoint, then the AF_UNIX
// socket a podman machine (Podman 5.3 or later) creates in the Windows
// temporary directory.
func windowsFallback(p windowsProbe) (string, error) {
	if p.listening(dockerDesktopTCP) {
		return "tcp://" + dockerDesktopTCP, nil
	}
	for _, path := range podmanMachineSockets(p) {
		if p.dialable(path) {
			return "unix://" + path, nil
		}
	}
	return "", ErrNoWindowsEndpoint
}

// podmanMachineSockets lists the sockets a podman machine may have put in
// the Windows temporary directory.
func podmanMachineSockets(p windowsProbe) []string {
	var dirs []string
	if tmp := p.getenv("TEMP"); tmp != "" {
		dirs = append(dirs, tmp)
	}
	if local := p.getenv("LOCALAPPDATA"); local != "" {
		dirs = append(dirs, filepath.Join(local, "Temp"))
	}
	var paths []string
	for _, dir := range dirs {
		paths = append(paths, p.glob(filepath.Join(dir, "podman", "*-api.sock"))...)
	}
	return paths
}

// tcpListening reports whether something accepts connections on addr.
func tcpListening(addr string) bool {
	return dials("tcp", addr, 500*time.Millisecond)
}

// socketDialable reports whether a daemon is listening on the unix socket
// at path. It connects rather than checking the file exists, because a
// stopped daemon can leave its socket file behind.
func socketDialable(path string) bool {
	return dials("unix", path, 100*time.Millisecond)
}

func dials(network, addr string, timeout time.Duration) bool {
	conn, err := net.DialTimeout(network, addr, timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// globPaths returns the paths matching pattern. A malformed pattern or an
// unreadable directory means no candidates.
func globPaths(pattern string) []string {
	matches, _ := filepath.Glob(pattern)
	return matches
}
