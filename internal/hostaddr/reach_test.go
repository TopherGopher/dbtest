package hostaddr

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/dbtest/dockerclient"
	"github.com/tophergopher/dbtest/dockermock"
)

// resolvedTo is a resolver that finds host, chosen by the caller or not.
func resolvedTo(host string, chosen bool) func(string, string) (Address, error) {
	return func(string, string) (Address, error) {
		return Address{Host: host, Source: "the test's resolver", Chosen: chosen}, nil
	}
}

// unresolved is a resolver that cannot work out the address.
func unresolved(string, string) (Address, error) {
	return Address{}, &UnresolvedError{Err: errNoDefaultRoute}
}

// debugLog keeps the attributes of every debug line.
type debugLog struct {
	mu    sync.Mutex
	lines []map[string]any
}

func (l *debugLog) Debug(_ string, kv ...any) {
	attrs := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		key, _ := kv[i].(string)
		attrs[key] = kv[i+1]
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, attrs)
}
func (l *debugLog) Info(string, ...any)  {}
func (l *debugLog) Warn(string, ...any)  {}
func (l *debugLog) Error(string, ...any) {}

// with returns the debug lines that carry a key.
func (l *debugLog) with(key string) []map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	var found []map[string]any
	for _, line := range l.lines {
		if _, ok := line[key]; ok {
			found = append(found, line)
		}
	}
	return found
}

// input is a ReachInput with the given resolver and network lookup.
func input(resolve func(string, string) (Address, error), network string, findErr error) (ReachInput, *bool, *debugLog) {
	looked := false
	logger := &debugLog{}
	return ReachInput{
		Name: "dbtest-test", Timeout: time.Second, Logger: logger, Resolve: resolve,
		Find: func(context.Context, dockerclient.Client) (string, error) {
			looked = true
			return network, findErr
		},
	}, &looked, logger
}

// The choice between a published port and a shared network decides where
// every image package dials, so each row is a case a caller can be in.
func TestHowAContainerIsReached(t *testing.T) {
	docker := &dockermock.Mock{}
	ctx := context.Background()

	t.Run("an address the caller chose is published", func(t *testing.T) {
		in, looked, logger := input(resolvedTo("10.0.0.5", true), "dbtest-net", nil)
		r, err := Decide(ctx, docker, in)
		require.NoError(t, err, "a chosen address always works out")
		assert.Equal(t, Reach{Host: "10.0.0.5", Source: "the test's resolver"}, r, "the port is published at the chosen address")
		assert.False(t, *looked, "no shared network is looked for")
		assert.Len(t, logger.with("option"), 1, "a debug line says what kept the port published")
	})
	t.Run("an option that needs a published port is honoured", func(t *testing.T) {
		in, looked, logger := input(resolvedTo("127.0.0.1", false), "dbtest-net", nil)
		in.PublishFor = "WithPort"
		r, err := Decide(ctx, docker, in)
		require.NoError(t, err, "the address works out")
		assert.False(t, r.Shared(), "the port is published")
		assert.Equal(t, "127.0.0.1", r.Host, "at the resolved address")
		assert.False(t, *looked, "no shared network is looked for")
		require.Len(t, logger.with("option"), 1, "a debug line names the option")
		assert.Equal(t, "WithPort", logger.with("option")[0]["option"], "by its name")
	})
	t.Run("a shared network is joined", func(t *testing.T) {
		in, _, _ := input(resolvedTo("172.17.0.1", false), "dbtest-net", nil)
		r, err := Decide(ctx, docker, in)
		require.NoError(t, err, "a shared network works out")
		assert.True(t, r.Shared(), "nothing is published")
		assert.Equal(t, "dbtest-net", r.Network, "the container joins the network")
		assert.Empty(t, r.Host, "its address there is known only once it starts")
		assert.Equal(t, "172.17.0.1", r.PublishHost, "the address a published port would have, for advice")
	})
	t.Run("a shared network is joined even where no address works out", func(t *testing.T) {
		in, _, _ := input(unresolved, "dbtest-net", nil)
		r, err := Decide(ctx, docker, in)
		require.NoError(t, err, "an internal network has no default route, and needs none")
		assert.True(t, r.Shared(), "the network is joined")
		assert.Empty(t, r.PublishHost, "and no published address can be suggested")
	})
	t.Run("without a shared network the port is published", func(t *testing.T) {
		in, _, logger := input(resolvedTo("127.0.0.1", false), "", ErrNoSharedNetwork)
		r, err := Decide(ctx, docker, in)
		require.NoError(t, err, "the address works out")
		assert.False(t, r.Shared(), "the port is published")
		assert.Equal(t, "127.0.0.1", r.Host, "at the resolved address")
		require.Len(t, logger.with("reason"), 1, "a debug line says why no network was shared")
		reason, _ := logger.with("reason")[0]["reason"].(error)
		assert.ErrorIs(t, reason, ErrNoSharedNetwork, "with the lookup's error")
	})
	t.Run("without a shared network or an address the start cannot go on", func(t *testing.T) {
		in, _, _ := input(unresolved, "", ErrNoSharedNetwork)
		_, err := Decide(ctx, docker, in)
		assert.ErrorIs(t, err, ErrUnresolved, "the caller is told to set the address")
	})
}

// A lookup that hangs must not hold a caller who set no deadline: the start
// timeout has not begun when it runs.
func TestTheNetworkLookupIsBoundedByTheTimeout(t *testing.T) {
	in, _, _ := input(resolvedTo("127.0.0.1", false), "", nil)
	in.Timeout = 20 * time.Millisecond
	in.Find = func(ctx context.Context, _ dockerclient.Client) (string, error) {
		select {
		case <-ctx.Done():
			return "", errors.Join(ErrNoSharedNetwork, ctx.Err())
		case <-time.After(5 * time.Second):
			return "", ErrNoSharedNetwork
		}
	}
	began := time.Now()
	r, err := Decide(context.Background(), &dockermock.Mock{}, in)
	require.NoError(t, err, "a lookup that gives up means publishing")
	assert.False(t, r.Shared(), "the port is published")
	assert.Less(t, time.Since(began), 2*time.Second, "the lookup gave up at the timeout, not on its own")
}

// Who else can connect decides whether an image needs credentials.
func TestWhoElseCanReachTheContainer(t *testing.T) {
	assert.False(t, Reach{Host: "127.0.0.1"}.Exposed(), "a port published on loopback is this machine's alone")
	assert.True(t, Reach{Host: "build-host"}.Exposed(), "a port published on every interface is not")
	assert.True(t, Reach{Network: "dbtest-net", Host: "127.0.0.1"}.Exposed(), "nor is a container on a shared network")
}

// The create request must match the decision: join the network, or publish
// the port on the interface the address to dial needs.
func TestTheHostConfigFollowsTheReach(t *testing.T) {
	shared := Reach{Network: "dbtest-net"}.HostConfig("5432/tcp", 0)
	assert.Equal(t, "dbtest-net", shared.NetworkMode, "a shared network is joined")
	assert.Empty(t, shared.PortBindings, "and nothing is published")

	local := Reach{Host: "127.0.0.1"}.HostConfig("5432/tcp", 0)
	assert.Empty(t, local.NetworkMode, "a published container joins no network of ours")
	assert.Equal(t, []dockerclient.PortBinding{{HostIP: Loopback, HostPort: ""}}, local.PortBindings["5432/tcp"],
		"a local daemon publishes on loopback, on a port the daemon chooses")

	remote := Reach{Host: "build-host"}.HostConfig("5432/tcp", 15432)
	assert.Equal(t, []dockerclient.PortBinding{{HostIP: AllInterfaces, HostPort: "15432"}}, remote.PortBindings["5432/tcp"],
		"an address elsewhere needs every interface, and a fixed port is kept")
}
