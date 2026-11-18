package container

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/internal/reaper"
)

// cleanupTimeout bounds a removal whose caller set no deadline, or whose
// context has already ended.
const cleanupTimeout = 30 * time.Second

// Container is a container this module creates and removes. It knows
// nothing about the image it runs.
//
// Every method except ID is safe to call from several goroutines.
type Container struct {
	name   string
	docker dockerclient.Client
	logger dockerclient.Logger

	// mu guards stopped and reaperHandle, and the writing of id, which
	// Create does once; after that id is read without the lock. Stop can be
	// called from a defer, a test cleanup and the reaper at once, and holds
	// mu while it waits for the daemon; see lockContext.
	mu           sync.Mutex
	id           string
	stopped      bool
	reaperHandle reaper.Handle
	// stopWanted says Stop has been called, by the caller or the reaper.
	// Create's caller reads it to learn that Stop came while the daemon was
	// creating the container.
	stopWanted atomic.Bool
}

// New describes a container that is about to be created, and registers it
// with the reaper, which removes it if the process is signalled.
//
// It registers before the daemon is asked, because a signal between the
// daemon creating the container and this process hearing of it would
// otherwise leave the container behind. See Create.
func New(name string, docker dockerclient.Client, logger dockerclient.Logger) *Container {
	c := &Container{name: name, docker: docker, logger: logger}
	// Locked because the reaper may call Stop, which reads the handle, the
	// moment Register returns.
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reaperHandle = reaper.Register(name, c.stop, logger)
	return c
}

// ID returns the id the daemon assigned, or "" before Create succeeds. Read
// it only after Create has returned, or from the goroutine that called Create.
func (c *Container) ID() string {
	if c == nil {
		return ""
	}
	return c.id
}

// Name returns the container's name. ID and Name return "" on a nil
// Container, so an image package's zero value still prints.
func (c *Container) Name() string {
	if c == nil {
		return ""
	}
	return c.name
}

// create asks the daemon for the container and records its id. Stop waits
// until create returns: a removal sent before the daemon knows the container
// would find nothing, and leave it behind.
//
// ctx needs a deadline, because Stop waits for create and create waits for
// the daemon. If Stop has already been called, create creates nothing and
// returns an *ExitedError with Status "removed".
func (c *Container) create(ctx context.Context, request dockerclient.ContainerConfig) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return c.removedError(nil)
	}
	id, warnings, err := c.docker.ContainerCreate(ctx, c.name, request)
	if err != nil {
		return err
	}
	c.id = id
	for _, warning := range warnings {
		c.logger.Warn("The Docker daemon created the container with a warning.", "container", c.name, "warning", warning)
	}
	return nil
}

// forget drops a container the daemon never created, so that nothing tries
// to remove it.
func (c *Container) forget() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopped = true
	reaper.Unregister(c.reaperHandle)
}

// Stop removes the container and its anonymous volumes, stopping it first if
// it runs. It is safe to call more than once, and a container that is already
// gone counts as removed. If the daemon refuses, or ctx ends while Stop is
// waiting, Stop returns the error and can be called again.
//
// If ctx has already ended, Stop still tries for up to cleanupTimeout, so a
// test's cleanup can pass the test's context.
func (c *Container) Stop(ctx context.Context) error {
	ctx, cancel := removalContext(ctx)
	defer cancel()
	return c.stop(ctx)
}

// removalContext returns ctx, or one with a deadline of its own if ctx has
// already ended. A test's context ends before its cleanups run, and a Stop
// that gave up there would leave the container running behind a green test.
func removalContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx.Err() == nil {
		return ctx, func() {}
	}
	return context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
}

// stop is Stop without the extra time for an ended context. The reaper uses
// it, because a process ending on a signal must not outlive its deadline.
func (c *Container) stop(ctx context.Context) error {
	c.stopWanted.Store(true)
	if err := lockContext(ctx, &c.mu); err != nil {
		return err
	}
	defer c.mu.Unlock()
	if c.stopped {
		return nil
	}
	if err := c.remove(ctx); err != nil {
		return err
	}
	c.stopped = true
	reaper.Unregister(c.reaperHandle)
	c.logger.Debug("Removed the container and its anonymous volumes.", "container", c.name, "id", ShortID(c.id))
	return nil
}

// remove has the daemon remove the container. If Create got no id, there is
// nothing to remove.
func (c *Container) remove(ctx context.Context) error {
	if c.id == "" {
		return nil
	}
	return c.removeRef(ctx, c.id)
}

// removeRef has the daemon remove a container, named by its id or its name,
// and its anonymous volumes. A container the daemon does not have counts as
// removed.
func (c *Container) removeRef(ctx context.Context, idOrName string) error {
	ctx, cancel := withRemovalDeadline(ctx)
	defer cancel()
	err := c.docker.ContainerRemove(ctx, idOrName, dockerclient.RemoveOptions{Force: true, RemoveVolumes: true})
	if dockerclient.IsNotFound(err) {
		return nil
	}
	return err
}

// withRemovalDeadline gives a removal a deadline when its caller set none.
// The daemon is asked while the container's lock is held, so a daemon that
// never answers must not be able to hold it forever.
func withRemovalDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, bounded := ctx.Deadline(); bounded {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, cleanupTimeout)
}

// lockPoll is how often lockContext tries again.
const lockPoll = 5 * time.Millisecond

// lockContext locks mu, or gives up when ctx ends. Stop holds its lock
// across a call to the daemon, so a second Stop, such as the reaper's on a
// signal, has to be able to stop waiting when its deadline passes.
func lockContext(ctx context.Context, mu *sync.Mutex) error {
	for !mu.TryLock() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(lockPoll):
		}
	}
	return nil
}

// removeAfterFailedStart removes the container a failed start left behind.
// The caller's context may be what failed, so the removal gets its own
// deadline instead.
func (c *Container) removeAfterFailedStart(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	if err := c.Stop(ctx); err != nil {
		c.logger.Error("A container failed to start and then could not be removed, so it may still be running. "+
			"Remove it by hand: docker rm -fv "+c.name,
			"container", c.name, "error", err)
	}
}
