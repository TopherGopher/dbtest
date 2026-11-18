package mongod

import (
	"context"
	"strconv"
	"sync"
	"time"

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
	deadline, err := c.create(ctx, cfg, host)
	if err != nil {
		return nil, err
	}
	if err := c.start(ctx, cfg, deadline); err != nil {
		c.removeAfterFailedStart(ctx)
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

// create has the daemon create the container, and reports a teardown that
// asked for the container while the daemon was at work. It returns the time
// by which the rest of the start has to finish.
func (c *Container) create(ctx context.Context, cfg config, host string) (time.Time, error) {
	deadline, err := c.createOrPull(ctx, cfg, host)
	if err != nil {
		c.ctr.Forget()
		return deadline, err
	}
	if !c.ctr.StopWanted() {
		return deadline, nil
	}
	// That teardown waited for the id and removes the container. This
	// removes it only if the teardown ran out of time first.
	c.removeAfterFailedStart(ctx)
	return deadline, &ContainerExitedError{Name: c.Name(), ID: c.ID(), Status: "removed"}
}

// createOrPull creates the container, pulling the image only if the daemon
// does not have it, which keeps the usual case to one call.
//
// A pull has the pull timeout to itself, and the start timeout begins again
// after one.
func (c *Container) createOrPull(ctx context.Context, cfg config, host string) (time.Time, error) {
	request := cfg.containerConfig(host)
	deadline, err := c.createWithin(ctx, cfg, request)
	if !dockerclient.IsNotFound(err) {
		return deadline, err
	}
	c.logger.Info(pullingMessage, "image", request.Image)
	pulled, err := pullShared(ctx, c.docker, request.Image, cfg.pullTimeout, true)
	if err != nil {
		return deadline, &StartError{Op: OpPullImage, Name: c.Name(), Image: request.Image, Err: err}
	}
	// A mirror's copy keeps the mirror's name: it is not tagged with the
	// name asked for, so the container is created from the mirror's.
	announceMirror(c.logger, request.Image, pulled)
	request.Image = pulled
	c.ref = pulled
	return c.createWithin(ctx, cfg, request)
}

// createWithin creates the container inside the start timeout, and returns
// the time the timeout ends. The timeout begins here and covers every later
// call to the daemon, so a daemon that stops answering cannot hold a caller
// who set no deadline.
func (c *Container) createWithin(ctx context.Context, cfg config, request dockerclient.ContainerConfig) (time.Time, error) {
	deadline := time.Now().Add(cfg.startTimeout)
	bounded, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	err := c.ctr.Create(bounded, request)
	if err == nil {
		return deadline, nil
	}
	if bounded.Err() != nil {
		c.removeUnconfirmed(ctx, cfg)
	}
	failed := &StartError{Op: OpCreate, Name: c.Name(), Image: request.Image, Err: err}
	return deadline, c.unlessCutShort(ctx, bounded, cfg, failed)
}

// removeUnconfirmed removes, by name, a container whose creation was cut
// short: the daemon may have created it all the same, and its id never
// arrived. Only a generated name is used, because a name from WithName may
// belong to a container that is someone else's.
func (c *Container) removeUnconfirmed(ctx context.Context, cfg config) {
	if !cfg.generatedName {
		return
	}
	// The context that was cut short cannot carry the request, and
	// RemoveByName gives one with no deadline its own.
	if err := c.ctr.RemoveByName(context.WithoutCancel(ctx), c.Name()); err != nil {
		c.logger.Error("Creating a container was cut short, and it could not then be removed, so it may exist. "+
			"Remove it by hand: docker rm -fv "+c.Name(),
			"container", c.Name(), "error", err)
	}
}

// unlessCutShort returns a step's error as it is, or inside a NotReadyError
// if bounded, which carries the start timeout, had ended: the step did not
// fail so much as run out of time, and the remedy is the timeout's.
func (c *Container) unlessCutShort(caller, bounded context.Context, cfg config, err error) error {
	if bounded.Err() == nil {
		return err
	}
	return c.notReady(caller, cfg, bounded.Err(), err)
}

// notReady builds the error for a start that ran out of time or was
// cancelled, at whatever step it had reached. caller is the context passed
// to Start, which tells a start its caller ended from one that timed out.
func (c *Container) notReady(caller context.Context, cfg config, cause, lastErr error) error {
	return &NotReadyError{
		Name: c.Name(), ID: c.ID(), Host: c.host, Port: c.port, HostSource: cfg.hostSource,
		Timeout: cfg.startTimeout, ContextEnded: caller.Err() != nil, Cause: cause, LastErr: lastErr,
	}
}

// start runs the created container and returns once it is usable.
//
// Everything here shares the one deadline: starting the container, the wait
// for mongod and the replica set's initiation.
func (c *Container) start(ctx context.Context, cfg config, deadline time.Time) error {
	bounded, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	notReady := func(cause, lastErr error) error { return c.notReady(ctx, cfg, cause, lastErr) }

	info, err := c.run(bounded, cfg)
	if err != nil {
		return c.unlessCutShort(ctx, bounded, cfg, err)
	}
	probe := readyProbe{
		docker: c.docker, logger: c.logger,
		id: c.ID(), name: c.Name(), host: c.host, port: c.port,
		requireAuth:    cfg.username != "",
		tlsConfig:      c.probeTLSConfig(),
		hasHealthCheck: declaresHealthCheck(info.State.Health.Status),
		notReady:       notReady,
	}
	if err := probe.wait(bounded); err != nil {
		return err
	}
	if cfg.replicaSet == "" {
		return nil
	}
	err = c.initiateReplicaSet(bounded)
	if err != nil && bounded.Err() != nil {
		// Out of time, which says nothing about whether the image can
		// initiate a set at all. A set initiated just as the time ran out
		// is a start that succeeded.
		return notReady(bounded.Err(), errNoPrimary)
	}
	return err
}

// run starts the created container and records the port it is published
// on. A replica set member that authenticates is given its key file first,
// because mongod will not start without one.
func (c *Container) run(ctx context.Context, cfg config) (dockerclient.ContainerInspect, error) {
	if cfg.needsKeyFile() {
		if err := copyKeyFile(ctx, c.docker, c.ID(), c.Image()); err != nil {
			return dockerclient.ContainerInspect{}, c.startError(OpCopyKeyFile, err)
		}
	}
	if c.certs != nil {
		if err := c.certs.copyInto(ctx, c.docker, c.ID()); err != nil {
			return dockerclient.ContainerInspect{}, c.startError(OpTLSCertificate, err)
		}
	}
	c.logger.Debug("Starting the MongoDB container.", "container", c.Name(), "image", c.Image())
	if err := c.docker.ContainerStart(ctx, c.ID()); err != nil {
		return dockerclient.ContainerInspect{}, c.startError(OpStart, err)
	}
	info, err := c.docker.ContainerInspect(ctx, c.ID())
	if err != nil {
		return dockerclient.ContainerInspect{}, c.startError(OpInspect, err)
	}
	// A mongod that rejects its flags can exit before this inspect. Report
	// the exit here: a stopped container publishes no port, so the port
	// check below would blame the wrong thing.
	if container.HasExited(info.State) {
		return dockerclient.ContainerInspect{}, container.NewExitedError(ctx, c.docker, c.Name(), c.ID(), info.State, nil)
	}
	port, published := hostPort(info)
	if !published {
		return dockerclient.ContainerInspect{}, &UnpublishedPortError{Port: mongoPort, Name: c.Name(), ID: c.ID()}
	}
	c.port = port
	return info, nil
}

// startError reports which step of a start failed for this container.
func (c *Container) startError(op string, err error) error {
	return &StartError{Op: op, Name: c.Name(), Image: c.Image(), Err: err}
}

// removeAfterFailedStart removes the container a failed start left behind.
// The caller's context may be what failed, so the removal gets its own
// deadline instead.
func (c *Container) removeAfterFailedStart(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), container.CleanupTimeout)
	defer cancel()
	if err := c.Stop(ctx); err != nil {
		c.logger.Error("A container failed to start and then could not be removed, so it may still be running. "+
			"Remove it by hand: docker rm -fv "+c.Name(),
			"container", c.Name(), "error", err)
	}
}

// hostPort returns the host port mongod's port is published on, and whether
// the daemon published it at all.
func hostPort(info dockerclient.ContainerInspect) (int, bool) {
	port, err := strconv.Atoi(info.HostPort(mongoPort))
	if err != nil || port <= 0 {
		return 0, false
	}
	return port, true
}
