package mongod

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/tophergopher/dbtest/dockerapi"
	"github.com/tophergopher/dbtest/dockerclient"
	"github.com/tophergopher/dbtest/internal/container"
	"github.com/tophergopher/dbtest/internal/hostaddr"
	"github.com/tophergopher/dbtest/internal/reaper"
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
	r, err := cfg.reach(ctx, docker)
	if err != nil {
		return nil, err
	}
	// Who else can connect decides whether the container needs credentials.
	if err := cfg.checkVolumeCredentials(r); err != nil {
		return nil, err
	}
	cfg = cfg.withCredentialsIfExposed(r)
	cfg.hostSource, cfg.network, cfg.publishHost = r.Source, r.Network, r.PublishHost
	cfg.announceExposure(r)

	certs, err := cfg.certificatesFor(r.Host)
	if err != nil {
		return nil, err
	}
	c := newContainer(r.Host, cfg, docker, certs)
	reached, err := c.ctr.Start(ctx, c.spec(cfg, r))
	if err != nil {
		return nil, err
	}
	c.host, c.port = reached.Host, reached.Port
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

// ReapRunningContainers removes every container this module started in this
// process that has not been stopped, PostgreSQL's from the postgres package
// included. Call it after m.Run in TestMain so a run leaves nothing behind. After [DisableSignalHandler], it is the only
// thing that removes a container nobody stopped.
//
// Every removal is attempted, all at the same time, even if one fails. The
// error matches [ErrReap], and is a [ReapError] with an entry for each
// container that is still there:
//
//	var re *mongod.ReapError
//	if errors.As(err, &re) {
//		for _, failed := range re.Failures {
//			log.Printf("remove %s by hand: %v", failed.Name, failed.Err)
//		}
//	}
func ReapRunningContainers(ctx context.Context) error { return reaper.Reap(ctx) }

// DisableSignalHandler stops this module from removing containers on
// SIGINT or SIGTERM and then ending the process, which would skip a
// program's own shutdown. Call it before the first Start, such as at the top
// of main. It applies to every container this module starts, PostgreSQL's
// included, for the rest of the process, so call [ReapRunningContainers]
// from your own shutdown or they are left running.
func DisableSignalHandler() { reaper.Disable() }

// SetMaxConcurrentStarts sets how many containers this process may be
// starting at once, in total rather than per CPU, and returns the previous
// limit. The limit covers every container this module starts, PostgreSQL's
// included. A value below 1 restores the default, 8 per CPU.
//
// A start counts against the limit until it returns, so the limit caps how
// many containers are starting, not how many are running. A start beyond
// the limit waits for another to return. The wait counts against the
// context passed to the start, not against the start timeout.
//
// Call it before the first start, for example in TestMain. Starts already
// running or waiting when it is called keep the old limit. A test that
// needs a different limit briefly can put the old one back with
// defer SetMaxConcurrentStarts(SetMaxConcurrentStarts(n)).
func SetMaxConcurrentStarts(n int) int { return container.SetMaxConcurrentStarts(n) }

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
		image: cfg.image, ref: cfg.image.Reference(), replicaSet: cfg.replicaSet, volume: cfg.volume,
		username: cfg.username, password: cfg.password,
		host: host, certs: certs, docker: docker, logger: cfg.logger,
	}
}

// startError reports which step of a start failed for this container.
func (c *Container) startError(op string, err error) error {
	return &StartError{Op: op, Name: c.Name(), Image: c.Image(), Err: err}
}

// spec fills in the generic start for mongod.
func (c *Container) spec(cfg config, r hostaddr.Reach) container.Spec {
	return container.Spec{
		Request: cfg.containerConfig(r), Port: mongoPort, Network: r.Network, NetworkPort: cfg.portOnNetwork(),
		Host: c.host, HostSource: cfg.hostSource, GeneratedName: cfg.generatedName,
		StartTimeout: cfg.startTimeout,
		Pull:         c.pullFor(cfg),
		BeforeStart:  c.beforeStart(cfg),
		Ready:        c.ready(cfg),
		AfterReady:   c.afterReady(cfg),
		BeforeRemove: c.shutdownBeforeRemove(cfg),
		ExitAdvice:   cfg.exitAdvice(),
		Advice:       cfg.notReadyAdvice,
	}
}

// pullFor pulls an image the daemon does not have, from a mirror if its own
// registry cannot provide it. A mirror's copy keeps the mirror's name, so
// it returns the mirror's reference, and records it as the image the
// container runs, for every later step and error.
func (c *Container) pullFor(cfg config) func(context.Context, string) (string, error) {
	return func(ctx context.Context, image string) (string, error) {
		c.logger.Info(pullingMessage, "image", image)
		pulled, err := pullShared(ctx, c.docker, image, cfg.pullTimeout, true)
		if err != nil {
			return "", &StartError{Op: OpPullImage, Name: c.Name(), Image: image, Err: err}
		}
		announceMirror(c.logger, image, pulled)
		c.ref = pulled
		return pulled, nil
	}
}

// beforeStart copies in the files mongod needs at start: the key file, which
// a replica set with credentials requires, and the TLS certificate if there
// is one.
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

// ready asks mongod, where the caller will connect, whether it is serving
// and whether it enforces the credentials the container was created with.
func (c *Container) ready(cfg config) func(context.Context, string, int, func(context.Context) error) error {
	return func(ctx context.Context, host string, port int, whileOpen func(context.Context) error) error {
		probe := readyProbe{
			name: c.Name(), id: c.ID(), host: host, port: port,
			requireAuth: cfg.username != "",
			tlsConfig:   c.probeTLSConfig(),
		}
		return probe.ask(ctx, whileOpen)
	}
}

// afterReady checks that the root user can log in to a kept volume, and
// initiates the replica set, if there is one. If the start timeout ends
// during initiation, it returns errNoPrimary, not the initiation's error,
// which would wrongly suggest the image cannot initiate a set.
func (c *Container) afterReady(cfg config) func(context.Context) error {
	checkLogin := cfg.volume != "" && cfg.username != ""
	if !checkLogin && cfg.replicaSet == "" {
		return nil
	}
	return func(ctx context.Context) error {
		if checkLogin {
			if err := c.checkVolumeLogin(ctx, cfg.volume); err != nil {
				return err
			}
		}
		if cfg.replicaSet == "" {
			return nil
		}
		err := c.initiateReplicaSet(ctx)
		if err != nil && ctx.Err() != nil {
			return errNoPrimary
		}
		return err
	}
}

// checkVolumeLogin logs in as the root user. The readiness check only sees
// that mongod demands a login, and a volume keeps the root user of its
// first start, so other credentials would otherwise fail at the caller's
// first command. An image with no shell cannot be checked.
func (c *Container) checkVolumeLogin(ctx context.Context, volume string) error {
	result, err := c.runShell(ctx, "db.runCommand({connectionStatus: 1}).ok")
	switch {
	case errors.Is(err, ErrNoShell):
		return nil
	case err != nil:
		return c.startError(OpCheckLogin, err)
	case result.ExitCode != 0:
		return c.startError(OpCheckLogin, &VolumeCredentialsChangedError{Volume: volume, Err: result.failure()})
	}
	return nil
}

// exitAdvice is what to try when mongod exits during a start on a volume:
// mongod will not open a data directory another mongod holds.
func (c config) exitAdvice() string {
	if c.volume == "" {
		return ""
	}
	return "If another container is using volume " + c.volume + ", give containers that run at the same time their own volume names"
}

// shutdownTimeout bounds the clean shutdown before a container on a kept
// volume is removed, so a mongod that cannot answer holds up the removal
// only briefly.
const shutdownTimeout = 10 * time.Second

// shutdownBeforeRemove shuts mongod down cleanly before a container on a
// kept volume is removed. Removing it kills mongod, and a standalone mongod
// loses the writes of about the last tenth of a second: acknowledged, but
// not yet in its journal. mongod --shutdown signals the process named in
// the data directory's lock file, so it needs no shell or credentials.
func (c *Container) shutdownBeforeRemove(cfg config) func(context.Context) {
	if cfg.volume == "" {
		return nil
	}
	return func(ctx context.Context) {
		ctx, cancel := context.WithTimeout(ctx, shutdownTimeout)
		defer cancel()
		result, execErr := c.docker.Exec(ctx, c.ID(), "mongod", "--shutdown", "--dbpath", dataDir)
		if c.exited(ctx) {
			return
		}
		c.logger.Warn("mongod could not be shut down cleanly before the container was removed, so the volume may lack the last writes.",
			"container", c.Name(), "volume", cfg.volume, "exitCode", result.ExitCode, "error", execErr)
	}
}

// exitGrace is how long the daemon is given, once the shutdown command has
// returned, to report that the container has exited.
const exitGrace = time.Second

// exited reports whether the container's process has ended, asking until
// exitGrace has passed. When mongod is the container's first process,
// Docker kills the exec that shut it down, so the exec's own result says
// nothing about the shutdown, and the exec can return before the daemon
// marks the container exited.
func (c *Container) exited(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, exitGrace)
	defer cancel()
	for {
		if processEnded(c.docker.ContainerInspect(ctx, c.ID())) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// processEnded reports whether an inspect shows the container's process has
// ended, or the container is gone.
func processEnded(info dockerclient.ContainerInspect, err error) bool {
	switch {
	case dockerclient.IsNotFound(err):
		return true
	case err != nil:
		return false
	}
	return info.State.Status == "exited" || info.State.Status == "dead"
}
