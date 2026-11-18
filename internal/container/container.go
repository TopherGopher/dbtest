package container

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/internal/reaper"
)

// CleanupTimeout bounds a removal whose caller set no deadline, or whose
// context has already ended.
const CleanupTimeout = 30 * time.Second

// Container is a container this module creates and removes. It knows
// nothing about the image it runs.
//
// Every method is safe to call from several goroutines.
type Container struct {
	name   string
	docker dockerclient.Client
	logger dockerclient.Logger

	// mu guards id, stopped and reaperHandle. Stop can be called from a
	// defer, a test cleanup and the reaper at once, and holds mu while it
	// waits for the daemon; see lockContext.
	mu           sync.Mutex
	id           string
	stopped      bool
	reaperHandle reaper.Handle
	// stopWanted says a teardown has asked for the container. A creation
	// reads it to learn that one arrived while the daemon was at work.
	stopWanted atomic.Bool
}

// New describes a container that is about to be created, and registers it
// for removal should the process be signalled.
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

// ID returns the id the daemon assigned, or "" before Create succeeds.
// Only the goroutine that called Create may read it while Create can run.
func (c *Container) ID() string { return c.id }

// Name returns the container's name.
func (c *Container) Name() string { return c.name }

// StopWanted reports whether a teardown has asked for the container, so a
// creation can learn that one arrived while the daemon was at work.
func (c *Container) StopWanted() bool { return c.stopWanted.Load() }

// Create asks the daemon for the container and records its id, holding the
// container's lock until the daemon has answered. A teardown that arrives
// meanwhile waits for the id: asked sooner, the daemon might not know the
// container yet, and the teardown would leave it behind.
//
// ctx should carry a deadline, because the wait under the lock has to end.
// A container already stopped is not created.
func (c *Container) Create(ctx context.Context, request dockerclient.ContainerConfig) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return &ExitedError{Name: c.name, Status: "removed"}
	}
	id, warnings, err := c.docker.ContainerCreate(ctx, c.name, request)
	if err != nil {
		return err
	}
	c.id = id
	for _, warning := range warnings {
		c.logger.Warn("The Docker daemon created the container, but warned about it.", "container", c.name, "warning", warning)
	}
	return nil
}

// Forget drops a container the daemon never created, so that nothing tries
// to remove it.
func (c *Container) Forget() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopped = true
	reaper.Unregister(c.reaperHandle)
}

// Stop removes the container and its anonymous volumes. It is safe to call
// twice, and a container that is already gone counts as stopped. If the
// daemon refuses, or ctx ends while Stop is waiting, Stop returns the error
// and can be called again.
//
// A context that has already ended still removes the container, so it can
// be called from a test's cleanup with the test's context.
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
	return context.WithTimeout(context.WithoutCancel(ctx), CleanupTimeout)
}

// stop is Stop for a caller whose context is the limit, which is what the
// reaper needs: a process dying of a signal must not wait past its deadline.
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

// remove has the daemon remove the container. One the daemon was never
// asked for is removed already.
func (c *Container) remove(ctx context.Context) error {
	if c.id == "" {
		return nil
	}
	return c.RemoveByName(ctx, c.id)
}

// RemoveByName has the daemon remove a container, named by its id or its
// name, and its anonymous volumes. One the daemon does not have is removed
// already. A creation that was cut short uses it, since the daemon may have
// created the container all the same and its id never arrived.
func (c *Container) RemoveByName(ctx context.Context, idOrName string) error {
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
// never answers must not be able to hold it for ever.
func withRemovalDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, bounded := ctx.Deadline(); bounded {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, CleanupTimeout)
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
