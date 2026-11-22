package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"maps"
	"os"
	"strings"
	"time"

	"github.com/tophergopher/dbtest/dockerclient"
)

// Environment variables that configure a whole suite without editing code.
// An option overrides each one; each one overrides the default.
const (
	// EnvImage is the image to run, in any shape WithImage accepts. It is
	// read only when no option names an image. Unlike mongod's, it does not
	// combine with a version an option names.
	EnvImage = "DBTEST_POSTGRES_IMAGE"
	// EnvStartTimeout is how long to wait for the server, as a Go duration:
	// "90s".
	EnvStartTimeout = "DBTEST_POSTGRES_START_TIMEOUT"
	// EnvHostIP is the address containers are reachable at; see WithHostIP.
	EnvHostIP = "DBTEST_HOST_IP"
)

const (
	// DefaultImage is the image a start runs when none is named.
	DefaultImage = officialRepository + ":18"
	// DefaultStartTimeout is how long a start waits for the server.
	DefaultStartTimeout = 60 * time.Second
	// DefaultPullTimeout is how long a pull of a missing image may take.
	DefaultPullTimeout = 10 * time.Minute

	// username and database are the superuser and the database the image
	// creates.
	username = "postgres"
	database = "postgres"
	// serverPort is the port the server listens on inside the container.
	serverPort = "5432/tcp"
	// namePrefix and nameRandomBytes shape a generated container name.
	namePrefix      = "dbtest-postgres-"
	nameRandomBytes = 4
	// passwordRandomBytes sizes a generated password.
	passwordRandomBytes = 16
)

// Options says how StartWithOptions runs a container. Begin with
// DefaultOptions and chain the With methods. Each returns a changed copy, so
// a base shared between tests is never altered by one of them:
//
//	base := postgres.DefaultOptions().WithImage("17")
//	c, err := postgres.StartWithOptions(ctx, base.WithLabel("team", "orders"))
//
// The zero value is the same as DefaultOptions.
type Options struct {
	image        string
	port         int
	name         string
	hostIP       string
	startTimeout time.Duration
	pullTimeout  time.Duration
	labels       map[string]string
	logger       dockerclient.Logger
	docker       dockerclient.Client
	// findNetwork replaces hostaddr.FindSharedNetwork, and networkPort the
	// port dialled on a shared network, for a test that describes a start
	// made inside a container.
	findNetwork func(context.Context, dockerclient.Client) (string, error)
	networkPort int
	// err is the first value a With method could not accept. A With method
	// has nowhere to return an error, so StartWithOptions reports it.
	err error
}

// DefaultOptions returns the options Start uses: the default image, a host
// port the daemon chooses, and a one-minute wait.
func DefaultOptions() Options { return Options{} }

// WithImage sets the image. A lone version, one that starts with a digit,
// means the official image at that tag. Anything with a name is used exactly
// as written, so a name with no tag means "latest", and nothing carries over
// from an earlier call:
//
//	"17"                       docker.io/library/postgres:17
//	"postgres:17-alpine"       as written
//	"pgvector/pgvector:pg18"   as written
//
// Only images built on the official one work, such as PostGIS and pgvector,
// because the start sets the password the way that image reads it. Bitnami's
// image, for one, exits at start.
func (o Options) WithImage(ref string) Options {
	image, err := parseImage("WithImage", ref)
	if err != nil {
		return o.refused(err)
	}
	o.image = image
	return o
}

// WithPort publishes the server on a fixed host port. Prefer the default,
// where the daemon chooses: a port found free can be taken before the
// container binds it, which parallel tests hit regularly.
func (o Options) WithPort(port int) Options {
	o.port = port
	return o
}

// WithName names the container instead of generating a name.
func (o Options) WithName(name string) Options {
	o.name = name
	return o
}

// WithHostIP states the address or hostname containers are reachable at, for
// a network the detection gets wrong. Setting it always publishes the port.
// Without it the first of these that applies is used:
//
//   - the DBTEST_HOST_IP environment variable;
//   - the server's address on the network of this process's container,
//     when that container is on the same daemon and WithPort is not set,
//     with nothing published;
//   - the daemon's own host, when the daemon is remote (tcp, http, https, ssh);
//   - loopback, when this process shares the daemon host's network;
//   - the default gateway, when this process is in a container not on the
//     host network, because the port is published on the host outside it.
func (o Options) WithHostIP(host string) Options {
	if host == "" {
		return o.refused(ErrEmptyHostIP)
	}
	o.hostIP = host
	return o
}

// WithStartTimeout sets how long a start waits for the server. A pull of a
// missing image is not counted: the wait begins again once the image is
// there.
func (o Options) WithStartTimeout(d time.Duration) Options {
	o.startTimeout = d
	return o
}

// WithPullTimeout sets how long a pull of a missing image may take, by a
// start or by PullImage. Zero means DefaultPullTimeout.
func (o Options) WithPullTimeout(d time.Duration) Options {
	o.pullTimeout = d
	return o
}

// WithLabel adds a label to the container.
func (o Options) WithLabel(key, value string) Options {
	labels := make(map[string]string, len(o.labels)+1)
	maps.Copy(labels, o.labels)
	labels[key] = value
	o.labels = labels
	return o
}

// WithLogger sends what a start does to a logger. *slog.Logger works, and
// nil leaves the logger as it was.
func (o Options) WithLogger(l dockerclient.Logger) Options {
	if l != nil {
		o.logger = l
	}
	return o
}

// WithDocker uses a specific Docker client, such as a double from dockermock.
func (o Options) WithDocker(client dockerclient.Client) Options {
	o.docker = client
	return o
}

// refused remembers the first value a With method could not accept.
func (o Options) refused(err error) Options {
	if o.err == nil {
		o.err = err
	}
	return o
}

// config is a set of options made ready for a start: the defaults and the
// environment filled in, and the result checked.
type config struct {
	Options
	// generatedName says the container's name was made up here, so no
	// other container can have it.
	generatedName bool
	// password is the superuser's, generated for each start.
	password string
	// hostSource says what decided the address to dial, for the error a
	// start that runs out of time returns. A start sets it.
	hostSource string
}

// newConfig fills in what the options left open, from the environment and
// then the defaults, and checks the result. It contacts nothing.
func newConfig(opts Options) (config, error) {
	if opts.err != nil {
		return config{}, opts.err
	}
	c := config{Options: opts, password: randomHex(passwordRandomBytes)}
	if c.name == "" {
		c.name, c.generatedName = namePrefix+randomHex(nameRandomBytes), true
	}
	if c.logger == nil {
		c.logger = dockerclient.NopLogger()
	}
	var err error
	if c.image, err = resolveImage(c.image); err != nil {
		return config{}, err
	}
	if c.startTimeout, err = resolveStartTimeout(c.startTimeout); err != nil {
		return config{}, err
	}
	if c.pullTimeout == 0 {
		c.pullTimeout = DefaultPullTimeout
	}
	return c, c.validate()
}

// resolveImage takes the image from the option, then PGTEST_IMAGE, then the
// default.
func resolveImage(fromOption string) (string, error) {
	if fromOption != "" {
		return fromOption, nil
	}
	raw := strings.TrimSpace(os.Getenv(EnvImage))
	if raw == "" {
		return DefaultImage, nil
	}
	return parseImage(EnvImage, raw)
}

// resolveStartTimeout takes the timeout from the option, then
// PGTEST_START_TIMEOUT, then the default.
func resolveStartTimeout(fromOption time.Duration) (time.Duration, error) {
	if fromOption != 0 {
		return fromOption, nil
	}
	raw := os.Getenv(EnvStartTimeout)
	if raw == "" {
		return DefaultStartTimeout, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0, environmentError(EnvStartTimeout, raw, `it is not a positive duration such as "90s" or "2m"`)
	}
	return d, nil
}

// validate rejects a configuration that cannot work, before anything is
// created, so the error can name the option to change.
func (c config) validate() error {
	switch {
	case c.port < 0 || c.port > 65535:
		return invalidPort(c.port)
	case c.startTimeout < 0:
		return invalidStartTimeout(c.startTimeout)
	case c.pullTimeout < 0:
		return invalidPullTimeout(c.pullTimeout)
	}
	return nil
}

// randomHex returns n random bytes in hex, which needs no escaping in a
// container name or a connection string. rand.Read cannot fail on the Go
// version this module requires.
func randomHex(n int) string {
	raw := make([]byte, n)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}
