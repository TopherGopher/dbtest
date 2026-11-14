package mongod

import (
	"context"
	"strconv"
	"sync"

	"github.com/tophergopher/mongotest/dockerapi"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/internal/reaper"
)

// Start creates a MongoDB container, starts it, and waits until mongod is
// reachable. If a step fails, the container is removed and the error names
// the step.
//
// With no options it runs mongo:8.0 on a host port the daemon chooses and
// waits up to a minute.
func Start(ctx context.Context, opts ...Option) (*Container, error) {
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
	if err := c.create(ctx, cfg, host); err != nil {
		return nil, err
	}
	if err := c.start(ctx, cfg, address.Source); err != nil {
		c.removeAfterFailedStart(ctx)
		return nil, err
	}
	// The logged URI hides the password: logs are shared more widely than
	// access to the daemon.
	c.logger.Info("mongod is running", "container", c.name, "id", shortID(c.id), "uri", c.uri(redactedPassword))
	return c, nil
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
// Every removal is attempted even if one fails. The error matches ErrReap
// and names each container that is still there.
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
// asked for the container while the daemon was at work.
func (c *Container) create(ctx context.Context, cfg config, host string) error {
	if err := c.createOrPull(ctx, cfg, host); err != nil {
		c.forget()
		return err
	}
	if !c.stopWanted.Load() {
		return nil
	}
	// That teardown waited for the id and removes the container. This
	// removes it only if the teardown ran out of time first.
	c.removeAfterFailedStart(ctx)
	return &ContainerExitedError{Name: c.name, ID: c.id, Status: "removed"}
}

// createOrPull creates the container, pulling the image only if the daemon
// does not have it, which keeps the usual case to one call.
func (c *Container) createOrPull(ctx context.Context, cfg config, host string) error {
	image := cfg.image.Reference()
	request := cfg.containerConfig(host)

	warnings, err := c.createOnce(ctx, request)
	if dockerclient.IsNotFound(err) {
		c.logger.Debug("image is not present locally, pulling it", "image", image)
		if err := c.docker.ImagePull(ctx, image); err != nil {
			return &StartError{Op: OpPullImage, Name: c.name, Image: image, Err: err}
		}
		warnings, err = c.createOnce(ctx, request)
	}
	if err != nil {
		return &StartError{Op: OpCreate, Name: c.name, Image: image, Err: err}
	}
	for _, warning := range warnings {
		c.logger.Warn("the daemon warned about the container it created", "container", c.name, "warning", warning)
	}
	return nil
}

// createOnce asks the daemon for the container and records its id, holding
// the container's lock until the daemon has answered. A teardown that
// arrives meanwhile waits for the id: asked sooner, the daemon might not
// know the container yet, and the teardown would leave it behind.
//
// The lock is not held across a pull, so a container torn down in between
// is not created.
func (c *Container) createOnce(ctx context.Context, request dockerclient.ContainerConfig) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return nil, &ContainerExitedError{Name: c.name, Status: "removed"}
	}
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
// The start timeout covers everything after the container is running: the
// wait for mongod and the replica set's initiation share it.
func (c *Container) start(ctx context.Context, cfg config, hostSource string) error {
	info, err := c.run(ctx, cfg)
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
	bounded, cancel := context.WithTimeout(ctx, cfg.startTimeout)
	defer cancel()
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
	c.logger.Debug("starting container", "container", c.name, "image", c.Image())
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
		c.logger.Error("cannot remove the container a failed start created", "container", c.name, "error", err)
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
