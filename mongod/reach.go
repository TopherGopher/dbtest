package mongod

import (
	"context"

	"github.com/tophergopher/dbtest/dockerclient"
	"github.com/tophergopher/dbtest/internal/hostaddr"
)

// reach works out how this process will connect to mongod, before anything
// is created: on a network shared with this process's container if there is
// one, or else through a published port.
func (c config) reach(ctx context.Context, docker dockerclient.Client) (hostaddr.Reach, error) {
	return hostaddr.Decide(ctx, docker, hostaddr.ReachInput{
		Name: c.name, HostIP: c.hostIP, PublishFor: c.publishingOption(),
		Timeout: c.startTimeout, Logger: c.logger,
		Resolve: c.resolveHost, Find: c.findNetwork,
	})
}

// publishingOption names the option that needs a published port, or is "".
// WithPort chooses a host port. WithTLS needs the address before the
// container has one, because the certificate names it.
func (c config) publishingOption() string {
	switch {
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
