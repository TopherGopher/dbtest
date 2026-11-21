// Package dockerclient declares the Docker Engine API surface that dbtest
// depends on, together with the types and errors that surface carries.
//
// It lets callers depend on an interface rather than on one particular
// Docker client. Every implementation reports the same errors for the same
// conditions. Every client that connects to a daemon finds it with
// ResolveHost, so they find the same daemon. Only ResolveHost and ConfigDir
// read the environment, and only ResolveHost connects to local sockets to
// see which answers.
//
// # Choosing an implementation
//
//   - github.com/tophergopher/dbtest/dockerapi is the default: a client
//     built on the Go standard library alone, with no third-party
//     dependencies.
//   - github.com/tophergopher/dbtest/mobyclient wraps the official
//     github.com/moby/moby/client. It is a separate module, so its
//     dependency tree only reaches builds that ask for it.
//   - github.com/tophergopher/dbtest/dockermock provides test doubles: a
//     Mock that records calls, a Fake with an in-memory container store, and
//     an HTTP-level Daemon that a real client can be pointed at.
//
// Code that starts containers should accept a Client and let the caller
// decide which of those it gets:
//
//	func StartMongo(ctx context.Context, docker dockerclient.Client) error {
//		id, _, err := docker.ContainerCreate(ctx, "", dockerclient.ContainerConfig{
//			Image:        "mongo:8",
//			ExposedPorts: map[string]struct{}{"27017/tcp": {}},
//			HostConfig: &dockerclient.HostConfig{PortBindings: map[string][]dockerclient.PortBinding{
//				"27017/tcp": {{HostIP: "127.0.0.1"}}, // an empty HostPort lets the daemon choose
//			}},
//		})
//		if dockerclient.IsNotFound(err) {
//			if err = docker.ImagePull(ctx, "mongo:8"); err != nil {
//				return err
//			}
//			id, _, err = docker.ContainerCreate(ctx, "", dockerclient.ContainerConfig{Image: "mongo:8"})
//		}
//		if err != nil {
//			return err
//		}
//		return docker.ContainerStart(ctx, id)
//	}
//
// # Errors
//
// Every implementation reports failures as one of the sentinels below, or as
// a typed value that wraps one, so callers branch with errors.Is and never
// parse a message:
//
//   - ErrInvalidArgument (InvalidArgumentError): a value was rejected before
//     any request was sent. The message names the argument, the problem and
//     the fix.
//   - ErrConnectionFailed (ConnectionError): the daemon could not be
//     reached, or no TLS connection could be made with it.
//   - ErrAPIVersion (APIVersionError): the client and daemon API windows do
//     not overlap, or a feature needs a newer daemon.
//   - ErrNotFound, ErrConflict, ErrUnauthorized (StatusError): the daemon
//     answered 404, 409, or 401/403, with its own message and a hint, or a
//     proxy in front of it refused the request (StatusError.FromProxy).
//   - ErrDaemonResponse (ResponseError): the daemon's answer could not be
//     used, or a request failed after its connection was made.
//   - ErrStream (StreamError), ErrPull (PullError): an exec output stream or
//     a pull progress stream reported a problem.
//   - ErrRateLimited (StatusError or PullError): a registry refused a pull
//     because too many came from this address or account.
//
// # Conformance
//
// dockerclienttest, in this directory, holds a conformance suite and a
// benchmark suite that every implementation runs, so the clients behave the
// same way and are measured on the same scale.
package dockerclient
