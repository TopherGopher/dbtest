package mongod

import (
	"context"
	"sync"

	"github.com/tophergopher/mongotest/dockerapi"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/internal/container"
	"github.com/tophergopher/mongotest/internal/hostaddr"
	"github.com/tophergopher/mongotest/internal/reaper"
)

// Start runs MongoDB with the default options: the official image on a host
// port the daemon chooses, waiting up to a minute for it.
func Start(ctx context.Context) (*Container, error) {
	return StartWithOptions(ctx, DefaultOptions())
}

// StartWithOptions creates a MongoDB container, starts it, and waits until
// mongod is reachable. If a step fails, the container is removed and the
// error names the step.
func StartWithOptions(ctx context.Context, opts Options) (*Container, error) {
	cfg, err := newConfig(opts)
	if err != nil {
		return nil, err
	}
	docker, err := cfg.client()
	if err != nil {
		return nil, err
	}
	// Worked out before anything is created, so failing to leaves no
	// container behind.
	address, err := hostaddr.Resolve(docker.Host(), cfg.hostIP)
	if err != nil {
		return nil, err
	}
	// The host decides which interface the port is published on, and so
	// whether the container needs credentials.
	host := address.Host
	cfg = cfg.withCredentialsIfExposed(host)
	cfg.hostSource = address.Source
	cfg.announceExposure(host)

	certs, err := cfg.certificatesFor(host)
	if err != nil {
		return nil, err
	}
	c := newContainer(host, cfg, docker, certs)
	ref, port, err := c.ctr.Start(ctx, c.spec(cfg))
	c.ref, c.port = ref, port
	if err != nil {
		return nil, err
	}
	c.logger.Info(c.readyMessage(), "container", c.Name(), "id", container.ShortID(c.ID()), "uri", c.uri(redactedPassword))
	return c, nil
}

// What a start logs once mongod is ready. The logged URI hides the password,
// because logs are shared more widely than access to the daemon, and the
// line says so only when there is a password to hide.
const (
	readyMessage               = "MongoDB is ready."
	readyMessagePasswordHidden = "MongoDB is ready. The password is hidden in the URI shown here; Container.URI() returns the real one."
)

// readyMessage returns the line for this container.
func (c *Container) readyMessage() string {
	if c.username == "" {
		return readyMessage
	}
	return readyMessagePasswordHidden
}

// pullingMessage is logged before a pull, at a level a first-time user sees:
// a pull can take minutes, and without it a first run looks like a hang.
const pullingMessage = "The Docker daemon does not have this image yet, so it is being pulled. That happens once and can take a few minutes."

// PullImage makes sure the daemon has the image a start with these options
// would run, pulling it only if it is missing. Call it once in TestMain, so
// the download happens before any test starts:
//
//	if err := mongod.PullImage(ctx, mongod.DefaultOptions()); err != nil {
//		log.Fatal(err)
//	}
func PullImage(ctx context.Context, opts Options) error {
	cfg, err := newConfig(opts)
	if err != nil {
		return err
	}
	docker, err := cfg.client()
	if err != nil {
		return err
	}
	image := cfg.image.Reference()
	// Asked first: pulling an image the daemon already has is a trip to
	// the registry for nothing, and can run into its rate limit.
	if _, err := docker.ImageInspect(ctx, image); err == nil {
		return nil
	}
	cfg.logger.Info(pullingMessage, "image", image)
	pulled, err := pullShared(ctx, docker, image, cfg.pullTimeout, false)
	if err != nil {
		return &StartError{Op: OpPullImage, Image: image, Err: err}
	}
	announceMirror(cfg.logger, image, pulled)
	return nil
}

// ReapRunningContainers removes every container Start created in this
// process that has not been stopped. Call it after m.Run in TestMain so a
// run leaves nothing behind:
//
//	func TestMain(m *testing.M) {
//		code := m.Run()
//		if err := mongod.ReapRunningContainers(context.Background()); err != nil {
//			log.Print(err)
//		}
//		os.Exit(code)
//	}
//
// Every removal is attempted, all at the same time, even if one fails. The
// error matches ErrReap, and is a [ReapError] with an entry for each
// container that is still there:
//
//	var re *mongod.ReapError
//	if errors.As(err, &re) {
//		for _, failed := range re.Failures {
//			log.Printf("remove %s by hand: %v", failed.Name, failed.Err)
//		}
//	}
func ReapRunningContainers(ctx context.Context) error { return reaper.Reap(ctx) }

// defaultClient finds the daemon the way the docker CLI does, once for the
// process. A client keeps connections open and has no Close, so a client
// per start would leak one for every container.
var defaultClient = sync.OnceValues(dockerapi.FromEnv)

// client returns the caller's Docker client, or the shared default. This is
// the only place a concrete client is named.
func (c config) client() (dockerclient.Client, error) {
	if c.docker != nil {
		return c.docker, nil
	}
	fromEnv, err := defaultClient()
	if err != nil {
		return nil, err
	}
	return fromEnv, nil
}

// newContainer describes a container that is about to be created, and
// registers it for removal should the process be signalled. See
// container.New.
func newContainer(host string, cfg config, docker dockerclient.Client, certs *certificates) *Container {
	return &Container{
		ctr:   container.New(cfg.name, docker, cfg.logger),
		image: cfg.image, ref: cfg.image.Reference(), replicaSet: cfg.replicaSet,
		username: cfg.username, password: cfg.password,
		host: host, certs: certs, docker: docker, logger: cfg.logger,
	}
}

// startError reports which step of a start failed for this container.
func (c *Container) startError(op string, err error) error {
	return &StartError{Op: op, Name: c.Name(), Image: c.Image(), Err: err}
}

// spec is what the generic start needs from this package: the request, the
// mirror-aware pull, the files copied in before mongod starts, the check
// that mongod is ready, and the replica set's initiation.
func (c *Container) spec(cfg config) container.Spec {
	return container.Spec{
		Request: cfg.containerConfig(c.host), Port: mongoPort,
		Host: c.host, HostSource: cfg.hostSource, GeneratedName: cfg.generatedName,
		StartTimeout: cfg.startTimeout,
		Pull:         c.pullFor(cfg),
		BeforeStart:  c.beforeStart(cfg),
		Ready:        c.ready(cfg),
		AfterReady:   c.afterReady(cfg),
		Advice:       cfg.notReadyAdvice,
	}
}

// pullFor pulls an image the daemon does not have, from a mirror if Docker
// Hub's rate limit refuses it. A mirror's copy keeps the mirror's name, so
// the container is created from that.
func (c *Container) pullFor(cfg config) func(context.Context, string) (string, error) {
	return func(ctx context.Context, image string) (string, error) {
		c.logger.Info(pullingMessage, "image", image)
		pulled, err := pullShared(ctx, c.docker, image, cfg.pullTimeout, true)
		if err != nil {
			return "", &StartError{Op: OpPullImage, Name: c.Name(), Image: image, Err: err}
		}
		announceMirror(c.logger, image, pulled)
		return pulled, nil
	}
}

// beforeStart copies in what mongod needs before it starts: the key file a
// replica set member that authenticates will not start without, and the
// TLS certificate.
func (c *Container) beforeStart(cfg config) func(context.Context, string, string) error {
	return func(ctx context.Context, id, ref string) error {
		if cfg.needsKeyFile() {
			if err := copyKeyFile(ctx, c.docker, id, ref); err != nil {
				return &StartError{Op: OpCopyKeyFile, Name: c.Name(), Image: ref, Err: err}
			}
		}
		if c.certs == nil {
			return nil
		}
		if err := c.certs.copyInto(ctx, c.docker, id); err != nil {
			return &StartError{Op: OpTLSCertificate, Name: c.Name(), Image: ref, Err: err}
		}
		return nil
	}
}

// ready asks mongod, through the published port, whether it is serving and
// enforces any credentials the container was created with.
func (c *Container) ready(cfg config) func(context.Context, int) error {
	return func(ctx context.Context, port int) error {
		probe := readyProbe{
			name: c.Name(), id: c.ID(), host: c.host, port: port,
			requireAuth: cfg.username != "",
			tlsConfig:   c.probeTLSConfig(),
		}
		if err := probe.ask(ctx); err != nil {
			return err
		}
		c.logger.Debug("mongod answered a command at the address tests will connect to.",
			"container", c.Name(), "address", addr(c.host, port))
		return nil
	}
}

// afterReady initiates the replica set, if there is one. Out of time, it
// reports that the set had no primary yet: that says nothing about whether
// the image can initiate a set at all.
func (c *Container) afterReady(cfg config) func(context.Context) error {
	if cfg.replicaSet == "" {
		return nil
	}
	return func(ctx context.Context) error {
		err := c.initiateReplicaSet(ctx)
		if err != nil && ctx.Err() != nil {
			return errNoPrimary
		}
		return err
	}
}
