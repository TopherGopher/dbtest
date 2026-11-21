package hostaddr

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"slices"
	"strings"

	"github.com/tophergopher/dbtest/dockerclient"
)

// ErrNoSharedNetwork: this process is not in a container, on the daemon in
// use, whose network a new container could join. A start then publishes a
// port instead, so the error is only ever logged.
var ErrNoSharedNetwork = errors.New("no Docker network is shared with this process's container")

// defaultBridge is the network every container started without --network
// joins.
const defaultBridge = "bridge"

// SharedNetwork is a network this process's container is on. A container
// created on it is reached at its address there, with no published port.
type SharedNetwork struct {
	Name string
	// UserDefined is false for the default bridge, where every container
	// started without --network can connect.
	UserDefined bool
}

// FindSharedNetwork returns the network to create a container on so that
// this process reaches it directly. Its error matches ErrNoSharedNetwork and
// says why there is none.
func FindSharedNetwork(ctx context.Context, docker dockerclient.Client) (SharedNetwork, error) {
	return networkFinder{openMountinfo: openMountinfoOS, localAddrs: net.InterfaceAddrs}.find(ctx, docker)
}

// networkFinder holds the lookups FindSharedNetwork makes on this machine,
// so a test can describe any machine.
type networkFinder struct {
	openMountinfo func() (io.ReadCloser, error)
	localAddrs    func() ([]net.Addr, error)
}

func openMountinfoOS() (io.ReadCloser, error) { return os.Open("/proc/self/mountinfo") }

// find names this process's container from mountinfo, asks the daemon about
// it, and keeps only the networks where its address is one of this
// process's own. The address check catches a different daemon, such as
// Docker-in-Docker, that happens to hold a container with the same id.
func (f networkFinder) find(ctx context.Context, docker dockerclient.Client) (SharedNetwork, error) {
	id, err := f.ownContainerID()
	if err != nil {
		return SharedNetwork{}, err
	}
	info, err := docker.ContainerInspect(ctx, id)
	if err != nil {
		return SharedNetwork{}, fmt.Errorf("%w: the daemon at %s cannot inspect this process's container %s: %w",
			ErrNoSharedNetwork, docker.Host(), id, err)
	}
	local, err := f.localIPs()
	if err != nil {
		return SharedNetwork{}, err
	}
	return chooseNetwork(info.NetworkSettings.Networks, local)
}

// ownContainerID reads this process's container id from mountinfo.
func (f networkFinder) ownContainerID() (string, error) {
	mountinfo, err := f.openMountinfo()
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrNoSharedNetwork, err)
	}
	defer mountinfo.Close()
	id, found := containerIDFromMountinfo(mountinfo)
	if !found {
		return "", fmt.Errorf("%w: this process is not in a Docker container: no mount of a container's hostname file", ErrNoSharedNetwork)
	}
	return id, nil
}

// localIPs returns the addresses of this process's network interfaces.
func (f networkFinder) localIPs() ([]net.IP, error) {
	addrs, err := f.localAddrs()
	if err != nil {
		return nil, fmt.Errorf("%w: cannot list this process's addresses: %w", ErrNoSharedNetwork, err)
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok {
			ips = append(ips, ipNet.IP)
		}
	}
	return ips, nil
}

// chooseNetwork picks, among the networks where the container's address is
// local, the user-defined one whose name sorts first, or else the default
// bridge. Host networking leaves no address, so it never qualifies.
func chooseNetwork(networks map[string]dockerclient.NetworkEndpoint, local []net.IP) (SharedNetwork, error) {
	var names []string
	for name, endpoint := range networks {
		if isLocal(endpoint.IPAddress, local) {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return SharedNetwork{}, fmt.Errorf("%w: none of this container's network addresses is assigned to this process", ErrNoSharedNetwork)
	}
	slices.Sort(names)
	for _, name := range names {
		if name != defaultBridge {
			return SharedNetwork{Name: name, UserDefined: true}, nil
		}
	}
	return SharedNetwork{Name: defaultBridge}, nil
}

// isLocal reports whether address is one of local.
func isLocal(address string, local []net.IP) bool {
	ip := net.ParseIP(address)
	if ip == nil {
		return false
	}
	return slices.ContainsFunc(local, ip.Equal)
}

// containerIDFromMountinfo finds the container id in the line where Docker
// bind-mounts the container's hostname file:
//
//	114 94 254:0 /var/lib/docker/containers/<id>/hostname /etc/hostname rw ...
//
// The hostname itself is not used: --hostname changes it, and it can equal
// another container's name.
func containerIDFromMountinfo(mountinfo io.Reader) (string, bool) {
	lines := bufio.NewScanner(mountinfo)
	for lines.Scan() {
		// The fourth field is the mounted path, the fifth where it is mounted.
		fields := strings.Fields(lines.Text())
		if len(fields) < 5 || fields[4] != "/etc/hostname" {
			continue
		}
		if id, ok := idFromHostnamePath(fields[3]); ok {
			return id, true
		}
	}
	return "", false
}

// idFromHostnamePath returns <id> from a path ending in
// /containers/<id>/hostname.
func idFromHostnamePath(path string) (string, bool) {
	rest, found := strings.CutSuffix(path, "/hostname")
	if !found {
		return "", false
	}
	i := strings.LastIndex(rest, "/containers/")
	if i < 0 {
		return "", false
	}
	id := rest[i+len("/containers/"):]
	return id, isContainerID(id)
}

// isContainerID reports whether s can name a container in a request.
func isContainerID(s string) bool {
	_, err := dockerclient.CheckID("container", s)
	return err == nil
}
