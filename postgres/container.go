package postgres

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"

	"github.com/tophergopher/dbtest/internal/container"
)

// Container is a running PostgreSQL server, created by a start and removed
// by Stop. It holds no client: hand URI to a driver to connect.
//
// Every method is safe to call from several goroutines.
type Container struct {
	ctr      *container.Container // the id, the name, and stopping
	ref      string               // the image the container runs, set before Start returns
	password string
	host     string
	port     int
}

// Format prints the container as its name and URI with the password hidden,
// whatever the verb, so that logging a Container cannot leak it.
func (c *Container) Format(f fmt.State, _ rune) {
	if c == nil {
		fmt.Fprint(f, "<nil>")
		return
	}
	fmt.Fprintf(f, "%s (%s)", c.Name(), c.uri(redactedPassword))
}

// ID returns the container id the daemon assigned.
func (c *Container) ID() string { return c.ctr.ID() }

// Name returns the container name: the one WithName gave, or a generated one.
//
//	pgtest-1a2b3c4d
func (c *Container) Name() string { return c.ctr.Name() }

// Image returns the reference of the image the container runs. When the
// image came from a mirror, this is the mirror's reference for it.
//
//	docker.io/library/postgres:18
//	public.ecr.aws/docker/library/postgres:18
func (c *Container) Image() string { return c.ref }

// Host returns the address the server is reachable at from this process. It
// is not always loopback; see [Options.WithHostIP]. A connection string
// built from Host and Port by hand has no password, so use URI.
//
//	127.0.0.1    // local daemon
//	172.18.0.3   // this process is in a container on the same daemon
//	build-host   // DOCKER_HOST=tcp://build-host:2376
func (c *Container) Host() string { return c.host }

// Port returns the port the server is reachable at: the host port it is
// published on, or 5432 when it shares a network with this process's
// container. A connection string built from Host and Port by hand has no
// password, so use URI.
//
//	32768   // published
//	5432    // on a shared network
func (c *Container) Port() int { return c.port }

// Username returns the superuser to connect as, which is always "postgres".
func (c *Container) Username() string { return username }

// Password returns the superuser's password, generated for this container.
// URI already includes it.
func (c *Container) Password() string { return c.password }

// Database returns the database to connect to, which is always "postgres".
// It is empty when the start returns.
func (c *Container) Database() string { return database }

// URI returns the connection string to hand to a driver, such as pgx or
// lib/pq. It includes the password, so keep it out of logs.
//
//	postgres://postgres:3f9a…@127.0.0.1:32768/postgres?sslmode=disable
//
// It spells out every part, so PGHOST, PGPASSWORD and the other variables
// libpq and pgx read cannot change where it connects. sslmode=disable is
// there because the image serves no TLS.
func (c *Container) URI() string { return c.uri(c.password) }

// redactedPassword replaces the password in a URI written for a person.
const redactedPassword = "xxxxx"

// uri builds the connection string around the given password, so the logged
// form and the real one cannot drift apart.
func (c *Container) uri(password string) string {
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(username, password),
		Host:     net.JoinHostPort(c.host, strconv.Itoa(c.port)),
		Path:     "/" + database,
		RawQuery: "sslmode=disable",
	}
	return u.String()
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
