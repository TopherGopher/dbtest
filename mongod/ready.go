package mongod

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"time"

	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/internal/container"
)

// Readiness is polled. The interval doubles from the first to the maximum,
// so a quick start is noticed at once and a slow one costs few requests.
const (
	firstPollInterval = 25 * time.Millisecond
	maxPollInterval   = 500 * time.Millisecond
)

// readyProbe waits for a started container to be usable.
type readyProbe struct {
	docker dockerclient.Client
	logger dockerclient.Logger
	id     string
	name   string
	// host and port are the address a caller will connect to.
	host string
	port int
	// requireAuth says the container was created with credentials, so the
	// mongod that serves must enforce them.
	requireAuth bool
	// tlsConfig is set when mongod requires TLS, and trusts its certificate.
	tlsConfig *tls.Config
	// hasHealthCheck says the image declares a health check to wait for.
	hasHealthCheck bool
	// notReady builds the error for a wait that ran out of time or was
	// cancelled, from the context error and the last failure the wait saw.
	notReady func(cause, lastErr error) error
}

// wait polls until the container is ready, ctx ends, or the container can no
// longer become ready. ctx carries the start timeout.
func (p readyProbe) wait(ctx context.Context) error {
	interval := firstPollInterval
	timer := time.NewTimer(interval)
	defer timer.Stop()

	var lastErr error
	for {
		ready, err := p.check(ctx)
		if ready || isFinal(err) {
			return err
		}
		// A check cut short by the timeout says nothing new. Any other
		// replaces what the one before it found.
		if ctx.Err() == nil {
			lastErr = err
		}

		select {
		case <-ctx.Done():
			return p.notReady(ctx.Err(), lastErr)
		case <-timer.C:
			interval = min(interval*2, maxPollInterval)
			timer.Reset(interval)
		}
	}
}

// check makes one attempt. Asking mongod shows that the published address
// reaches it, that it is serving, and that it enforces any credentials the
// container was created with. The image's health check, when it has one,
// shows that an image which restarts mongod after creating the user has
// finished starting.
func (p readyProbe) check(ctx context.Context) (ready bool, err error) {
	if err := p.ask(ctx); err != nil {
		return false, p.unlessExited(ctx, err)
	}
	if err := p.healthy(ctx); err != nil {
		return false, err
	}
	p.logger.Debug("mongod answered a command at the address tests will connect to, so it is ready.",
		"container", p.name, "address", addr(p.host, p.port))
	return true, nil
}

// Why a check could not say mongod was ready, inside the NotReadyError a
// caller sees. None of them puts the address in doubt.
var (
	errNotHealthy    = errors.New("the image's own health check has not passed")
	errHealthUnknown = errors.New("the daemon could not report the container's health check")
)

// healthy waits on the health check an image declares, and passes at once
// for an image that declares none.
func (p readyProbe) healthy(ctx context.Context) error {
	if !p.hasHealthCheck {
		return nil
	}
	info, err := p.docker.ContainerInspect(ctx, p.id)
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

// unlessExited returns err, or the container's exit when nothing answering
// was because the container has died rather than because it is starting.
func (p readyProbe) unlessExited(ctx context.Context, err error) error {
	if !errors.Is(err, errNoReply) {
		return err // something answered, so the container is running
	}
	if exited := p.exitedError(ctx, err); exited != nil {
		return exited
	}
	return err
}

// isFinal reports whether waiting longer cannot help. A mongod that lets
// anyone in is not final: it may be an image's set-up mongod, which a
// runtime may let the published port reach, about to be replaced.
func isFinal(err error) bool {
	return errors.Is(err, ErrContainerExited)
}

// exitedError returns an error if the container can never become ready
// because it has exited or been removed, and nil if it may still start.
func (p readyProbe) exitedError(ctx context.Context, cause error) error {
	info, err := p.docker.ContainerInspect(ctx, p.id)
	if dockerclient.IsNotFound(err) {
		return &ContainerExitedError{Name: p.name, ID: p.id, Status: "removed", Err: cause}
	}
	if err != nil {
		return nil // nothing is known, so keep waiting
	}
	if container.HasExited(info.State) {
		return container.NewExitedError(ctx, p.docker, p.name, p.id, info.State, cause)
	}
	return nil
}
