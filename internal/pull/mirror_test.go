package pull

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWhereAMirrorServesAnImage(t *testing.T) {
	digest := "sha256:" + strings.Repeat("b", 64)
	cases := []struct{ ref, want string }{
		{"mongo", "public.ecr.aws/docker/library/mongo:latest"},
		{"mongo:7", "public.ecr.aws/docker/library/mongo:7"},
		{"library/mongo:7", "public.ecr.aws/docker/library/mongo:7"},
		{"docker.io/mongo:7", "public.ecr.aws/docker/library/mongo:7"},
		{"docker.io/library/mongo:7", "public.ecr.aws/docker/library/mongo:7"},
		{"index.docker.io/library/mongo:7", "public.ecr.aws/docker/library/mongo:7"},
		{"mongo@" + digest, "public.ecr.aws/docker/library/mongo@" + digest},
		{"mongo:7@" + digest, "public.ecr.aws/docker/library/mongo:7@" + digest},
		{"mongodb/mongodb-community-server:8.0-ubi9", "quay.io/mongodb/mongodb-community-server:8.0-ubi9"},
		{"docker.io/mongodb/mongodb-atlas-local:8.0", "quay.io/mongodb/mongodb-atlas-local:8.0"},
	}
	mirrors := []Mirror{OfficialImages, HubMirror("mongodb", "quay.io")}
	for _, tc := range cases {
		var found []string
		for _, mirror := range mirrors {
			if name, ok := mirror(tc.ref); ok {
				found = append(found, name)
			}
		}
		assert.Equal(t, []string{tc.want}, found, "%s is the same image on one mirror, at the same tag or digest", tc.ref)
	}
}

func TestAnImageOffDockerHubOrOutsideTheNamespaceHasNoMirror(t *testing.T) {
	for _, ref := range []string{
		"bitnami/mongodb:8.0",                   // another Docker Hub namespace
		"public.ecr.aws/docker/library/mongo:7", // already a mirror
		"quay.io/mongodb/mongodb-community-server:8.0-ubi9",
		"localhost/mongo:7", // a local registry, though its name has no dot
		"registry.example.com:5000/mongo:7",
		"registry.example.com/library/mongo:7", // the namespace, on another registry
		"localhost:5000/mongodb/x:1",
		"Docker.io/library/mongo:7", // docker would take this for another registry
		"Not A Reference",
	} {
		_, ok := OfficialImages(ref)
		assert.False(t, ok, "%s is not one of Docker Hub's official images", ref)
		_, ok = HubMirror("mongodb", "quay.io")(ref)
		assert.False(t, ok, "%s is not one of MongoDB's images on Docker Hub", ref)
	}
}

// An image reference comes from the caller or the environment, so the
// mapping must take any text.
func FuzzHubMirror(f *testing.F) {
	for _, seed := range []string{
		"mongo", "mongo:7", "library/mongo", "docker.io/library/mongo:8.0", "index.docker.io/mongo",
		"mongodb/mongodb-community-server:8.0-ubi9", "mongo@sha256:" + strings.Repeat("c", 64),
		"localhost:5000/mongo", "quay.io/mongodb/x:1", "",
	} {
		f.Add(seed)
	}
	mongoDB := HubMirror("mongodb", "quay.io")
	f.Fuzz(func(t *testing.T, ref string) {
		if mirrored, ok := OfficialImages(ref); ok {
			assert.True(t, strings.HasPrefix(mirrored, "public.ecr.aws/docker/library/"), "an official image is found only on ECR Public")
			_, again := OfficialImages(mirrored)
			assert.False(t, again, "and a mirror's name is not mirrored again")
		}
		if mirrored, ok := mongoDB(ref); ok {
			assert.True(t, strings.HasPrefix(mirrored, "quay.io/mongodb/"), "a MongoDB image is found only on quay.io")
		}
	})
}
