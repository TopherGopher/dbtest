package postgres

import (
	"context"

	"github.com/tophergopher/dbtest/dockerclient"
)

// This file is compiled only into the tests. It lets a test outside the
// package assert on a private error or decision, where it would otherwise
// have to read the wording of a message.

// Why the server was not found ready.
var (
	ErrNoReply     = errNoReply
	ErrNotPostgres = errNotPostgres
	ErrStartingUp  = errStartingUp
)

// WithNetworkLookup replaces the lookup a start makes for a network shared
// with this process's container, to describe running in a container.
func (o Options) WithNetworkLookup(lookup func(context.Context, dockerclient.Client) (string, error)) Options {
	o.findNetwork = lookup
	return o
}

// WithNetworkPort replaces 5432 as the port dialled on a shared network, so
// that a stand-in for the server can listen on any free port.
func (o Options) WithNetworkPort(port int) Options {
	o.networkPort = port
	return o
}
