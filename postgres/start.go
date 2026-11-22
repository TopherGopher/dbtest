package postgres

import (
	"context"
	"errors"
	"maps"
	"sync"

	"github.com/tophergopher/dbtest/dockerapi"
	"github.com/tophergopher/dbtest/dockerclient"
	"github.com/tophergopher/dbtest/internal/container"
	"github.com/tophergopher/dbtest/internal/hostaddr"
	"github.com/tophergopher/dbtest/internal/pull"
	"github.com/tophergopher/dbtest/internal/reaper"
)

// Start runs PostgreSQL with the default options: the official image on a
// host port the daemon chooses, waiting up to a minute for it.
func Start(ctx context.Context) (*Container, error) {
	return StartWithOptions(ctx, DefaultOptions())
}

// StartWithOptions creates a PostgreSQL container, starts it, and waits
// until the server accepts logins. If a step fails, the container is
// removed and the error names the step.
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
	r, err := cfg.reach(ctx, docker)
	if err != nil {
		return nil, err
	}
	cfg.hostSource = r.Source
	cfg.announceExposure(r)
	c := &Container{
		ctr: container.New(cfg.name, docker, cfg.logger),
		ref: cfg.image, password: cfg.password, host: r.Host,
	}
	reached, err := c.ctr.Start(ctx, c.spec(cfg, docker, r))
	if err != nil {
		return nil, err
	}
	c.host, c.port = reached.Host, reached.Port
	cfg.logger.Info("PostgreSQL is ready. The password is hidden in the URI shown here; Container.URI() returns the real one.",
		"container", c.Name(), "id", container.ShortID(c.ID()), "uri", c.uri(redactedPassword))
	return c, nil
}

// pullingMessage is logged before a pull, at a level a first-time user sees:
// a pull can take minutes, and without it a first run looks like a hang.
const pullingMessage = "The Docker daemon does not have this image yet, so it is being pulled. That happens once and can take a few minutes."

// PullImage makes sure the daemon has the image a start with these options
// would run, pulling it only if it is missing. Call it once in TestMain, so
// the download happens before any test starts:
//
//	if err := postgres.PullImage(ctx, postgres.DefaultOptions()); err != nil {
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
	// Asked first: pulling an image the daemon already has is a trip to
	// the registry for nothing, and can run into its rate limit.
	if _, err := docker.ImageInspect(ctx, cfg.image); err == nil {
		return nil
	}
	// An inspect that failed because the caller gave up says nothing about
	// the image, so no pull is announced.
	if err := ctx.Err(); err != nil {
		return err
	}
	cfg.logger.Info(pullingMessage, "image", cfg.image)
	if _, err := pullImage(ctx, docker, cfg, false); err != nil {
		return &StartError{Op: OpPullImage, Image: cfg.image, Err: err}
	}
	return nil
}

// ReapRunningContainers removes every container this module started in this
// process that has not been stopped, MongoDB's included. Call it after m.Run
// in TestMain so a run leaves nothing behind. After [DisableSignalHandler],
// it is the only thing that removes a container nobody stopped.
//
// Every removal is attempted, even if one fails. The error matches [ErrReap],
// and is a [ReapError] with an entry for each container that is still there.
func ReapRunningContainers(ctx context.Context) error { return reaper.Reap(ctx) }

// DisableSignalHandler stops this module from removing containers on SIGINT
// or SIGTERM and then ending the process, which would skip a program's own
// shutdown. Call it before the first start. It applies to every container
// this module starts, MongoDB's included, for the rest of the process, so
// call [ReapRunningContainers] from your own shutdown.
func DisableSignalHandler() { reaper.Disable() }

// SetMaxConcurrentStarts sets how many containers this process may be
// starting at once, MongoDB's included, and returns the previous limit. A
// value below 1 restores the default, 8 per CPU. A start beyond the limit
// waits for another to return, and the wait counts against the context
// passed to the start, not against the start timeout.
func SetMaxConcurrentStarts(n int) int { return container.SetMaxConcurrentStarts(n) }

// defaultClient finds the daemon the way the docker CLI does, once for the
// process. A client keeps connections open and has no Close, so a client
// per start would leak one for every container.
var defaultClient = sync.OnceValues(dockerapi.FromEnv)

// client returns the caller's Docker client, or the shared default.
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

// reach works out how this process will connect, before anything is
// created: on a network shared with this process's container if there is
// one, or else through a published port. WithPort needs a published port.
func (c config) reach(ctx context.Context, docker dockerclient.Client) (hostaddr.Reach, error) {
	in := hostaddr.ReachInput{
		Name: c.name, HostIP: c.hostIP, Timeout: c.startTimeout, Logger: c.logger, Find: c.findNetwork,
	}
	if c.port != 0 {
		in.PublishFor = "WithPort"
	}
	return hostaddr.Decide(ctx, docker, in)
}

// announceExposure logs that more than this process can reach the server.
// The same call is loopback-only on a laptop, so the change is worth saying.
func (c config) announceExposure(r hostaddr.Reach) {
	if !r.Exposed() {
		return
	}
	attrs := []any{"container", c.name}
	if r.Shared() {
		attrs = append(attrs, "network", r.Network)
	}
	c.logger.Info("Other machines or containers can reach this server, because "+r.Where()+
		". It asks for the password that Container.URI() includes.", attrs...)
}

// portOnNetwork is the port the server is dialled at on a shared network.
func (c config) portOnNetwork() int {
	if c.networkPort != 0 {
		return c.networkPort
	}
	return 5432
}

// spec fills in the generic start for PostgreSQL.
func (c *Container) spec(cfg config, docker dockerclient.Client, r hostaddr.Reach) container.Spec {
	return container.Spec{
		Request: cfg.containerConfig(r), Port: serverPort, Network: r.Network, NetworkPort: cfg.portOnNetwork(),
		Host: r.Host, HostSource: r.Source, GeneratedName: cfg.generatedName,
		StartTimeout: cfg.startTimeout,
		Pull:         c.pullFor(cfg, docker),
		// The official image declares no health check, and the readiness
		// check needs no second look: the temporary server listens on no
		// TCP port, so whatever answers is the final one.
		Ready: func(ctx context.Context, host string, port int, _ func(context.Context) error) error {
			return probe(ctx, host, port, username, database)
		},
		Advice: cfg.notReadyAdvice,
	}
}

// containerConfig builds the create request: the image creates the
// superuser with the password it is given, and the port is published on
// the interface the address to dial needs, unless the container joins a
// shared network.
func (c config) containerConfig(r hostaddr.Reach) dockerclient.ContainerConfig {
	return dockerclient.ContainerConfig{
		Image:        c.image,
		Env:          []string{"POSTGRES_PASSWORD=" + c.password},
		Labels:       maps.Clone(c.labels),
		ExposedPorts: map[string]struct{}{serverPort: {}},
		HostConfig:   r.HostConfig(serverPort, c.port),
	}
}

// pullFor pulls an image the daemon does not have, from a mirror if its own
// registry cannot provide it, and records the mirror's name as the image
// the container runs.
func (c *Container) pullFor(cfg config, docker dockerclient.Client) func(context.Context, string) (string, error) {
	return func(ctx context.Context, image string) (string, error) {
		cfg.logger.Info(pullingMessage, "image", image)
		pulled, err := pullImage(ctx, docker, cfg, true)
		if err != nil {
			return "", &StartError{Op: OpPullImage, Name: c.Name(), Image: image, Err: err}
		}
		c.ref = pulled
		return pulled, nil
	}
}

// pullImage pulls the configured image through the shared pulls, trying
// Amazon ECR Public for an official image Docker Hub cannot serve, and
// returns the reference it was pulled as. inStart says the pull is part of
// a start, which decides what a timed-out pull advises.
func pullImage(ctx context.Context, docker dockerclient.Client, cfg config, inStart bool) (string, error) {
	ref, err := pull.Shared(ctx, docker, cfg.image, cfg.pullTimeout, pull.OfficialImages)
	var timedOut *pull.TimeoutError
	if errors.As(err, &timedOut) {
		return "", &pullTimeoutError{err: timedOut, inStart: inStart}
	}
	if err == nil && ref != cfg.image {
		cfg.logger.Info("Using a mirror's copy of the image, because its own registry could not provide it.", "image", cfg.image, "mirror", ref)
	}
	return ref, err
}
