// mongotest/mongotest.go and mongotestv2/mongotest.go are copies. They
// differ only in the package's name, the package doc, and the driver's import
// path and API. A change to one belongs in the other too.

// Package mongotest gives a test a MongoDB server in a Docker container and a
// connected client for it. The client is from MongoDB's Go driver v1,
// go.mongodb.org/mongo-driver.
//
//	func TestOrders(t *testing.T) {
//		db := mongotest.Run(t).Database("shop")
//		...
//	}
//
// Run starts the container, connects, and removes the container when the
// test ends. To start a server without a driver client, use
// [github.com/tophergopher/dbtest/mongod].
//
// To share one server across a package's tests, call [Start] in TestMain,
// and [Instance.DatabaseFor] in each test for a database of its own.
//
// For driver v2, go.mongodb.org/mongo-driver/v2, use
// [github.com/tophergopher/dbtest/mongotestv2], which has the same API.
package mongotest

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/tophergopher/dbtest/mongod"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Options configures a start. Its With methods return a changed copy, as in
// mongotest.DefaultOptions().WithReplicaSet("rs0"). The known images and the
// errors a start returns are in the mongod package.
type Options = mongod.Options

// DefaultOptions returns the options Start uses.
func DefaultOptions() Options { return mongod.DefaultOptions() }

// Instance is a MongoDB server and a client connected to it. The client's
// methods are promoted, so inst.Database("app") works. Stop, not the
// promoted Disconnect, removes the container.
type Instance struct {
	*mongo.Client
	// Container is the container the instance started, or nil for one from
	// Attach.
	Container *mongod.Container

	uri string
}

// Start starts a MongoDB container with the default options and connects to
// it. Call Stop when done. In TestMain, defer Stop and return after m.Run.
// Calling os.Exit would skip the deferred Stop.
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
	client, err := connect(ctx, clientOptions(c))
	if err != nil {
		return nil, errors.Join(err, c.Stop(ctx))
	}
	return &Instance{Client: client, Container: c, uri: c.URI()}, nil
}

// clientOptions returns how to connect to a container. URI carries the
// address this process can reach, any credentials, and directConnection.
// directConnection stops the driver from switching to the address a replica
// set member advertises, localhost:27017, which this process may not reach.
// The TLS configuration is set after the URI, because applying a URI with
// tls=true would replace a configuration set before it.
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
	tb.Cleanup(func() {
		if err := inst.Stop(context.Background()); err != nil {
			tb.Errorf("mongotest: %v", err)
		}
	})
	return inst
}

// Attach connects to a MongoDB server that is already running. Its instance
// has no container, and Stop only disconnects.
func Attach(ctx context.Context, uri string) (*Instance, error) {
	client, err := connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		return nil, err
	}
	return &Instance{Client: client, uri: uri}, nil
}

// connect creates a client and pings the server once, so a URI that leads
// nowhere is reported here rather than at the first command. The error does
// not quote the URI, which may hold a password.
func connect(ctx context.Context, opts *options.ClientOptions) (*mongo.Client, error) {
	client, err := mongo.Connect(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNotConnected, err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		_ = client.Disconnect(context.WithoutCancel(ctx))
		return nil, fmt.Errorf("%w: %w. Check that the server is running and reachable at the URI", ErrNotConnected, err)
	}
	return client, nil
}

// URI returns the connection string the instance connected with. It
// includes any password, so keep it out of logs. With WithTLS, a driver also
// needs Container.TLSConfig to trust the server.
func (inst *Instance) URI() string { return inst.uri }

// Stop disconnects the client, then removes the container if the instance
// started one. It can be called again, including after a Stop that failed,
// and after the client was disconnected.
func (inst *Instance) Stop(ctx context.Context) error {
	if inst == nil {
		return nil
	}
	err := inst.Client.Disconnect(ctx)
	if errors.Is(err, mongo.ErrClientDisconnected) {
		err = nil
	}
	return errors.Join(err, inst.Container.Stop(ctx))
}
