package container_test

import (
	"context"
	"errors"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tophergopher/dbtest/dockerclient"
	"github.com/tophergopher/dbtest/dockermock"
	"github.com/tophergopher/dbtest/internal/container"
)

// These test the start's contract with an image's package, which no image
// package can reach on its own because each sets the hooks it needs.

const (
	containerID   = "c0ffee"
	containerPort = "5432/tcp"
	hostPort      = 40000
)

// inspected is what the daemon reports for the container: running, its
// port published, and the given health status.
func inspected(health string) dockerclient.ContainerInspect {
	return dockerclient.ContainerInspect{
		ID: containerID,
		State: dockerclient.ContainerState{Status: "running", Running: true,
			Health: dockerclient.ContainerHealth{Status: health}},
		NetworkSettings: dockerclient.NetworkSettings{Ports: map[string][]dockerclient.PortBinding{
			containerPort: {{HostIP: "127.0.0.1", HostPort: strconv.Itoa(hostPort)}},
		}},
	}
}

// daemon is a mocked daemon that creates the container and reports it as
// inspect says.
func daemon(inspect func(calls int) (dockerclient.ContainerInspect, error)) *dockermock.Mock {
	var calls atomic.Int32
	return &dockermock.Mock{
		ContainerCreateFunc: func(context.Context, string, dockerclient.ContainerConfig) (string, []string, error) {
			return containerID, nil, nil
		},
		ContainerInspectFunc: func(context.Context, string) (dockerclient.ContainerInspect, error) {
			return inspect(int(calls.Add(1)))
		},
	}
}

// spec is a start with no hooks at all.
func spec(timeout time.Duration) container.Spec {
	return container.Spec{
		Request: dockerclient.ContainerConfig{Image: "postgres:17"}, Port: containerPort,
		Host: "127.0.0.1", StartTimeout: timeout,
	}
}

func newContainer(m *dockermock.Mock) *container.Container {
	return container.New("pg-test", m, dockerclient.NopLogger())
}

func TestAStartNeedsNoHooks(t *testing.T) {
	m := daemon(func(int) (dockerclient.ContainerInspect, error) { return inspected(""), nil })

	reached, err := newContainer(m).Start(t.Context(), spec(time.Second))

	require.NoError(t, err, "with no hooks the container is ready once it runs and its port is published")
	assert.Equal(t, container.Endpoint{Host: "127.0.0.1", Port: hostPort}, reached, "the start returns the host and the published port")
}

// onNetwork is what the daemon reports for a running container on network,
// with nothing published.
func onNetwork(network, address string) dockerclient.ContainerInspect {
	return dockerclient.ContainerInspect{
		ID:    containerID,
		State: dockerclient.ContainerState{Status: "running", Running: true},
		NetworkSettings: dockerclient.NetworkSettings{Networks: map[string]dockerclient.NetworkEndpoint{
			network: {IPAddress: address},
		}},
	}
}

func TestAContainerOnASharedNetworkIsReachedAtItsAddressThere(t *testing.T) {
	m := daemon(func(int) (dockerclient.ContainerInspect, error) { return onNetwork("ci-net", "172.18.0.5"), nil })
	s := spec(time.Second)
	s.Network, s.Host = "ci-net", ""
	var asked container.Endpoint
	s.Ready = func(_ context.Context, host string, port int) error {
		asked = container.Endpoint{Host: host, Port: port}
		return nil
	}

	reached, err := newContainer(m).Start(t.Context(), s)

	require.NoError(t, err, "a container on the shared network needs no published port")
	want := container.Endpoint{Host: "172.18.0.5", Port: 5432}
	assert.Equal(t, want, reached, "the container is reached at its address on the network, on the container port")
	assert.Equal(t, want, asked, "readiness is asked where the caller will connect")
}

func TestAContainerWithNoAddressOnTheNetworkCannotBeReached(t *testing.T) {
	m := daemon(func(int) (dockerclient.ContainerInspect, error) { return onNetwork("other-net", "172.19.0.5"), nil })
	s := spec(time.Second)
	s.Network = "ci-net"

	_, err := newContainer(m).Start(t.Context(), s)

	assert.ErrorIs(t, err, container.ErrNoPublishedPort, "with no address on the network there is nothing to connect to")
}

func TestAStartWaitsForADeclaredHealthCheck(t *testing.T) {
	m := daemon(func(calls int) (dockerclient.ContainerInspect, error) {
		if calls < 4 {
			return inspected("starting"), nil
		}
		return inspected("healthy"), nil
	})

	_, err := newContainer(m).Start(t.Context(), spec(10*time.Second))

	require.NoError(t, err, "the start succeeds once the health check passes")
	assert.GreaterOrEqual(t, len(m.CallsTo("ContainerInspect")), 4, "and not before")
}

func TestAHealthCheckThatNeverPassesIsNotReady(t *testing.T) {
	m := daemon(func(int) (dockerclient.ContainerInspect, error) { return inspected("starting"), nil })

	_, err := newContainer(m).Start(t.Context(), spec(200*time.Millisecond))

	require.ErrorIs(t, err, container.ErrNotReady, "the start runs out of time")
	assert.Len(t, m.CallsTo("ContainerRemove"), 1, "and removes the container")
}

// A container that exits while its health check has not passed will never
// be ready, so the start ends without waiting out its timeout.
func TestAContainerThatExitsWhileUnhealthyEndsTheStart(t *testing.T) {
	m := daemon(func(calls int) (dockerclient.ContainerInspect, error) {
		if calls < 3 {
			return inspected("starting"), nil
		}
		return dockerclient.ContainerInspect{ID: containerID, State: dockerclient.ContainerState{Status: "exited", ExitCode: 1}}, nil
	})
	started := time.Now()

	_, err := newContainer(m).Start(t.Context(), spec(time.Minute))

	require.ErrorIs(t, err, container.ErrExited, "a container that has exited is reported as such")
	assert.Less(t, time.Since(started), 10*time.Second, "without waiting out the start timeout")
}

func TestWithNoPullAMissingImageFailsTheCreate(t *testing.T) {
	m := daemon(func(int) (dockerclient.ContainerInspect, error) { return inspected(""), nil })
	m.ContainerCreateFunc = func(context.Context, string, dockerclient.ContainerConfig) (string, []string, error) {
		return "", nil, dockerclient.ErrNotFound
	}

	_, err := newContainer(m).Start(t.Context(), spec(time.Second))

	require.ErrorIs(t, err, dockerclient.ErrNotFound, "the daemon's error is kept")
	var failed *container.StartError
	require.ErrorAs(t, err, &failed, "and says which step failed")
	assert.Equal(t, container.OpCreate, failed.Op, "creating the container")
}

// The advice an image's package gives is about the last failure the start
// saw, and ends the error a caller reads.
func TestTheAdviceIsGivenTheLastFailureAndEndsTheError(t *testing.T) {
	stillStarting := errors.New("the server is still starting")
	var givenLastErr error
	s := spec(150 * time.Millisecond)
	s.Ready = func(context.Context, string, int) error { return stillStarting }
	s.Advice = func(lastErr error) string {
		givenLastErr = lastErr
		return "Try the image package's own option."
	}
	m := daemon(func(int) (dockerclient.ContainerInspect, error) { return inspected(""), nil })

	_, err := newContainer(m).Start(t.Context(), s)

	require.ErrorIs(t, err, container.ErrNotReady, "the start runs out of time")
	assert.ErrorIs(t, givenLastErr, stillStarting, "the advice is asked about the last failure")
	assert.Contains(t, err.Error(), "Try the image package's own option.", "and the error a caller reads ends with it")
}

// A container removed between start and its first inspect, by a reap or by
// hand, is reported as removed, not as a failed step.
func TestAContainerRemovedBeforeItsFirstInspectIsReportedAsRemoved(t *testing.T) {
	m := daemon(func(int) (dockerclient.ContainerInspect, error) {
		return dockerclient.ContainerInspect{}, dockerclient.ErrNotFound
	})

	_, err := newContainer(m).Start(t.Context(), spec(time.Second))

	require.ErrorIs(t, err, container.ErrExited, "a container that is gone will never be ready")
	var removed *container.ExitedError
	require.ErrorAs(t, err, &removed, "with the details")
	assert.Equal(t, "removed", removed.Status, "it was removed")
}
