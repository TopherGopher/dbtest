package mongod

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/tophergopher/mongotest/dockerapi"
	"github.com/tophergopher/mongotest/dockerclient"
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
	address, err := cfg.hostResolver(docker.Host()).resolve()
	if err != nil {
		return nil, err
	}
	// The host decides which interface the port is published on, and so
	// whether the container needs credentials.
	host := dialHost(address.Host)
	cfg = cfg.withCredentialsIfExposed(host)
	cfg.announceExposure(host)

	c := newContainer(host, cfg, docker)
	deadline, err := c.create(ctx, cfg, host)
	if err != nil {
		return nil, err
	}
	if err := c.start(ctx, cfg, address.Source, deadline); err != nil {
		c.removeAfterFailedStart(ctx)
		return nil, err
	}
	// The logged URI hides the password: logs are shared more widely than
	// access to the daemon.
	c.logger.Info("MongoDB is ready. The password is hidden in the URI shown here; Container.URI() returns the real one.",
		"container", c.name, "id", shortID(c.id), "uri", c.uri(redactedPassword))
	return c, nil
}

// PullImage makes sure the daemon has the image a start with these options
// would run, pulling it only if it is missing. Call it once in TestMain, so
// that tests starting in parallel do not each find the image missing:
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
	if err := docker.ImagePull(ctx, image); err != nil {
		return &StartError{Op: OpPullImage, Image: image, Err: err}
	}
	return nil
}

// ReapRunningContainers removes every container Start created in this
// process that has not been stopped. Call it after m.Run in TestMain so a run leaves nothing
// behind:
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
// error matches ErrReap and is a [ReapError] naming each container that is
// still there.
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
// registers it for removal should the process be signalled.
//
// It registers before the daemon is asked, because a signal between the
// daemon creating the container and this process hearing of it would
// otherwise leave the container behind. See createOnce.
func newContainer(host string, cfg config, docker dockerclient.Client) *Container {
	c := &Container{
		name: cfg.name, image: cfg.image, replicaSet: cfg.replicaSet,
		username: cfg.username, password: cfg.password,
		host: host, docker: docker, logger: cfg.logger,
	}
	// Locked because the reaper may call Stop, which reads the handle, the
	// moment Register returns.
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reaperHandle = reaper.Register(c.name, c.stop)
	return c
}

// create has the daemon create the container, and reports a teardown that
// asked for the container while the daemon was at work. It returns the time
// by which the rest of the start has to finish.
func (c *Container) create(ctx context.Context, cfg config, host string) (time.Time, error) {
	deadline, err := c.createOrPull(ctx, cfg, host)
	if err != nil {
		c.forget()
		return deadline, err
	}
	if !c.stopWanted.Load() {
		return deadline, nil
	}
	// That teardown waited for the id and removes the container. This
	// removes it only if the teardown ran out of time first.
	c.removeAfterFailedStart(ctx)
	return deadline, &ContainerExitedError{Name: c.name, ID: c.id, Status: "removed"}
}

// createOrPull creates the container, pulling the image only if the daemon
// does not have it, which keeps the usual case to one call.
//
// The start timeout begins here and covers every later call to the daemon,
// so a daemon that stops answering cannot hold a caller who set no deadline.
// A pull can take minutes and is not counted: the timeout begins again
// after one.
func (c *Container) createOrPull(ctx context.Context, cfg config, host string) (time.Time, error) {
	image := cfg.image.Reference()
	request := cfg.containerConfig(host)

	deadline := time.Now().Add(cfg.startTimeout)
	warnings, err := c.createOnce(ctx, deadline, request)
	if dockerclient.IsNotFound(err) {
		c.logger.Debug("The Docker daemon does not have this image yet, so it is being pulled. That happens once and can take a few minutes.",
			"image", image)
		if err := c.docker.ImagePull(ctx, image); err != nil {
			return deadline, &StartError{Op: OpPullImage, Name: c.name, Image: image, Err: err}
		}
		deadline = time.Now().Add(cfg.startTimeout)
		warnings, err = c.createOnce(ctx, deadline, request)
	}
	if err != nil {
		return deadline, &StartError{Op: OpCreate, Name: c.name, Image: image, Err: err}
	}
	for _, warning := range warnings {
		c.logger.Warn("The Docker daemon created the container, but warned about it.", "container", c.name, "warning", warning)
	}
	return deadline, nil
}

// createOnce asks the daemon for the container and records its id, holding
// the container's lock until the daemon has answered. A teardown that
// arrives meanwhile waits for the id: asked sooner, the daemon might not
// know the container yet, and the teardown would leave it behind.
//
// The lock is not held across a pull, so a container torn down in between
// is not created.
func (c *Container) createOnce(ctx context.Context, deadline time.Time, request dockerclient.ContainerConfig) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return nil, &ContainerExitedError{Name: c.name, Status: "removed"}
	}
	// The lock is held until the daemon answers, so the wait has an end.
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	id, warnings, err := c.docker.ContainerCreate(ctx, c.name, request)
	if err != nil {
		return nil, err
	}
	c.id = id
	return warnings, nil
}

// forget drops a container the daemon never created, so that nothing tries
// to remove it.
func (c *Container) forget() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopped = true
	reaper.Unregister(c.reaperHandle)
}

// start runs the created container and returns once it is usable. hostSource
// says where the dial address came from, for the error a failed wait returns.
//
// Everything here shares the one deadline: starting the container, the wait
// for mongod and the replica set's initiation.
func (c *Container) start(ctx context.Context, cfg config, hostSource string, deadline time.Time) error {
	bounded, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	info, err := c.run(bounded, cfg)
	if err != nil {
		return err
	}
	probe := readyProbe{
		docker: c.docker, logger: c.logger,
		id: c.id, name: c.name, host: c.host, port: c.port,
		hostSource: hostSource, timeout: cfg.startTimeout,
		requireAuth:    cfg.username != "",
		hasHealthCheck: declaresHealthCheck(info.State.Health.Status),
		callerEnded:    func() bool { return ctx.Err() != nil },
	}
	if err := probe.wait(bounded); err != nil {
		return err
	}
	if cfg.replicaSet == "" {
		return nil
	}
	err = c.initiateReplicaSet(bounded)
	if bounded.Err() != nil {
		// Out of time, which says nothing about whether the image can
		// initiate a set at all.
		return probe.notReady(bounded.Err(), errNoPrimary)
	}
	return err
}

// run starts the created container and records the port it is published
// on. A replica set member that authenticates is given its key file first,
// because mongod will not start without one.
func (c *Container) run(ctx context.Context, cfg config) (dockerclient.ContainerInspect, error) {
	if cfg.needsKeyFile() {
		if err := copyKeyFile(ctx, c.docker, c.id, c.Image()); err != nil {
			return dockerclient.ContainerInspect{}, c.startError(OpCopyKeyFile, err)
		}
	}
	c.logger.Debug("Starting the MongoDB container.", "container", c.name, "image", c.Image())
	if err := c.docker.ContainerStart(ctx, c.id); err != nil {
		return dockerclient.ContainerInspect{}, c.startError(OpStart, err)
	}
	info, err := c.docker.ContainerInspect(ctx, c.id)
	if err != nil {
		return dockerclient.ContainerInspect{}, c.startError(OpInspect, err)
	}
	port, published := hostPort(info)
	if !published {
		return dockerclient.ContainerInspect{}, &UnpublishedPortError{Port: mongoPort, Name: c.name, ID: c.id}
	}
	c.port = port
	return info, nil
}

// startError reports which step of a start failed for this container.
func (c *Container) startError(op string, err error) error {
	return &StartError{Op: op, Name: c.name, Image: c.Image(), Err: err}
}

// removeAfterFailedStart removes the container a failed start left behind.
// The caller's context may be what failed, so the removal gets its own
// deadline instead.
func (c *Container) removeAfterFailedStart(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	if err := c.Stop(ctx); err != nil {
		c.logger.Error("A container failed to start and then could not be removed, so it may still be running. "+
			"Remove it by hand: docker rm -fv followed by the container name.",
			"container", c.name, "error", err)
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
