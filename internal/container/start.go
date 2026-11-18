package container

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/tophergopher/mongotest/dockerclient"
)

// Spec is what a start needs from an image's package. Each hook may be nil.
// A hook's error is returned as it is, or as the LastErr of a NotReadyError
// if the start deadline has passed.
type Spec struct {
	// Request is what to create. A pull may change its image.
	Request dockerclient.ContainerConfig
	// Port is the container port whose published host port is dialled,
	// such as "27017/tcp".
	Port string
	// Host is the address the port is reached at, and HostSource what
	// chose it, for NotReadyError.
	Host       string
	HostSource string
	// GeneratedName says the container's name was generated, so a creation
	// cut short may remove a container of that name.
	GeneratedName bool
	// StartTimeout bounds everything after the image is on the daemon.
	StartTimeout time.Duration

	// Pull fetches an image the daemon does not have, and returns the
	// reference to create the container from.
	Pull func(ctx context.Context, image string) (ref string, err error)
	// BeforeStart runs between create and start, with the image reference
	// the container was created from.
	BeforeStart func(ctx context.Context, id, ref string) error
	// Ready checks once whether the container is usable at port.
	Ready func(ctx context.Context, port int) error
	// AfterReady finishes the start, under the same deadline.
	AfterReady func(ctx context.Context) error
	// Advice says what to do when the start timeout passed, given the last
	// failure the start saw.
	Advice func(lastErr error) string
}

// Start creates the container, pulling its image if the daemon does not
// have it, starts it, reads its published port and waits until it is ready.
// It returns the image reference it was created from and the host port. A
// start that fails removes the container.
func (c *Container) Start(ctx context.Context, spec Spec) (ref string, port int, err error) {
	s := &start{c: c, spec: spec, ref: spec.Request.Image}
	deadline, err := s.create(ctx)
	if err != nil {
		return s.ref, 0, err
	}
	if err := s.finish(ctx, deadline); err != nil {
		c.removeAfterFailedStart(ctx)
		return s.ref, 0, err
	}
	return s.ref, s.port, nil
}

// start is one start's progress.
type start struct {
	c    *Container
	spec Spec
	ref  string
	port int
}

// create has the daemon create the container, and reports a teardown that
// asked for the container while the daemon was at work. It returns the time
// by which the rest of the start has to finish.
func (s *start) create(ctx context.Context) (time.Time, error) {
	deadline, err := s.createOrPull(ctx)
	if err != nil {
		s.c.forget()
		return deadline, err
	}
	if !s.c.stopWanted.Load() {
		return deadline, nil
	}
	// That teardown waited for the id and removes the container. This
	// removes it only if the teardown ran out of time first.
	s.c.removeAfterFailedStart(ctx)
	return deadline, s.c.removedError(nil)
}

// createOrPull creates the container, pulling the image only if the daemon
// does not have it, which keeps the usual case to one call.
//
// A pull has its own timeout, and the start timeout begins again after one.
func (s *start) createOrPull(ctx context.Context) (time.Time, error) {
	request := s.spec.Request
	deadline, err := s.createWithin(ctx, request)
	if !dockerclient.IsNotFound(err) || s.spec.Pull == nil {
		return deadline, err
	}
	pulled, err := s.spec.Pull(ctx, request.Image)
	if err != nil {
		return deadline, err
	}
	request.Image = pulled
	s.ref = pulled
	return s.createWithin(ctx, request)
}

// createWithin creates the container inside the start timeout, and returns
// the time the timeout ends. The timeout begins here and covers every later
// call to the daemon, so a daemon that stops answering cannot hold a caller
// who set no deadline.
func (s *start) createWithin(ctx context.Context, request dockerclient.ContainerConfig) (time.Time, error) {
	deadline := time.Now().Add(s.spec.StartTimeout)
	bounded, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	err := s.c.create(bounded, request)
	if err == nil {
		return deadline, nil
	}
	if bounded.Err() != nil {
		s.removeUnconfirmed(ctx)
	}
	failed := &StartError{Op: OpCreate, Name: s.c.name, Image: request.Image, Err: err}
	return deadline, s.unlessCutShort(ctx, bounded, failed)
}

// removeUnconfirmed removes, by name, a container whose creation was cut
// short: the daemon may have created it all the same, and its id never
// arrived. Only a generated name is used, because a name the caller chose
// may belong to a container that is someone else's.
func (s *start) removeUnconfirmed(ctx context.Context) {
	if !s.spec.GeneratedName {
		return
	}
	// The context that was cut short cannot carry the request, and
	// removeRef gives one with no deadline its own.
	if err := s.c.removeRef(context.WithoutCancel(ctx), s.c.name); err != nil {
		s.c.logger.Error("Creating a container was cut short, and it could not then be removed, so it may exist. "+
			"Remove it by hand: docker rm -fv "+s.c.name,
			"container", s.c.name, "error", err)
	}
}

// unlessCutShort returns a step's error as it is, or inside a NotReadyError
// if bounded, which carries the start timeout, had ended: the step did not
// fail so much as run out of time, and the remedy is the timeout's.
func (s *start) unlessCutShort(caller, bounded context.Context, err error) error {
	if bounded.Err() == nil {
		return err
	}
	return s.notReady(caller, bounded.Err(), err)
}

// notReady builds the error for a start that ran out of time or was
// cancelled, at whatever step it had reached. caller is the context passed
// to Start, which tells a start its caller ended from one that timed out.
func (s *start) notReady(caller context.Context, cause, lastErr error) error {
	err := &NotReadyError{
		Name: s.c.name, ID: s.c.id, Host: s.spec.Host, Port: s.port, HostSource: s.spec.HostSource,
		Timeout: s.spec.StartTimeout, ContextEnded: caller.Err() != nil, Cause: cause, LastErr: lastErr,
	}
	if s.spec.Advice != nil {
		err.advice = s.spec.Advice(lastErr)
	}
	return err
}

// finish runs the created container and returns once it is usable.
// Everything here shares the one deadline: starting the container, the wait
// for readiness, and AfterReady.
func (s *start) finish(ctx context.Context, deadline time.Time) error {
	bounded, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	info, err := s.run(bounded)
	if err != nil {
		return s.unlessCutShort(ctx, bounded, err)
	}
	if err := s.wait(ctx, bounded, declaresHealthCheck(info.State.Health.Status)); err != nil {
		return err
	}
	if s.spec.AfterReady == nil {
		return nil
	}
	// A step that finished just as the time ran out still finished.
	if err := s.spec.AfterReady(bounded); err != nil {
		return s.unlessCutShort(ctx, bounded, err)
	}
	return nil
}

// run starts the created container and records the port it is published
// on.
func (s *start) run(ctx context.Context) (dockerclient.ContainerInspect, error) {
	if s.spec.BeforeStart != nil {
		if err := s.spec.BeforeStart(ctx, s.c.id, s.ref); err != nil {
			return dockerclient.ContainerInspect{}, err
		}
	}
	s.c.logger.Debug("Starting the container.", "container", s.c.name, "image", s.ref)
	if err := s.c.docker.ContainerStart(ctx, s.c.id); err != nil {
		return dockerclient.ContainerInspect{}, s.stepError(OpStart, err)
	}
	info, err := s.c.docker.ContainerInspect(ctx, s.c.id)
	if err != nil {
		return dockerclient.ContainerInspect{}, s.stepError(OpInspect, err)
	}
	// A server that rejects its flags can exit before this inspect. Report
	// the exit here: a stopped container publishes no port, so the port
	// check below would blame the wrong thing.
	if hasExited(info.State) {
		return dockerclient.ContainerInspect{}, s.c.exitedError(ctx, info.State, nil)
	}
	port, published := hostPort(info, s.spec.Port)
	if !published {
		return dockerclient.ContainerInspect{}, &UnpublishedPortError{Port: s.spec.Port, Name: s.c.name, ID: s.c.id}
	}
	s.port = port
	return info, nil
}

// stepError reports which step of the start failed.
func (s *start) stepError(op string, err error) error {
	return &StartError{Op: op, Name: s.c.name, Image: s.ref, Err: err}
}

// hostPort returns the host port a container port is published on, and
// whether the daemon published it at all.
func hostPort(info dockerclient.ContainerInspect, containerPort string) (int, bool) {
	port, err := strconv.Atoi(info.HostPort(containerPort))
	if err != nil || port <= 0 {
		return 0, false
	}
	return port, true
}

// How often the wait asks whether the container is ready: soon at first,
// since most containers are, then less often.
const (
	firstPollInterval = 25 * time.Millisecond
	maxPollInterval   = 500 * time.Millisecond
)

// wait polls until the container is ready, bounded ends, or the container
// can no longer become ready. bounded carries the start timeout; caller is
// the context passed to Start.
func (s *start) wait(caller, bounded context.Context, hasHealthCheck bool) error {
	interval := firstPollInterval
	timer := time.NewTimer(interval)
	defer timer.Stop()

	var lastErr error
	for {
		err := s.check(bounded, hasHealthCheck)
		if err == nil || errors.Is(err, ErrExited) {
			return err
		}
		// A check cut short by the timeout says nothing new. Any other
		// replaces what the one before it found.
		if bounded.Err() == nil {
			lastErr = err
		}

		select {
		case <-bounded.Done():
			return s.notReady(caller, bounded.Err(), lastErr)
		case <-timer.C:
			interval = min(interval*2, maxPollInterval)
			timer.Reset(interval)
		}
	}
}

// check makes one attempt: the image package's readiness check, then the
// image's own health check if it declares one. A failure is checked against
// the container's state, since a container that has gone will never be
// ready.
func (s *start) check(ctx context.Context, hasHealthCheck bool) error {
	err := s.ready(ctx)
	if err == nil && hasHealthCheck {
		err = s.healthy(ctx)
	}
	if err == nil {
		return nil
	}
	if exited := s.exited(ctx, err); exited != nil {
		return exited
	}
	return err
}

// ready runs the image package's readiness check, if it has one.
func (s *start) ready(ctx context.Context) error {
	if s.spec.Ready == nil {
		return nil
	}
	return s.spec.Ready(ctx, s.port)
}

// Why the health check did not pass, inside the NotReadyError a caller sees.
var (
	errNotHealthy    = errors.New("the image's own health check has not passed")
	errHealthUnknown = errors.New("the daemon could not report the container's health check")
)

// healthy reports whether the health check the image declares has passed.
func (s *start) healthy(ctx context.Context) error {
	info, err := s.c.docker.ContainerInspect(ctx, s.c.id)
	if err != nil {
		return fmt.Errorf("%w: %w", errHealthUnknown, err)
	}
	if info.State.Health.Status != healthStatusHealthy {
		return errNotHealthy
	}
	return nil
}

// healthStatusHealthy is what the daemon reports once a health check passes.
const healthStatusHealthy = "healthy"

// declaresHealthCheck reports whether a container's image has a health
// check, from the status the daemon gives a container that has none.
func declaresHealthCheck(status string) bool {
	return status != "" && status != "none"
}

// exited returns an error if the container can never become ready because
// it has exited or been removed, and nil if it may still start.
func (s *start) exited(ctx context.Context, cause error) error {
	info, err := s.c.docker.ContainerInspect(ctx, s.c.id)
	if dockerclient.IsNotFound(err) {
		return s.c.removedError(cause)
	}
	if err != nil {
		return nil // nothing is known, so keep waiting
	}
	if hasExited(info.State) {
		return s.c.exitedError(ctx, info.State, cause)
	}
	return nil
}
