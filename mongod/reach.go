package mongod

import (
	"context"

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
	// network is the shared network, or "" to publish a port.
	network string
	// publishHost is, on a shared network, the address a published port
	// would be dialled at, or "" if it could not be worked out. The advice
	// for a start that times out suggests it.
	publishHost string
}

// shared reports whether mongod is reached on a shared network.
func (r reach) shared() bool { return r.network != "" }

// exposed reports whether more than this process can connect: a port
// published on every interface, or any shared network. A user-defined
// network may be a macvlan or overlay that other machines reach.
func (r reach) exposed() bool {
	return r.shared() || hostaddr.BindIP(r.host) == hostaddr.AllInterfaces
}

// where says, for a log line, who can connect.
func (r reach) where() string {
	if r.shared() {
		return "it is on the Docker network " + r.network + ", where other containers can reach it"
	}
	return "its port is open on every network interface of the Docker host, because it is reached from another machine or container"
}

// reach works out how to connect, before anything is created: on a network
// shared with this process's container if there is one, or else through a
// published port.
func (c config) reach(ctx context.Context, docker dockerclient.Client) (reach, error) {
	resolve := c.resolveHost
	if resolve == nil {
		resolve = hostaddr.Resolve
	}
	// An address that cannot be worked out is needed only to publish a port,
	// so a container on an internal network, with no default route, can
	// still share it.
	address, err := resolve(docker.Host(), c.hostIP)
	if network, ok := c.sharedNetwork(ctx, docker, address); ok {
		return reach{
			network:     network,
			source:      "the network " + network + ", which this process's container is on",
			publishHost: address.Host, // "" if err is set
		}, nil
	}
	if err != nil {
		return reach{}, err
	}
	return reach{host: address.Host, source: address.Source}, nil
}

// sharedNetwork returns the network to create the container on, unless the
// caller chose something that needs a published port or there is none. In
// both cases the start publishes a port instead, and only logs why.
func (c config) sharedNetwork(ctx context.Context, docker dockerclient.Client, address hostaddr.Address) (string, bool) {
	if option := c.publishingOption(address); option != "" {
		c.logger.Debug("Publishing mongod's port, because of an option.",
			"container", c.name, "option", option)
		return "", false
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
		c.logger.Debug("Publishing mongod's port, because no network shared with this process's container was found.",
			"container", c.name, "reason", err)
		return "", false
	}
	return network, true
}

// publishingOption names the option that needs a published port, or is "".
// WithHostIP, DBTEST_HOST_IP and WithPort each choose an address or a host
// port. WithTLS needs the address before the container has one, because the
// certificate names it.
func (c config) publishingOption(address hostaddr.Address) string {
	switch {
	case address.Chosen:
		return address.Source
	case c.port != 0:
		return "WithPort"
	case c.tls:
		return "WithTLS"
	}
	return ""
}

// portOnNetwork is the port mongod is dialled at on a shared network.
func (c config) portOnNetwork() int {
	if c.networkPort != 0 {
		return c.networkPort
	}
	return 27017
}
