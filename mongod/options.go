package mongod

import (
	"crypto/rand"
	"encoding/hex"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/tophergopher/mongotest/dockerclient"
)

// Environment variables that configure a whole suite without editing code.
// An option overrides each one; each one overrides the default.
const (
	// EnvImage is the image to run, in any shape WithImage accepts.
	EnvImage = "MONGOTEST_IMAGE"
	// EnvStartTimeout is how long to wait for mongod, as a Go duration: "90s".
	EnvStartTimeout = "MONGOTEST_START_TIMEOUT"
	// EnvHostIP is the address containers are reachable at; see WithHostIP.
	EnvHostIP = "MONGOTEST_HOST_IP"
)

const (
	// DefaultStartTimeout is how long a start waits for mongod.
	DefaultStartTimeout = 60 * time.Second
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
	// cleanupTimeout bounds removing the container a failed start created.
	cleanupTimeout = 30 * time.Second
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
	mongodArgs   []string
	startTimeout time.Duration
	labels       map[string]string
	logger       dockerclient.Logger
	docker       dockerclient.Client
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
	parsed, err := ParseMongoImage(ref)
	if err != nil {
		return o.refused(err)
	}
	return o.withImageParts(parsed, parsed.Version)
}

// WithMongoImage sets the image to one of the known ones. A version named
// with WithImage, before or after, is kept:
//
//	mongod.DefaultOptions().WithMongoImage(mongod.ImagePublicECR).WithImage("7.0")
//
// The image's own version is used only if it differs from that image's
// default, as after MongoImage.WithVersion.
func (o Options) WithMongoImage(img MongoImage) Options {
	named := img.Version
	if named == img.defaultTag() {
		named = ""
	}
	return o.withImageParts(img, named)
}

// withImageParts takes the repository an image names, if it names one, and
// the version the caller named, if any.
func (o Options) withImageParts(img MongoImage, version string) Options {
	img = img.qualified()
	if img.Repository != "" {
		o.image.Registry, o.image.Repository = img.Registry, img.Repository
	}
	if version != "" {
		o.version = version
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
// a network the detection gets wrong. Without it the first of these is used:
//
//  1. the MONGOTEST_HOST_IP environment variable;
//  2. the daemon's own host, when the daemon is remote (tcp, http, https, ssh);
//  3. loopback, when this process shares the daemon host's network;
//  4. the default gateway, when this process is in a container with a network
//     of its own, because the port is published on the host outside it.
func (o Options) WithHostIP(host string) Options {
	if host == "" {
		return o.refused(ErrEmptyHostIP)
	}
	o.hostIP = host
	return o
}

// WithReplicaSet starts mongod as a single-member replica set with this
// name, which is what transactions and change streams need. The start
// initiates the set and returns once it accepts writes. That uses the
// image's own mongosh, so an image without it fails with [ErrNotInitiated].
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
// credentials are only generated for a container other machines can reach.
// Anyone who can use the Docker daemon can read them, so do not reuse a
// password that matters.
func (o Options) WithAuth(username, password string) Options {
	o.username, o.password, o.noAuth = username, password, false
	return o
}

// WithoutAuth runs mongod without authentication even where other machines
// can reach it. Use it only on a network you trust.
func (o Options) WithoutAuth() Options {
	o.username, o.password, o.noAuth = "", "", true
	return o
}

// WithMongodArgs passes extra flags to mongod, after any given before.
func (o Options) WithMongodArgs(args ...string) Options {
	o.mongodArgs = append(slices.Clone(o.mongodArgs), args...)
	return o
}

// WithStartTimeout sets how long a start waits for mongod.
func (o Options) WithStartTimeout(d time.Duration) Options {
	o.startTimeout = d
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
type config struct{ Options }

// newConfig fills in what the options left open, from the environment and
// then the defaults, and checks the result. It contacts nothing.
func newConfig(opts Options) (config, error) {
	if opts.err != nil {
		return config{}, opts.err
	}
	c := config{Options: opts.WithLabel(RequiredLabelKey, RequiredLabelValue)}
	if c.name == "" {
		c.name = namePrefix + randomHex(nameRandomBytes)
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
	if _, err := dockerclient.ParseImageRef(image.Reference()); err != nil {
		return MongoImage{}, err
	}
	return image, nil
}

// imageFromEnv reads MONGOTEST_IMAGE, or returns no image if it is unset.
func imageFromEnv() (MongoImage, error) {
	ref := os.Getenv(EnvImage)
	if ref == "" {
		return MongoImage{}, nil
	}
	image, err := ParseMongoImage(ref)
	if err != nil {
		return MongoImage{}, environmentError(EnvImage, ref, err)
	}
	return image, nil
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
