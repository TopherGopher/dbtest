package mobyclient

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/moby/moby/client"

	"github.com/tophergopher/mongotest/dockerclient"
)

// Client wraps a *client.Client from github.com/moby/moby/client so it
// satisfies dockerclient.Client. It is safe for concurrent use: the moby
// client it wraps is.
type Client struct {
	cli    *client.Client
	host   string
	logger dockerclient.Logger
}

// Client satisfies the interface every mongotest package depends on.
var _ dockerclient.Client = (*Client)(nil)

// Option configures a Client built by New.
type Option func(*clientConfig) error

// clientConfig accumulates the options passed to New before the underlying
// moby client is built.
type clientConfig struct {
	host       string
	mobyClient *client.Client
	logger     dockerclient.Logger
}

// WithHost sets the daemon address (unix:///path, tcp://host:port,
// https://host:port) and skips discovery. The API version, and TLS for a
// tcp:// or https:// address, are still read from the environment. It has no
// effect when combined with WithMobyClient, since that client is already
// fully configured.
func WithHost(host string) Option {
	return func(cfg *clientConfig) error {
		cfg.host = host
		return nil
	}
}

// WithMobyClient wraps a *client.Client the caller already constructed,
// instead of building one from WithHost. Use this when the caller needs
// moby client options this package does not expose directly, such as a
// custom *tls.Config or extra HTTP headers.
func WithMobyClient(cli *client.Client) Option {
	return func(cfg *clientConfig) error {
		if cli == nil {
			return dockerclient.InvalidArgument("mobyclient client", "", "the value is nil",
				"pass a client built with client.New, or omit WithMobyClient to let mobyclient build one")
		}
		cfg.mobyClient = cli
		return nil
	}
}

// WithLogger sets the logger that receives one Debug record per request,
// via the moby client's response hooks. *slog.Logger satisfies Logger
// directly. The default discards everything. It has no effect when combined
// with WithMobyClient, since hooks are only attached to a client this
// package builds.
func WithLogger(l dockerclient.Logger) Option {
	return func(cfg *clientConfig) error {
		if l != nil {
			cfg.logger = l
		}
		return nil
	}
}

// New creates a Client. Without WithHost or WithMobyClient it finds the
// daemon as the docker CLI does: DOCKER_HOST, then the docker context, and
// otherwise the first Docker or Podman socket that answers (see
// dockerclient.ResolveHost). Whether the address is passed or found,
// DOCKER_TLS_VERIFY turns on TLS for a tcp:// daemon, and DOCKER_API_VERSION,
// when set, pins the API version.
//
// It returns an error matching dockerclient.ErrInvalidArgument when the
// Docker configuration cannot be read or a setting is malformed. It does not
// need a daemon to be running.
func New(opts ...Option) (*Client, error) {
	cfg := &clientConfig{logger: dockerclient.NopLogger()}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(cfg); err != nil {
			return nil, err
		}
	}
	if cfg.mobyClient != nil {
		return &Client{cli: cfg.mobyClient, host: cfg.mobyClient.DaemonHost(), logger: cfg.logger}, nil
	}
	host, mopts, err := hostOptions(cfg.host)
	if err != nil {
		return nil, err
	}
	mopts = append(mopts, client.WithResponseHook(loggingHook(cfg.logger)))
	cli, err := client.New(mopts...)
	if err != nil {
		return nil, constructError(host, err)
	}
	return &Client{cli: cli, host: cli.DaemonHost(), logger: cfg.logger}, nil
}

// hostOptions returns the daemon's address and the moby options that reach
// it. The address is the one given, or else the one dockerclient.ResolveHost
// finds.
func hostOptions(given string) (string, []client.Opt, error) {
	host := given
	if host == "" {
		found, err := dockerclient.ResolveHost()
		if err != nil {
			return "", nil, err
		}
		host = found
	}
	opts := []client.Opt{client.WithHost(host), client.WithAPIVersionFromEnv()}
	if isTCP(host) {
		opts = append(opts, tlsFromEnv(host)...)
	}
	return host, opts, nil
}

// tlsFromEnv returns the TLS option for a tcp:// or https:// daemon, as
// the docker CLI and dockerapi read the environment: DOCKER_TLS_VERIFY turns
// TLS on and the server's certificate is verified, against ca.pem in
// DOCKER_CERT_PATH or else the Docker config directory, with cert.pem and
// key.pem as the client certificate if both are there. An https:// address
// with no DOCKER_TLS_VERIFY is verified against the system's roots.
func tlsFromEnv(host string) []client.Opt {
	if os.Getenv("DOCKER_TLS_VERIFY") != "" {
		dir := certDir()
		cert, key := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
		if !exists(cert) || !exists(key) {
			cert, key = "", "" // no client certificate
		}
		return []client.Opt{client.WithTLSClientConfig(filepath.Join(dir, "ca.pem"), cert, key)}
	}
	if strings.HasPrefix(host, "https://") {
		return []client.Opt{client.WithTLSClientConfig("", "", "")}
	}
	return nil
}

// isTCP reports whether host is reached over TCP, where TLS applies, rather
// than through a local socket or named pipe.
func isTCP(host string) bool {
	return strings.HasPrefix(host, "tcp://") || strings.HasPrefix(host, "https://")
}

// certDir is where the TLS files are read from: DOCKER_CERT_PATH, else the
// Docker config directory.
func certDir() string {
	if dir := os.Getenv("DOCKER_CERT_PATH"); dir != "" {
		return dir
	}
	return dockerclient.ConfigDir()
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// FromEnv is New with no options.
func FromEnv() (*Client, error) { return New() }

// loggingHook builds a client.ResponseHook that logs one Debug record per
// request, in the same shape dockerapi's WithLogger produces, so a caller
// switching clients sees familiar log lines.
func loggingHook(logger dockerclient.Logger) client.ResponseHook {
	return func(resp *http.Response) {
		if resp == nil || resp.Request == nil {
			return
		}
		logger.Debug("docker request", "method", resp.Request.Method, "path", resp.Request.URL.Path, "status", resp.StatusCode)
	}
}

// constructError reports which setting client.New refused, as an
// InvalidArgumentError. moby's error does not say which it was, so each
// setting is tried on its own; this runs only once building has failed.
func constructError(host string, err error) error {
	switch {
	case refuses(client.WithHost(host)):
		return dockerclient.InvalidArgument("docker host", host, err.Error(),
			`use a host like "unix:///var/run/docker.sock" or "tcp://host:2375"`)
	case refuses(client.WithAPIVersionFromEnv()):
		return dockerclient.InvalidArgument("DOCKER_API_VERSION", os.Getenv("DOCKER_API_VERSION"), err.Error(),
			"set an API version such as 1.44, or unset it to negotiate one with the daemon")
	}
	return dockerclient.InvalidArgument("DOCKER_CERT_PATH", certDir(), err.Error(),
		"point DOCKER_CERT_PATH at the directory holding ca.pem, and cert.pem and key.pem if the daemon wants a client certificate, "+
			"or unset DOCKER_TLS_VERIFY to connect without TLS")
}

// refuses reports whether the moby client cannot be built with opt.
func refuses(opt client.Opt) bool {
	_, err := client.New(opt)
	return err != nil
}

// Host returns the daemon address the client resolved, in DOCKER_HOST form.
func (c *Client) Host() string { return c.host }

// mapErr turns an error the moby client returned for one request into a
// dockerclient sentinel or carrier; see errors.go.
func (c *Client) mapErr(method, path string, err error) error {
	return mapError(c.host, method, path, err)
}
