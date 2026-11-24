package hostaddr

import (
	"context"
	"strconv"
	"time"

	"github.com/tophergopher/dbtest/dockerclient"
)

// Reach is how this process will connect to a container: through a port
// published on the Docker host, or on a network this process's container
// is on, where nothing needs publishing.
type Reach struct {
	// Host is the address to dial. On a shared network it is empty until
	// the container starts and has an address there.
	Host string
	// Source says what decided it, for the error a slow start returns.
	Source string
	// Network is the shared network, or "" to publish a port.
	Network string
	// PublishHost is, on a shared network, the address a published port
	// would be dialled at, or "" if it could not be worked out. The advice
	// for a start that times out suggests it.
	PublishHost string
}

// Shared reports whether the container is reached on a shared network.
func (r Reach) Shared() bool { return r.Network != "" }

// Exposed reports whether more than this process can connect: a port
// published on every interface, or any shared network. A user-defined
// network may be a macvlan or overlay that other machines reach.
func (r Reach) Exposed() bool {
	return r.Shared() || BindIP(r.Host) == AllInterfaces
}

// Where says, for a log line, who can connect.
func (r Reach) Where() string {
	if r.Shared() {
		return "it is on the Docker network " + r.Network + ", where other containers can reach it"
	}
	return "its port is open on every network interface of the Docker host, because it is reached from another machine or container"
}

// HostConfig returns how the container is created for this reach: on the
// shared network, or with containerPort published on the interface the
// address to dial needs. A hostPort of 0 lets the daemon choose.
func (r Reach) HostConfig(containerPort string, hostPort int) *dockerclient.HostConfig {
	if r.Shared() {
		return &dockerclient.HostConfig{NetworkMode: r.Network}
	}
	publishOn := "" // empty asks the daemon to choose the port
	if hostPort != 0 {
		publishOn = strconv.Itoa(hostPort)
	}
	return &dockerclient.HostConfig{PortBindings: map[string][]dockerclient.PortBinding{
		containerPort: {{HostIP: BindIP(r.Host), HostPort: publishOn}},
	}}
}

// ReachInput is what an image package knows when it decides how its
// container is reached.
type ReachInput struct {
	// Name is the container's, for log lines.
	Name string
	// HostIP is the address the caller chose, or "".
	HostIP string
	// PublishFor names an option that needs a published port, such as
	// "WithPort", or is "".
	PublishFor string
	// Timeout bounds the lookup of a shared network, which runs before the
	// start timeout begins. It must be positive: at zero the lookup fails
	// at once, and the port is always published.
	Timeout time.Duration
	Logger  dockerclient.Logger
	// Resolve and Find replace Resolve and FindSharedNetwork when set, so a
	// test can describe any machine.
	Resolve func(dockerHost, override string) (Address, error)
	Find    func(context.Context, dockerclient.Client) (string, error)
}

// Decide works out how to connect, before anything is created: on a network
// shared with this process's container if there is one, or else through a
// published port.
func Decide(ctx context.Context, docker dockerclient.Client, in ReachInput) (Reach, error) {
	resolve := in.Resolve
	if resolve == nil {
		resolve = Resolve
	}
	// An address that cannot be worked out is needed only to publish a port,
	// so a container on an internal network, with no default route, can
	// still share it.
	address, err := resolve(docker.Host(), in.HostIP)
	if network, ok := in.sharedNetwork(ctx, docker, address); ok {
		return Reach{
			Network:     network,
			Source:      "the network " + network + ", which this process's container is on",
			PublishHost: address.Host, // "" if err is set
		}, nil
	}
	if err != nil {
		return Reach{}, err
	}
	return Reach{Host: address.Host, Source: address.Source}, nil
}

// sharedNetwork returns the network to create the container on, unless the
// caller chose something that needs a published port or there is none. In
// both cases the start publishes a port instead, and only logs why.
func (in ReachInput) sharedNetwork(ctx context.Context, docker dockerclient.Client, address Address) (string, bool) {
	if option := in.publishingOption(address); option != "" {
		in.Logger.Debug("Publishing the container's port, because of an option.", "container", in.Name, "option", option)
		return "", false
	}
	find := in.Find
	if find == nil {
		find = FindSharedNetwork
	}
	ctx, cancel := context.WithTimeout(ctx, in.Timeout)
	defer cancel()
	network, err := find(ctx, docker)
	if err != nil {
		in.Logger.Debug("Publishing the container's port, because no network shared with this process's container was found.",
			"container", in.Name, "reason", err)
		return "", false
	}
	return network, true
}

// publishingOption names what needs a published port, or is "". WithHostIP
// and DBTEST_HOST_IP choose an address; the image package names the rest.
func (in ReachInput) publishingOption(address Address) string {
	if address.Chosen {
		return address.Source
	}
	return in.PublishFor
}
