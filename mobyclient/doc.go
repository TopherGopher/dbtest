// Package mobyclient wraps the official Docker client,
// github.com/moby/moby/client, so it satisfies
// github.com/tophergopher/mongotest/dockerclient.Client. It is its own Go
// module so a caller who does not need it never pulls in the moby client's
// dependency tree (containerd/errdefs, opentelemetry, distribution/reference
// and the rest).
//
// # Choosing between mobyclient and dockerapi
//
// dockerapi, the standard-library client in the parent module, is the
// default: it takes no third-party dependency and covers everything
// mongotest needs. Reach for mobyclient instead when the process already
// depends on github.com/moby/moby/client for other reasons (so this adds
// nothing new to the build), or when a caller needs a daemon endpoint
// dockerapi does not implement and wants to keep using the moby client's own
// richer surface (options, streaming responses, the full Engine API) for
// that, while still handing mongotest a Client it understands.
//
// # Getting a client
//
// New with no options finds the daemon as the docker CLI does: DOCKER_HOST,
// then the docker context, then the Docker and Podman sockets (see
// dockerclient.ResolveHost). FromEnv is the same as New(); use New().
// dockerapi finds
// the same daemon, except on Windows, where it cannot open Docker's named
// pipe and looks for another endpoint.
//
//	c, err := mobyclient.New()
//	if err != nil {
//		return err
//	}
//
// WithHost names the daemon instead, and WithLogger logs each request:
//
//	c, err := mobyclient.New(
//		mobyclient.WithHost("tcp://build-host:2375"),
//		mobyclient.WithLogger(slog.Default()),
//	)
//
// DOCKER_TLS_VERIFY turns on TLS for a tcp:// daemon. The certificates come
// from DOCKER_CERT_PATH, or from the Docker config directory when it is
// unset. DOCKER_API_VERSION pins the API version. Both apply whether the
// address comes from WithHost or is found.
//
// WithMobyClient wraps a *client.Client the caller already built, with its
// own TLS, headers or transport. To use the moby client's own discovery,
// which reads DOCKER_HOST alone with no docker context and no socket search,
// build it with client.FromEnv and wrap it:
//
//	moby, err := client.New(client.FromEnv)
//	if err != nil {
//		return err
//	}
//	c, err := mobyclient.New(mobyclient.WithMobyClient(moby))
//
// # Errors
//
// Every error mobyclient returns is one of the shared dockerclient
// sentinels or a typed value that wraps one, exactly as dockerapi's are, so
// calling code branches with errors.Is without caring which client produced
// the failure:
//
//	if dockerclient.IsNotFound(err) { /* pull, then retry */ }
//
// mobyclient maps the moby client's own errors (github.com/moby/moby/client
// classifies failures with github.com/containerd/errdefs) onto
// dockerclient's StatusError, ConnectionError, ResponseError, StreamError
// and PullError, preserving the daemon's own message. Input is validated
// with the same dockerclient helpers dockerapi uses, so a bad image
// reference, container id or port binding is rejected with the same message
// before any request is sent.
//
// # Testing code that uses this package
//
// mobyclient's own suite runs the shared conformance and benchmark tests
// from dockerclient/dockerclienttest against a client pointed at
// dockermock.Daemon, so it is held to the same behaviour as dockerapi and
// the doubles in dockermock. Every exported function has a runnable Example
// backed by the same fake daemon, so they execute with no Docker daemon
// installed.
package mobyclient
