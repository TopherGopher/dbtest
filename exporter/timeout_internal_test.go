package exporter

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// A deadline only ever shortens the wait to reach the server: one later
// than the driver's own thirty seconds, or a connection string that sets
// its own timeout, leaves the wait as it was.
func TestADeadlineOnlyShortensTheWaitToReachTheServer(t *testing.T) {
	const uri = "mongodb://127.0.0.1:1/?directConnection=true"
	soon, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	late, cancelLate := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancelLate()

	seconds, ok := selectionTimeout(soon, uri)
	assert.True(t, ok, "a deadline sooner than the driver's default shortens the wait")
	assert.Equal(t, 2, seconds, "rounded up to whole seconds")

	_, ok = selectionTimeout(late, uri)
	assert.False(t, ok, "a later deadline does not lengthen it")

	_, ok = selectionTimeout(context.Background(), uri)
	assert.False(t, ok, "nor does a context with no deadline")

	_, ok = selectionTimeout(soon, uri+"&serverSelectionTimeoutMS=5000")
	assert.False(t, ok, "the connection string's own timeout is left alone, since mongoexport refuses both")

	_, ok = selectionTimeout(soon, "mongodb://[::1]:27017,[::1]:27018/?serverSelectionTimeoutMS=1000")
	assert.False(t, ok, "even in a connection string with several IPv6 hosts")
}
