package mongod_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/dbtest/mongod"
)

// The round trip a suite with a slow data load relies on: what one container
// wrote, a new container on the same volume reads. Stop removing the first
// container must leave the volume, and a replica set must become primary
// again in a container with a new hostname.
func TestIntegrationDataOnANamedVolumeOutlivesTheContainer(t *testing.T) {
	official := mongod.DefaultOptions().WithMongoImage(mongod.ImageDockerHub)
	cases := []struct {
		name string
		opts mongod.Options
	}{
		{name: "docker official", opts: official},
		{name: "docker official with auth", opts: official.WithAuth("admin", "s3cret")},
		{name: "docker official replica set", opts: official.WithReplicaSet("rs0")},
		{name: "docker official replica set with auth", opts: official.WithReplicaSet("rs0").WithAuth("admin", "s3cret")},
		{name: "mongodb community server", opts: mongod.DefaultOptions().WithMongoImage(mongod.ImageCommunity)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			docker, ctx := liveDocker(t)
			key, value := runLabel(t)
			volume := testVolume(t)
			opts := tc.opts.WithDocker(docker).WithNamedVolume(volume).WithLabel(key, value)

			first, err := mongod.StartWithOptions(ctx, opts)
			require.NoError(t, err, "the first container starts on a new volume")
			t.Cleanup(func() { _ = first.Stop(context.Background()) })
			assert.Equal(t, volume, first.Volume(), "the container reports the volume")
			evalIn(t, ctx, docker, first, `db.getSiblingDB("kept").docs.insertOne({kept: 1}).acknowledged`)
			require.NoError(t, first.Stop(ctx), "the first container is removed")

			second, err := mongod.StartWithOptions(ctx, opts)
			require.NoError(t, err, "a second container starts on the volume the first filled")
			t.Cleanup(func() { _ = second.Stop(context.Background()) })

			assert.Equal(t, "1", evalIn(t, ctx, docker, second, `db.getSiblingDB("kept").docs.countDocuments({kept: 1})`),
				"it reads what the first container wrote")
			assert.Equal(t, "true", evalIn(t, ctx, docker, second, "db.hello().isWritablePrimary"), "and accepts writes")
		})
	}
}

// mongod will not open a data directory another mongod holds, so a second
// start fails at once instead of waiting out the timeout, and the first
// keeps serving its data.
func TestIntegrationASecondContainerCannotUseAVolumeInUse(t *testing.T) {
	docker, ctx := liveDocker(t)
	key, value := runLabel(t)
	opts := mongod.DefaultOptions().WithDocker(docker).WithNamedVolume(testVolume(t)).WithLabel(key, value)
	first, err := mongod.StartWithOptions(ctx, opts)
	require.NoError(t, err, "the first container starts")
	t.Cleanup(func() { _ = first.Stop(context.Background()) })
	evalIn(t, ctx, docker, first, `db.getSiblingDB("kept").docs.insertOne({kept: 1}).acknowledged`)

	second, err := mongod.StartWithOptions(ctx, opts)
	if err == nil {
		t.Cleanup(func() { _ = second.Stop(context.Background()) })
	}

	assert.ErrorIs(t, err, mongod.ErrContainerExited, "the second mongod exits because the volume is in use")
	assert.Equal(t, "1", evalIn(t, ctx, docker, first, `db.getSiblingDB("kept").docs.countDocuments({kept: 1})`),
		"the first container still has its data")
}

// testVolume returns a volume name no other test uses, and removes the
// volume when the test ends, after its containers. Nothing in mongod removes
// a named volume, so the docker CLI does.
func testVolume(t testing.TB) string {
	t.Helper()
	raw := make([]byte, 4)
	_, _ = rand.Read(raw)
	name := "dbtest-test-" + hex.EncodeToString(raw)
	t.Cleanup(func() {
		out, err := exec.Command("docker", "volume", "rm", "--force", name).CombinedOutput()
		if err != nil {
			t.Errorf("the test volume %s is left behind; remove it with docker volume rm %s: %v %s",
				name, name, err, strings.TrimSpace(string(out)))
		}
	})
	return name
}
