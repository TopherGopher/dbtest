package mongod

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/tophergopher/dbtest/dockerclient"
	"github.com/tophergopher/dbtest/internal/hostaddr"
)

// Environment variables that configure a whole suite without editing code.
// An option overrides each one; each one overrides the default.
const (
	// EnvImage is the image to run, in any shape WithImage accepts.
	EnvImage = "MONGOTEST_IMAGE"
	// EnvStartTimeout is how long to wait for mongod, as a Go duration: "90s".
	EnvStartTimeout = "MONGOTEST_START_TIMEOUT"
	// EnvHostIP is the address containers are reachable at; see WithHostIP.
	EnvHostIP = "DBTEST_HOST_IP"
)

const (
	// DefaultStartTimeout is how long a start waits for mongod.
	DefaultStartTimeout = 60 * time.Second
	// DefaultPullTimeout is how long a pull of a missing image may take.
	DefaultPullTimeout = 10 * time.Minute
	// DefaultUsername is the root user when credentials are generated.
	DefaultUsername = "mongotest"
	// RequiredLabelKey and RequiredLabelValue mark every container this
	// package starts, so a stray one can be found and removed:
	//
	//	docker rm -fv $(docker ps -aq --filter label=mongotest=regression)
	RequiredLabelKey   = "mongotest"
	RequiredLabelValue = "regression"

	// mongoPort is the port mongod listens on inside the container.
	mongoPort = "27017/tcp"
	// namePrefix and nameRandomBytes shape a generated container name.
	namePrefix      = "mongotest-"
	nameRandomBytes = 4
	// secretRandomBytes sizes a generated password and replica set key.
	secretRandomBytes = 16
)

// Options says how StartWithOptions runs a container. Begin with
// DefaultOptions and chain the With methods. Each returns a changed copy, so
// a base shared between tests is never altered by one of them:
//
//	base := mongod.DefaultOptions().WithImage("7.0")
//	c, err := mongod.StartWithOptions(ctx, base.WithReplicaSet("rs0"))
//
// The zero value is the same as DefaultOptions.
type Options struct {
	// image holds the registry and repository the caller chose, and version
	// a version the caller named, so the two combine in either order.
	image        MongoImage
	version      string
	port         int
	name         string
	hostIP       string
	replicaSet   string
	username     string
	password     string
	noAuth       bool
	tls          bool
	mongodArgs   []string
	startTimeout time.Duration
	pullTimeout  time.Duration
	labels       map[string]string
	logger       dockerclient.Logger
	docker       dockerclient.Client
	// findNetwork replaces hostaddr.FindSharedNetwork, resolveHost
	// hostaddr.Resolve, and networkPort the port dialled on a shared
	// network, for a test that describes a start made inside a container.
	findNetwork func(context.Context, dockerclient.Client) (string, error)
	resolveHost func(dockerHost, override string) (hostaddr.Address, error)
	networkPort int
	// err is the first value a With method could not accept. A With method
	// has nowhere to return an error, so StartWithOptions reports it.
	err error
}

// DefaultOptions returns the options Start uses: the official image at its
// default version, a host port the daemon chooses, and a one-minute wait.
func DefaultOptions() Options { return Options{} }

// WithImage sets the image from a reference. Only the parts it names change,
// and a name with no registry is taken to be Docker Hub's:
//
//	"8.0"                                    the version
//	"mongodb/mongodb-atlas-local"            the image, at its default version
//	"mirror.corp.example/mongo"              the same image from a mirror
//	"public.ecr.aws/docker/library/mongo:8"  everything
func (o Options) WithImage(ref string) Options {
	parsed, err := parseImage("WithImage", ref)
	if err != nil {
		return o.refused(err)
	}
	return o.WithMongoImage(parsed)
}

// WithMongoImage sets the image from a value, such as one of the known
// images. Like WithImage it changes only the parts the value names.
//
// A version named anywhere stays until another is named, even when the image
// changes after it:
//
//	mongod.DefaultOptions().WithImage("7.0").WithMongoImage(mongod.ImagePublicECR)   // public ECR's mongo:7.0
//	mongod.DefaultOptions().WithMongoImage(mongod.ImagePublicECR).WithImage("7.0")   // the same
//
// So WithImage("mongo:7.0").WithMongoImage(mongod.ImageCommunity) is refused
// with a [NoSuchVersionError]: the community image publishes no tag "7.0".
func (o Options) WithMongoImage(img MongoImage) Options {
	img = img.qualified()
	if img.Repository != "" {
		o.image.Registry, o.image.Repository = img.Registry, img.Repository
	}
	if img.Version != "" {
		o.version = img.Version
	}
	return o
}

// WithPort publishes mongod on a fixed host port. Prefer the default, where
// the daemon chooses: a port found free can be taken before the container
// binds it, which parallel tests hit regularly.
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
//   - mongod's address on the network of this process's container, when
//     that container is on the same daemon and neither WithPort nor WithTLS
//     is set, with nothing published;
//   - the daemon's own host, when the daemon is remote (tcp, http, https, ssh);
//   - loopback, when this process shares the daemon host's network;
//   - the default gateway, when this process is in a container not on the
//     host network, because the port is published on the host outside it.
//
// The shared network fails where containers on it cannot reach each other,
// as on a bridge with icc=false, Docker's setting that stops containers on a
// network talking to each other. The start then times out with
// [ErrNotReady], and its error names the address to pass here instead.
func (o Options) WithHostIP(host string) Options {
	if host == "" {
		return o.refused(ErrEmptyHostIP)
	}
	o.hostIP = host
	return o
}

// WithReplicaSet starts mongod as a single-member replica set with this
// name, which is what transactions and change streams need. The start
// initiates the set and returns once it accepts writes, which adds about
// half a second. That uses the shell in the image, mongosh or the legacy
// mongo, so an image with neither fails with [ErrNotInitiated].
func (o Options) WithReplicaSet(name string) Options {
	if name == "" {
		return o.refused(ErrEmptyReplicaSet)
	}
	o.replicaSet = name
	return o
}

// WithAuth creates a root user and makes mongod require authentication.
// [Container.URI] then includes the credentials.
//
// Creating the user makes a start a few seconds slower, so by default
// credentials are only generated when other machines or containers can
// reach mongod.
// Anyone who can use the Docker daemon can read them, so do not reuse a
// password that matters.
func (o Options) WithAuth(username, password string) Options {
	o.username, o.password, o.noAuth = username, password, false
	return o
}

// WithoutAuth runs mongod without authentication even where other machines
// or containers can reach it. Use it only on a network you trust.
func (o Options) WithoutAuth() Options {
	o.username, o.password, o.noAuth = "", "", true
	return o
}

// WithTLS makes mongod accept only TLS connections, with a certificate made
// for this container. Connect with [Container.URI] and give the driver
// [Container.TLSConfig], which trusts that certificate. The port is always
// published, because the certificate must name the address clients dial,
// and the container has no network address until it starts. An image that takes no mongod flags, such as Atlas
// Local, refuses it with [ErrUnsupportedForImage].
func (o Options) WithTLS() Options {
	o.tls = true
	return o
}

// WithMongodArgs passes extra flags to mongod, after any given before.
func (o Options) WithMongodArgs(args ...string) Options {
	o.mongodArgs = append(slices.Clone(o.mongodArgs), args...)
	return o
}

// WithStartTimeout sets how long a start waits for mongod. A pull of a
// missing image is not counted: the wait begins again once the image is there.
func (o Options) WithStartTimeout(d time.Duration) Options {
	o.startTimeout = d
	return o
}

// WithPullTimeout sets how long a pull of a missing image may take, by a
// start or by PullImage. Zero means DefaultPullTimeout. A call that joins a
// pull another call started waits no longer than that call's timeout.
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
// nil leaves the logger as it was. If Ctrl-C stops the process and the
// container cannot be removed, the logger also gets an Error record naming
// it.
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

// Format prints the options with the password hidden, whatever the verb, so
// that logging an Options cannot leak it.
func (o Options) Format(f fmt.State, _ rune) {
	password := ""
	if o.password != "" {
		password = redactedPassword
	}
	fmt.Fprintf(f, "mongod.Options{image: %q, name: %q, port: %d, hostIP: %q, replicaSet: %q, username: %q, password: %q, "+
		"noAuth: %t, tls: %t, mongodArgs: %q, startTimeout: %s, pullTimeout: %s, labels: %v}",
		o.image.WithVersion(o.version).Reference(), o.name, o.port, o.hostIP, o.replicaSet, o.username, password,
		o.noAuth, o.tls, o.mongodArgs, o.startTimeout, o.pullTimeout, o.labels)
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
	// hostSource says what decided the address to dial, for the error a
	// start that runs out of time returns. A start sets it.
	hostSource string
	// network and publishHost are the reach's, for that error's advice.
	network, publishHost string
}

// newConfig fills in what the options left open, from the environment and
// then the defaults, and checks the result. It contacts nothing.
func newConfig(opts Options) (config, error) {
	if opts.err != nil {
		return config{}, opts.err
	}
	c := config{Options: opts.WithLabel(RequiredLabelKey, RequiredLabelValue)}
	if c.name == "" {
		c.name, c.generatedName = namePrefix+randomHex(nameRandomBytes), true
	}
	if c.logger == nil {
		c.logger = dockerclient.NopLogger()
	}

	var err error
	if c.image, err = resolveImage(c.image.WithVersion(c.version)); err != nil {
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

// resolveImage fills in what the options left open. An option that names a
// repository decides the image. Otherwise MONGOTEST_IMAGE does, with any
// version the options gave, and after that the defaults.
func resolveImage(image MongoImage) (MongoImage, error) {
	if image.Repository == "" {
		fromEnv, err := imageFromEnv()
		if err != nil {
			return MongoImage{}, err
		}
		image.Registry, image.Repository = fromEnv.Registry, fromEnv.Repository
		if image.Version == "" {
			image.Version = fromEnv.Version
		}
	}
	if image.Repository == "" {
		image.Registry, image.Repository = registryDockerHub, repositoryOfficial
	}
	if image.Version == "" {
		image.Version = image.defaultTag()
	}
	// Everything given as text was checked when it was parsed, so only the
	// fields of an image value can make a reference docker would refuse.
	if _, err := dockerclient.ParseImageRef(image.Reference()); err != nil {
		return MongoImage{}, &imageRefError{source: "WithMongoImage", ref: image.Reference(), reason: errNotAnImage}
	}
	return image, nil
}

// imageFromEnv reads MONGOTEST_IMAGE, or returns no image if it is unset.
func imageFromEnv() (MongoImage, error) {
	ref := os.Getenv(EnvImage)
	if ref == "" {
		return MongoImage{}, nil
	}
	image, err := parseImage(EnvImage, ref)
	if errors.Is(err, ErrEmptyImage) {
		// Only spaces. ErrEmptyImage names WithImage, which nobody called.
		return MongoImage{}, environmentError(EnvImage, ref, err)
	}
	return image, err
}

// resolveStartTimeout takes the timeout from the option, then
// MONGOTEST_START_TIMEOUT, then the default.
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
		return 0, environmentError(EnvStartTimeout, raw, errNotADuration)
	}
	return d, nil
}

// validate rejects a configuration that cannot work, before anything is
// created, so the error can name the option to change.
func (c config) validate() error {
	hasUsername, hasPassword := c.username != "", c.password != ""
	switch {
	case c.port < 0 || c.port > 65535:
		return invalidPort(c.port)
	case c.startTimeout < 0:
		return invalidStartTimeout(c.startTimeout)
	case c.pullTimeout < 0:
		return invalidPullTimeout(c.pullTimeout)
	case hasUsername != hasPassword:
		return ErrIncompleteCredentials
	case !creatable(c.username) || !creatable(c.password):
		return ErrUnusableCredentials
	case !c.image.hasBareVersionTags() && isBareVersion(c.image.Version):
		return &NoSuchVersionError{Image: c.image}
	case c.replicaSet != "" && c.image.ReplicaSetPreconfigured():
		return &UnsupportedForImageError{Image: c.image, Option: "WithReplicaSet", Reason: reasonPreconfiguredReplicaSet}
	case len(c.mongodArgs) > 0 && !c.image.acceptsMongodArgs():
		return &UnsupportedForImageError{Image: c.image, Option: "WithMongodArgs", Reason: reasonCommandIsEntrypoint}
	case c.tls && !c.image.acceptsMongodArgs():
		return &UnsupportedForImageError{Image: c.image, Option: "WithTLS", Reason: reasonNoTLSFlags}
	}
	return nil
}

// creatable reports whether every image can create a user from a username
// or password. See ErrUnusableCredentials.
func creatable(credential string) bool {
	return !strings.ContainsAny(credential, "\\`") && !strings.Contains(credential, "${")
}

// isBareVersion reports whether a tag is only digits and dots, such as "8"
// or "8.0.30", with no OS variant after it.
func isBareVersion(version string) bool {
	if version == "" {
		return false
	}
	for _, c := range version {
		if !isDigit(c) && c != '.' {
			return false
		}
	}
	return isDigit(rune(version[0]))
}

// isDigit reports whether c is an ASCII digit.
func isDigit(c rune) bool { return c >= '0' && c <= '9' }

// randomHex returns n random bytes in hex, which needs no escaping in a
// container name, a connection string or a key file. rand.Read cannot fail
// on the Go version this module requires.
func randomHex(n int) string {
	raw := make([]byte, n)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}
