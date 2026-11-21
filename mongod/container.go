package mongod

import (
	"context"
	"fmt"
	"strings"

	"github.com/tophergopher/dbtest/dockerclient"
	"github.com/tophergopher/dbtest/internal/container"
)

// Container is a running mongod, created by a start and removed by Stop. It
// holds no MongoDB client: hand URI to a driver to connect.
//
// Every method is safe to call from several goroutines.
type Container struct {
	ctr        *container.Container // the id, the name, and stopping
	image      MongoImage
	ref        string // the reference the container was created from, set before Start returns
	replicaSet string
	volume     string
	username   string
	password   string
	host       string
	port       int
	certs      *certificates // nil without WithTLS
	docker     dockerclient.Client
	logger     dockerclient.Logger
}

// Format prints the container as its name and URI with the password hidden,
// whatever the verb, so that logging a Container cannot leak its credentials.
func (c *Container) Format(f fmt.State, _ rune) {
	if c == nil {
		fmt.Fprint(f, "<nil>")
		return
	}
	fmt.Fprintf(f, "%s (%s)", c.Name(), c.uri(redactedPassword))
}

// ID returns the container id the daemon assigned.
//
//	3f1a9c4b7e2d5a8f0b6c3e9d1a4f7b2c5e8d0a3f6b9c2e5d8a1f4b7c0e3d6a9f
func (c *Container) ID() string { return c.ctr.ID() }

// Name returns the container name: the one WithName gave, or a generated one.
//
//	mongotest-1a2b3c4d
func (c *Container) Name() string { return c.ctr.Name() }

// Image returns the reference of the image the container runs. When the
// image came from a mirror, this is the mirror's reference for it.
//
//	docker.io/library/mongo:8.0
//	public.ecr.aws/docker/library/mongo:8.0
func (c *Container) Image() string { return c.ref }

// MongoImage returns the image that was asked for, in its three parts. It
// differs from Image only when the image came from a mirror.
//
//	Registry:   "docker.io"
//	Repository: "library/mongo"
//	Version:    "8.0"
func (c *Container) MongoImage() MongoImage { return c.image }

// ReplicaSet returns the name given to WithReplicaSet, or "".
//
//	rs0
//
// Atlas Local names its own replica set, so ReplicaSet returns "" for it
// too. See [MongoImage.ReplicaSetPreconfigured].
func (c *Container) ReplicaSet() string { return c.replicaSet }

// Volume returns the name of the volume that keeps the data, given to
// WithNamedVolume or made by WithDurableStorage, or "" when Stop removes the
// data with the container.
//
//	mongotest-data-3f1a9c4b7e2d
func (c *Container) Volume() string { return c.volume }

// Username returns mongod's root user, or "" if it does not authenticate.
//
//	admin       // WithAuth("admin", "secret")
//	mongotest   // generated; see WithAuth
func (c *Container) Username() string { return c.username }

// Password returns the root user's password, or "" if there is none. URI
// already includes it.
func (c *Container) Password() string { return c.password }

// Port returns the port mongod is reachable at: the host port it is
// published on, or 27017 when it shares a network with this process's
// container. A connection string built from Host and Port by hand carries
// no credentials and no TLS, so use URI.
//
//	32768   // published
//	27017   // on a shared network
func (c *Container) Port() int { return c.port }

// Host returns the address mongod is reachable at from this process. It is
// not always loopback; see [Options.WithHostIP]. A connection string built from Host
// and Port by hand carries no credentials and no TLS, so use URI.
//
//	127.0.0.1    // local daemon
//	172.18.0.3   // this process is in a container on the same daemon
//	build-host   // DOCKER_HOST=tcp://build-host:2376
func (c *Container) Host() string { return c.host }

// URI returns the connection string to hand to a driver. It includes the
// password when there is one, so keep it out of logs.
//
//	mongodb://127.0.0.1:32768/?directConnection=true
//	mongodb://admin:secret@127.0.0.1:32768/?directConnection=true   // WithAuth
//	mongodb://127.0.0.1:32768/?directConnection=true&tls=true       // WithTLS
//
// With WithTLS the driver also needs [Container.TLSConfig], which trusts the
// container's certificate.
//
// directConnection makes a driver use this address. Without it a driver
// switches to the address a replica set member advertises, which only works
// inside the container.
func (c *Container) URI() string { return c.uri(c.password) }

// redactedPassword replaces the password in a URI written for a person.
const redactedPassword = "xxxxx"

// uri builds the connection string around the given password, so the logged
// form and the real one cannot drift apart.
func (c *Container) uri(password string) string {
	var b strings.Builder
	b.WriteString("mongodb://")
	if c.username != "" {
		b.WriteString(escapeUserinfo(c.username) + ":" + escapeUserinfo(password) + "@")
	}
	b.WriteString(addr(c.host, c.Port()))
	b.WriteString("/?directConnection=true")
	if c.certs != nil {
		b.WriteString("&tls=true")
	}
	return b.String()
}

// unreserved are the characters a connection string never needs escaped.
const unreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"

// escapeUserinfo percent-encodes a username or password for a connection
// string. net/url is not strict enough: it leaves "$" and "+" alone, and
// drivers disagree about what those mean.
func escapeUserinfo(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := range len(s) {
		c := s[i]
		if strings.IndexByte(unreserved, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0x0f])
	}
	return b.String()
}

// Stop removes the container and its anonymous volumes. It is safe to call
// twice and on a nil Container, and a container that is already gone counts
// as stopped. If the daemon refuses, or ctx ends while Stop is waiting, Stop
// returns the error and can be called again.
//
// A context that has already ended still removes the container, so
// t.Cleanup(func() { c.Stop(t.Context()) }) works.
func (c *Container) Stop(ctx context.Context) error {
	if c == nil {
		return nil
	}
	return c.ctr.Stop(ctx)
}
