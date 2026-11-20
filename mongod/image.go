package mongod

import (
	"strings"

	"github.com/tophergopher/dbtest/dockerclient"
)

// The registries, repositories and versions the known images are built from.
const (
	// registryDockerHub is the name Docker Hub's registry goes by in an image
	// reference. Docker assumes it for a name with no registry.
	registryDockerHub = "docker.io"
	// officialNamespace is where Docker Hub keeps its official images, and
	// what Docker puts before a one-word name such as "mongo".
	officialNamespace = "library/"
	// registryPublicECR mirrors the Docker official images without Docker
	// Hub's pull rate limits.
	registryPublicECR = "public.ecr.aws"

	// repositoryOfficial is the Docker official MongoDB image.
	repositoryOfficial = officialNamespace + "mongo"
	// repositoryPublicECRMirror is the official image on public ECR.
	repositoryPublicECRMirror = "docker/library/mongo"
	// repositoryCommunity is MongoDB's own build of the community server.
	repositoryCommunity = "mongodb/mongodb-community-server"
	// repositoryEnterprise is MongoDB's enterprise server, which needs a
	// licence beyond evaluation.
	repositoryEnterprise = "mongodb/mongodb-enterprise-server"
	// repositoryAtlasLocal is Atlas Local: mongod with Atlas Search.
	repositoryAtlasLocal = "mongodb/mongodb-atlas-local"

	// defaultVersion is the default tag for a repository that publishes plain
	// version tags. It names a minor version so that every image family
	// defaults to the same server.
	defaultVersion = "8.0"
	// defaultVersionUBI is the default for MongoDB's own builds, whose tags
	// all name an OS variant.
	defaultVersionUBI = "8.0-ubi9"
)

// The known images. Each names no version, so a start runs the image's
// default. Derive from one with its With methods, which return a copy; do
// not assign to their fields.
//
//	mongod.ImageCommunity.WithVersion("8.0-ubi8")
var (
	// ImageDockerHub is the Docker official image, and the default.
	ImageDockerHub = MongoImage{Registry: registryDockerHub, Repository: repositoryOfficial}
	// ImagePublicECR is the same image from public ECR.
	ImagePublicECR = MongoImage{Registry: registryPublicECR, Repository: repositoryPublicECRMirror}
	// ImageCommunity is MongoDB's community server. It has no bare version
	// tags: "8" does not exist, "8.0-ubi9" does.
	ImageCommunity = MongoImage{Registry: registryDockerHub, Repository: repositoryCommunity}
	// ImageEnterprise is MongoDB's enterprise server, tagged like community.
	ImageEnterprise = MongoImage{Registry: registryDockerHub, Repository: repositoryEnterprise}
	// ImageAtlasLocal is Atlas Local, for Atlas Search and Vector Search.
	//
	// It runs its own single-node replica set and takes no mongod flags, so
	// WithReplicaSet and WithMongodArgs are refused for it.
	ImageAtlasLocal = MongoImage{Registry: registryDockerHub, Repository: repositoryAtlasLocal}
)

// MongoImage is an image in three parts, so one can change without restating
// the others. Any part may be empty; a start fills it in.
//
//	mongod.ImageCommunity.WithVersion("8.0-ubi8")                        // from a known image
//	mongod.MustParseMongoImage("public.ecr.aws/docker/library/mongo:8")  // from a constant
//	mongod.ParseMongoImage(os.Getenv("IMAGE"))                           // from input
type MongoImage struct {
	// Registry is the host to pull from. Docker Hub is "docker.io", and is
	// assumed when this is empty.
	Registry string
	// Repository is the path within the registry, such as "library/mongo".
	Repository string
	// Version is a tag such as "8.0", or a digest such as "sha256:...".
	Version string
}

// MustParseMongoImage is ParseMongoImage for a reference written in the
// source, and panics if it is invalid. Use ParseMongoImage for input.
func MustParseMongoImage(ref string) MongoImage {
	img, err := ParseMongoImage(ref)
	if err != nil {
		panic(err)
	}
	return img
}

// WithRegistry returns a copy pulled from a different registry.
func (m MongoImage) WithRegistry(registry string) MongoImage {
	m.Registry = registry
	return m
}

// WithRepository returns a copy with a different repository.
func (m MongoImage) WithRepository(repository string) MongoImage {
	m.Repository = repository
	return m
}

// WithVersion returns a copy at a different tag or digest.
func (m MongoImage) WithVersion(version string) MongoImage {
	m.Version = version
	return m
}

// Reference renders the image the way docker spells it.
//
//	docker.io/library/mongo:8.0
//	public.ecr.aws/docker/library/mongo:8.0
//	docker.io/library/mongo@sha256:9f2b5c8e...   // a digest is joined with @
func (m MongoImage) Reference() string {
	name := m.Repository
	if m.Registry != "" {
		name = m.Registry + "/" + m.Repository
	}
	switch {
	case name == "":
		return m.Version // only a version so far, as WithImage("8.0") leaves it
	case m.Version == "":
		return name
	case m.pinnedByDigest():
		return name + "@" + m.Version
	default:
		return name + ":" + m.Version
	}
}

// String is Reference, so an image prints as one.
func (m MongoImage) String() string { return m.Reference() }

// pinnedByDigest reports whether Version is a digest rather than a tag. A
// digest has a colon after its algorithm, which a tag cannot contain.
func (m MongoImage) pinnedByDigest() bool { return strings.Contains(m.Version, ":") }

// ReplicaSetPreconfigured reports whether the image runs a replica set of
// its own, as Atlas Local does. Its name has to be read from the server.
func (m MongoImage) ReplicaSetPreconfigured() bool { return m.is(repositoryAtlasLocal) }

// acceptsMongodArgs reports whether a container command reaches mongod as
// flags. It does not in Atlas Local, where it would replace the program.
func (m MongoImage) acceptsMongodArgs() bool { return !m.is(repositoryAtlasLocal) }

// hasBareVersionTags reports whether the repository publishes tags that are
// only a version, such as "8.0". MongoDB's own builds do not: every tag
// there names an OS variant.
func (m MongoImage) hasBareVersionTags() bool {
	return !m.is(repositoryCommunity) && !m.is(repositoryEnterprise)
}

// defaultTag returns the version this repository gets when none is given.
func (m MongoImage) defaultTag() string {
	if m.hasBareVersionTags() {
		return defaultVersion
	}
	return defaultVersionUBI
}

// is reports whether this is a known image: its repository is the known
// one, or ends in it after a slash, which is how a registry proxy serves it.
func (m MongoImage) is(repository string) bool {
	return m.Repository == repository || strings.HasSuffix(m.Repository, "/"+repository)
}

// ParseMongoImage splits a reference into its parts. A name with no registry
// is Docker Hub's and is spelled out in full; a version left out stays
// empty, for a start to fill in.
//
//	"8.0-ubi9"                               only a version
//	"mongo"                                  docker.io/library/mongo
//	"mongo:8.0"                              docker.io/library/mongo:8.0
//	"mongodb/mongodb-atlas-local"            docker.io/mongodb/mongodb-atlas-local
//	"public.ecr.aws/docker/library/mongo"    a named registry is left alone
//	"localhost:5000/team/mongo:8"            a registry with a port
//	"mongo@sha256:..."                       pinned by digest
//
// A lone word is a version if it starts with a digit or is "latest".
func ParseMongoImage(ref string) (MongoImage, error) {
	return parseImage("ParseMongoImage", ref)
}

// parseImage is ParseMongoImage for a reference that an option or a variable
// gave. source names it, for the error a refused reference gets.
func parseImage(source, ref string) (MongoImage, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return MongoImage{}, ErrEmptyImage
	}
	image, reason := splitReference(ref)
	if reason != nil {
		return MongoImage{}, &imageRefError{source: source, ref: ref, reason: reason}
	}
	return image, nil
}

// splitReference splits a reference into its parts, or says why it is not
// one. The reason is mongod's own: the Docker client's is written for a
// reference in full, and blames the case of one that is wrong another way.
func splitReference(ref string) (MongoImage, error) {
	switch {
	case strings.Contains(ref, "://"):
		return MongoImage{}, errImageHasScheme
	case isLoneVersion(ref):
		return loneVersion(ref)
	}
	parsed, err := dockerclient.ParseImageRef(ref)
	if err != nil {
		return MongoImage{}, errNotAnImage
	}
	registry, repository := splitRegistry(parsed.Name)
	image := MongoImage{Registry: registry, Repository: repository, Version: versionOf(ref, parsed)}.qualified()
	// Checked again in full: capitals are allowed in a registry's name only,
	// and a first element such as "Team" has turned out not to be one.
	if _, err := dockerclient.ParseImageRef(image.Reference()); err != nil {
		return MongoImage{}, errNotAnImage
	}
	return image, nil
}

// loneVersion returns an image that is only a version, if docker could use
// the version as a tag.
func loneVersion(version string) (MongoImage, error) {
	if _, err := dockerclient.ParseImageRef(repositoryOfficial + ":" + version); err != nil {
		return MongoImage{}, errNotAVersion
	}
	return MongoImage{Version: version}, nil
}

// qualified spells out what Docker assumes: Docker Hub for a name with no
// registry, and "library/" before the one-word name of an official image.
func (m MongoImage) qualified() MongoImage {
	if m.Repository == "" {
		return m
	}
	if m.Registry == "" {
		m.Registry = registryDockerHub
	}
	if m.Registry == registryDockerHub && !strings.Contains(m.Repository, "/") {
		m.Repository = officialNamespace + m.Repository
	}
	return m
}

// isLoneVersion reports whether a reference is a version and nothing else.
func isLoneVersion(ref string) bool {
	if strings.ContainsAny(ref, "/:@") {
		return false
	}
	return ref == "latest" || isDigit(rune(ref[0]))
}

// versionOf returns the digest or tag a reference spells out, or "" if it
// names neither. ParseImageRef fills in "latest" for a missing tag, which
// would hide that the caller left the version open.
func versionOf(ref string, parsed dockerclient.ImageRef) string {
	if parsed.Digest != "" {
		return parsed.Digest
	}
	// A colon before the last slash is a registry port, not a tag.
	lastElement := ref[strings.LastIndex(ref, "/")+1:]
	if strings.Contains(lastElement, ":") {
		return parsed.Tag
	}
	return ""
}

// splitRegistry separates a registry host from the repository. By docker's
// rule the first element is a host if it has a dot or a colon, or is
// "localhost".
func splitRegistry(name string) (registry, repository string) {
	first, rest, found := strings.Cut(name, "/")
	if !found {
		return "", name
	}
	if strings.ContainsAny(first, ".:") || first == "localhost" {
		return first, rest
	}
	return "", name
}
