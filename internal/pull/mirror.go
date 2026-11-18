package pull

import (
	"fmt"
	"slices"
	"strings"

	"github.com/tophergopher/mongotest/dockerclient"
)

// A Mirror returns another registry's name for an image, if it has one.
// When Docker Hub's rate limit refuses a pull, the image is pulled from
// there instead.
type Mirror func(ref string) (string, bool)

// OfficialImages finds Docker Hub's official images, such as mongo, on
// Amazon ECR Public, where Docker and AWS publish the same images.
var OfficialImages = HubMirror("library", "public.ecr.aws/docker")

// dockerHubNames are the registry names that mean Docker Hub.
var dockerHubNames = []string{"docker.io", "index.docker.io", "registry-1.docker.io"}

// HubMirror returns a Mirror for one Docker Hub namespace, which the mirror
// serves under prefix: HubMirror("mongodb", "quay.io") finds
// docker.io/mongodb/x:1 at quay.io/mongodb/x:1. The tag or digest is kept,
// so a pull by digest gets the same content from either registry.
func HubMirror(namespace, prefix string) Mirror {
	return func(ref string) (string, bool) {
		path, ok := dockerHubPath(ref)
		if !ok || !strings.HasPrefix(path, namespace+"/") {
			return "", false
		}
		return prefix + "/" + path, true
	}
}

// firstMirror returns the first of mirrors that has a name for ref, or "".
func firstMirror(mirrors []Mirror, ref string) string {
	for _, mirror := range mirrors {
		if name, ok := mirror(ref); ok {
			return name
		}
	}
	return ""
}

// dockerHubPath returns ref's repository path, tag and digest, if ref names
// an image on Docker Hub. Docker Hub's short forms are spelled out, so
// "mongo" is "library/mongo:latest".
func dockerHubPath(ref string) (string, bool) {
	parsed, err := dockerclient.ParseImageRef(ref)
	if err != nil {
		return "", false
	}
	registry, path := splitRegistry(parsed.Name)
	if !slices.Contains(dockerHubNames, registry) {
		return "", false
	}
	if !strings.Contains(path, "/") {
		path = "library/" + path
	}
	return path + versionSuffix(parsed), true
}

// splitRegistry separates the registry from a repository name. As docker
// reads a name, the first part is a registry only if it has a dot or a port,
// or is localhost; otherwise the name is on Docker Hub.
func splitRegistry(name string) (registry, path string) {
	first, rest, found := strings.Cut(name, "/")
	if found && isRegistryHost(first) {
		return first, rest
	}
	return "docker.io", name
}

// isRegistryHost reports whether the first part of a name is a registry.
func isRegistryHost(part string) bool {
	return strings.ContainsAny(part, ".:") || part == "localhost"
}

// versionSuffix renders a reference's tag and digest as they follow its name.
func versionSuffix(ref dockerclient.ImageRef) string {
	suffix := ""
	if ref.Tag != "" {
		suffix = ":" + ref.Tag
	}
	if ref.Digest != "" {
		suffix += "@" + ref.Digest
	}
	return suffix
}

// mirrorError is a pull that Docker Hub's rate limit refused and its mirror
// failed too. It matches both failures, and so dockerclient.ErrRateLimited.
type mirrorError struct {
	image, mirror     string
	limited, mirrored error
}

func (e *mirrorError) Error() string {
	return fmt.Sprintf("Docker Hub's rate limit refused %s (%v), and pulling the same image from %s failed too (%v). "+
		"Wait for the limit to reset, log the Docker daemon in to Docker Hub, or pull the image before the tests run",
		e.image, e.limited, e.mirror, e.mirrored)
}

func (e *mirrorError) Unwrap() []error { return []error{e.limited, e.mirrored} }
