package mongod_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/mongod"
)

func TestParseMongoImage(t *testing.T) {
	cases := []struct {
		ref  string
		want mongod.MongoImage
	}{
		{"8", mongod.MongoImage{Version: "8"}},
		{"8.0-ubi9", mongod.MongoImage{Version: "8.0-ubi9"}},
		{"latest", mongod.MongoImage{Version: "latest"}},
		{"mongo", mongod.MongoImage{Registry: "docker.io", Repository: "library/mongo"}},
		{"mongo:8.0", mongod.MongoImage{Registry: "docker.io", Repository: "library/mongo", Version: "8.0"}},
		{"mongodb/mongodb-atlas-local", mongod.MongoImage{Registry: "docker.io", Repository: "mongodb/mongodb-atlas-local"}},
		{"docker.io/library/mongo:8.0", mongod.MongoImage{Registry: "docker.io", Repository: "library/mongo", Version: "8.0"}},
		{"docker.io/mongo", mongod.MongoImage{Registry: "docker.io", Repository: "library/mongo"}},
		{"docker.io/mongo:8.0", mongod.MongoImage{Registry: "docker.io", Repository: "library/mongo", Version: "8.0"}},
		{"mirror.corp.example/mongo", mongod.MongoImage{Registry: "mirror.corp.example", Repository: "mongo"}},
		{"public.ecr.aws/docker/library/mongo", mongod.MongoImage{Registry: "public.ecr.aws", Repository: "docker/library/mongo"}},
		{"localhost:5000/team/mongo", mongod.MongoImage{Registry: "localhost:5000", Repository: "team/mongo"}},
		{"localhost:5000/team/mongo:8", mongod.MongoImage{Registry: "localhost:5000", Repository: "team/mongo", Version: "8"}},
		{"mongo@sha256:" + digest, mongod.MongoImage{Registry: "docker.io", Repository: "library/mongo", Version: "sha256:" + digest}},
		{"  mongo:8.0  ", mongod.MongoImage{Registry: "docker.io", Repository: "library/mongo", Version: "8.0"}},
	}
	for _, tc := range cases {
		t.Run(tc.ref, func(t *testing.T) {
			got, err := mongod.ParseMongoImage(tc.ref)

			require.NoError(t, err, "a reference docker accepts is parsed")
			assert.Equal(t, tc.want, got, "split into its parts, with Docker Hub spelled out")
		})
	}
}

// digest is made up: sixty-four hex digits are all a parser needs. It names
// no real image and nothing here pulls one, so no release can change it.
var digest = strings.Repeat("0123456789abcdef", 4)

func TestParseMongoImageRefusesWhatDockerWould(t *testing.T) {
	for _, ref := range []string{"", "   ", "Mongo:8", "Team/mongo", "mongo:8:9", "mongo:", "8.0!", "https://mirror.corp.example/mongo"} {
		t.Run(ref, func(t *testing.T) {
			_, err := mongod.ParseMongoImage(ref)

			assert.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "the mistake is caught before a daemon sees it")
		})
	}
}

func TestMustParseMongoImagePanicsOnAReferenceDockerWouldRefuse(t *testing.T) {
	assert.Panics(t, func() { mongod.MustParseMongoImage("Mongo:8") }, "for a reference written in the source")
	assert.Equal(t, "docker.io/library/mongo:8.0", mongod.MustParseMongoImage("mongo:8.0").Reference(), "and returns a good one, under its full name")
}

func TestAnImageRendersAsDockerSpellsIt(t *testing.T) {
	cases := []struct {
		image mongod.MongoImage
		want  string
	}{
		{mongod.MongoImage{Registry: "docker.io", Repository: "library/mongo", Version: "8.0"}, "docker.io/library/mongo:8.0"},
		{mongod.MongoImage{Repository: "mongo", Version: "8.0"}, "mongo:8.0"},
		{mongod.MongoImage{Registry: "public.ecr.aws", Repository: "docker/library/mongo", Version: "8.0"}, "public.ecr.aws/docker/library/mongo:8.0"},
		{mongod.MongoImage{Repository: "mongo"}, "mongo"},
		{mongod.MongoImage{Repository: "mongo", Version: "sha256:" + digest}, "mongo@sha256:" + digest},
		{mongod.MongoImage{Repository: "mongo", Version: "sha512:" + digest + digest}, "mongo@sha512:" + digest + digest},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, tc.image.Reference(), "a tag is joined with a colon and a digest with @")
		assert.Equal(t, tc.want, tc.image.String(), "and an image prints as its reference")
	}
}

func TestAnImageCanBeDerivedFromAKnownOne(t *testing.T) {
	assert.Equal(t, "mirror.corp.example/library/mongo", mongod.ImageDockerHub.WithRegistry("mirror.corp.example").Reference(),
		"WithRegistry changes where the image is pulled from")
	assert.Equal(t, "docker.io/team/mongo", mongod.ImageDockerHub.WithRepository("team/mongo").Reference(),
		"WithRepository changes which image it is")
	assert.Equal(t, "docker.io/library/mongo:7.0", mongod.ImageDockerHub.WithVersion("7.0").Reference(),
		"WithVersion names the tag")
}

// A known image stands for whatever its default version is, so it must not
// look as though the caller had named one.
func TestTheKnownImagesNameNoVersion(t *testing.T) {
	known := []mongod.MongoImage{mongod.ImageDockerHub, mongod.ImagePublicECR, mongod.ImageCommunity, mongod.ImageEnterprise, mongod.ImageAtlasLocal}
	for _, image := range known {
		assert.Empty(t, image.Version, "%s leaves the version for a start to fill in", image.Repository)
	}
}

// Whatever ParseMongoImage accepts must render as a reference that parses
// back to the same image.
func FuzzParseMongoImage(f *testing.F) {
	for _, seed := range []string{"8", "mongo", "mongo:8.0", "docker.io/mongo", "Team/mongo", "localhost:5000/team/mongo:8", "mongo@sha256:" + digest, "mongo@sha512:" + digest + digest} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, ref string) {
		parsed, err := mongod.ParseMongoImage(ref)
		if err != nil {
			return
		}
		again, err := mongod.ParseMongoImage(parsed.Reference())

		require.NoError(t, err, "%q rendered as %q, which does not parse", ref, parsed.Reference())
		assert.Equal(t, parsed, again, "%q changed when rendered and parsed again", ref)
	})
}
