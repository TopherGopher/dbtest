package mongod

import (
	"context"
	"os"

	"github.com/tophergopher/dbtest/dockerclient"
	"github.com/tophergopher/dbtest/internal/hostaddr"
)

// reach is how this process will connect to mongod: through a port
// published on the Docker host, or on a network this process's container
// is on, where nothing needs publishing.
type reach struct {
	// host is the address to dial. On a shared network it is empty until
	// the container starts and has an address there.
	host string
	// source says what decided it, for the error a slow start returns.
	source string
	// network is the shared network, or the zero value to publish a port.
	network hostaddr.SharedNetwork
}

// shared reports whether mongod is reached on a shared network.
func (r reach) shared() bool { return r.network.Name != "" }

// exposed reports whether more than this process can connect: a port
// published on every interface, or the default bridge every container
// started without --network joins.
func (r reach) exposed() bool {
	if r.shared() {
		return !r.network.UserDefined
	}
	return hostaddr.BindIP(r.host) == hostaddr.AllInterfaces
}

// where says, for a log line, who can connect.
func (r reach) where() string {
	if r.shared() {
		return "it is on Docker's default bridge network, which every container started without --network joins"
	}
	return "its port is open on every network interface of the Docker host, because it is reached from another machine or container"
}

// reach works out how to connect, before anything is created: on a network
// shared with this process's container if there is one, or else through a
// published port.
func (c config) reach(ctx context.Context, docker dockerclient.Client) (reach, error) {
	if network, ok := c.sharedNetwork(ctx, docker); ok {
		return reach{network: network, source: "the container's address on " + network.Name + ", a network this process's container is on"}, nil
	}
	address, err := hostaddr.Resolve(docker.Host(), c.hostIP)
	if err != nil {
		return reach{}, err
	}
	return reach{host: address.Host, source: address.Source}, nil
}

// sharedNetwork returns the network to create the container on, unless the
// caller chose something that needs a published port or there is none.
// Either way the start carries on, so the reason is only logged.
func (c config) sharedNetwork(ctx context.Context, docker dockerclient.Client) (hostaddr.SharedNetwork, bool) {
	if !c.mayShareNetwork() {
		return hostaddr.SharedNetwork{}, false
	}
	find := c.findNetwork
	if find == nil {
		find = hostaddr.FindSharedNetwork
	}
	// The start timeout has not begun, so the lookup gets its own.
	ctx, cancel := context.WithTimeout(ctx, c.startTimeout)
	defer cancel()
	network, err := find(ctx, docker)
	if err != nil {
		c.logger.Debug("Publishing mongod's port, because it cannot be reached on a network shared with this process.",
			"container", c.name, "reason", err)
		return hostaddr.SharedNetwork{}, false
	}
	return network, true
}

// mayShareNetwork reports whether the caller left the address to the start.
// WithHostIP, DBTEST_HOST_IP and WithPort each choose an address or a host
// port. WithTLS needs the address before the container has one, because the
// certificate names it.
func (c config) mayShareNetwork() bool {
	switch {
	case c.hostIP != "", os.Getenv(EnvHostIP) != "":
		return false
	case c.port != 0, c.tls:
		return false
	}
	return true
}
