package mongod_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/dbtest/mongod"
)

// BenchmarkStart times Start against a real Docker daemon, once for each
// option that changes how long a start takes. The image is pulled first, and
// the time Stop takes is left out. Like the integration tests, it fails
// rather than skips without a daemon.
func BenchmarkStart(b *testing.B) {
	cases := []struct {
		name string
		opts mongod.Options
	}{
		{name: "Default", opts: mongod.DefaultOptions()},
		{name: "WithAuth", opts: mongod.DefaultOptions().WithAuth("admin", "s3cret")},
		{name: "WithReplicaSet", opts: mongod.DefaultOptions().WithReplicaSet("rs0")},
		{name: "WithReplicaSetAndAuth", opts: mongod.DefaultOptions().WithReplicaSet("rs0").WithAuth("admin", "s3cret")},
		{name: "ImageAtlasLocal", opts: mongod.DefaultOptions().WithMongoImage(mongod.ImageAtlasLocal)},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			docker, pullCtx := liveDocker(b)
			key, value := runLabel(b)
			opts := tc.opts.WithDocker(docker).WithLabel(key, value)
			require.NoError(b, mongod.PullImage(pullCtx, opts), "the image is on the daemon before the clock starts")
			for b.Loop() {
				startThenStop(b, opts)
			}
			if ids, checked := containersLabelled(b, key, value); checked {
				assert.Empty(b, ids, "every container the benchmark started is removed")
			}
		})
	}
}

// startThenStop starts a container and removes it with the timer paused.
func startThenStop(b *testing.B, opts mongod.Options) {
	b.Helper()
	c, err := mongod.StartWithOptions(b.Context(), opts)
	require.NoError(b, err, "the container starts")
	b.StopTimer()
	defer b.StartTimer()
	require.NoError(b, c.Stop(b.Context()), "the container is removed before the next start")
}
