// Package mongotest gives a test a MongoDB server in a Docker container and a
// connected client for it, using the official driver v1:
//
//	func TestOrders(t *testing.T) {
//		db := mongotest.Run(t).Database("shop")
//		...
//	}
//
// Run starts the container, connects, and removes the container when the
// test ends. The container itself comes from the mongod package, which
// imports no driver; this package adds the client.
package mongotest

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/tophergopher/mongotest/mongod"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Options configures a start. Its With methods return a changed copy, as in
// mongotest.DefaultOptions().WithReplicaSet("rs0"); see the mongod package.
type Options = mongod.Options

// DefaultOptions returns the options Start uses.
func DefaultOptions() Options { return mongod.DefaultOptions() }

// ErrNoContainer is returned when an instance from Attach is asked to do
// something only a container it started can do, such as run a script.
var ErrNoContainer = errors.New("mongotest: the instance was attached to a running server and has no container")

// Instance is a MongoDB server and a client connected to it. The client's
// methods are promoted, so inst.Database("app") works.
type Instance struct {
	*mongo.Client
	// Container is the container the instance started, or nil for one from
	// Attach.
	Container *mongod.Container

	uri      string
	stopOnce sync.Once
}

// Start starts a MongoDB container with the default options and connects to
// it. Call Stop when done.
func Start(ctx context.Context) (*Instance, error) {
	return StartWithOptions(ctx, DefaultOptions())
}

// StartWithOptions starts a MongoDB container and connects to it. If the
// client cannot connect, the container is removed.
func StartWithOptions(ctx context.Context, opts Options) (*Instance, error) {
	c, err := mongod.StartWithOptions(ctx, opts)
	if err != nil {
		return nil, err
	}
	client, err := connect(ctx, c.URI(), clientOptions(c))
	if err != nil {
		return nil, errors.Join(err, c.Stop(context.WithoutCancel(ctx)))
	}
	return &Instance{Client: client, Container: c, uri: c.URI()}, nil
}

// clientOptions returns how to connect to a container. URI carries the
// address this process can reach, any credentials, and directConnection,
// which keeps a replica set member's driver off the localhost address the
// member advertises. The TLS configuration goes after the URI, because
// applying a URI that says tls=true replaces it.
func clientOptions(c *mongod.Container) *options.ClientOptions {
	opts := options.Client().ApplyURI(c.URI())
	if config := c.TLSConfig(); config != nil {
		opts.SetTLSConfig(config)
	}
	return opts
}

// Run starts a MongoDB container for a test and connects to it, and removes
// it when the test ends. Any failure, Docker being unreachable included,
// fails the test: it is never skipped.
func Run(tb testing.TB) *Instance {
	tb.Helper()
	return RunWithOptions(tb, DefaultOptions())
}

// RunWithOptions is Run with options.
func RunWithOptions(tb testing.TB, opts Options) *Instance {
	tb.Helper()
	inst, err := StartWithOptions(tb.Context(), opts)
	if err != nil {
		tb.Fatalf("mongotest: cannot start MongoDB: %v", err)
	}
	// The test's context has ended by the time cleanup runs; Stop removes
	// the container all the same.
	tb.Cleanup(func() { _ = inst.Stop(context.Background()) })
	return inst
}

// Attach connects to a MongoDB server that is already running. Its instance
// has no container, and Stop only disconnects.
func Attach(ctx context.Context, uri string) (*Instance, error) {
	client, err := connect(ctx, uri, options.Client().ApplyURI(uri))
	if err != nil {
		return nil, err
	}
	return &Instance{Client: client, uri: uri}, nil
}

// connect creates a client and pings the server once, so a URI that leads
// nowhere is reported here rather than at the first command.
func connect(ctx context.Context, uri string, opts *options.ClientOptions) (*mongo.Client, error) {
	client, err := mongo.Connect(ctx, opts)
	if err != nil {
		return nil, err
	}
	if err := client.Ping(ctx, nil); err != nil {
		_ = client.Disconnect(context.WithoutCancel(ctx))
		return nil, err
	}
	return client, nil
}

// URI returns the connection string the instance connected with.
func (inst *Instance) URI() string { return inst.uri }

// Stop disconnects the client, then removes the container if the instance
// started one. A second call returns nil.
func (inst *Instance) Stop(ctx context.Context) error {
	if inst == nil {
		return nil
	}
	var err error
	inst.stopOnce.Do(func() {
		err = errors.Join(inst.Client.Disconnect(ctx), inst.Container.Stop(ctx))
	})
	return err
}
