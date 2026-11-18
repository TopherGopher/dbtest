package pull

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/tophergopher/mongotest/dockerclient"
)

// A Mirror returns another registry's name for an image, if it has one.
// When Docker Hub's rate limit refuses a pull, the image is pulled by that
// name instead.
type Mirror func(ref string) (string, bool)

// OfficialImages finds Docker Hub's official images, such as mongo, on
// Amazon ECR Public, where Docker and AWS publish the same images.
var OfficialImages = HubMirror("library", "public.ecr.aws/docker")

// dockerHubNames are the registry names that mean Docker Hub.
var dockerHubNames = []string{"docker.io", "index.docker.io", "registry-1.docker.io"}

// HubMirror returns a Mirror for one Docker Hub namespace, which the mirror
// serves under prefix: HubMirror("mongodb", "quay.io") finds
// docker.io/mongodb/x:1 at quay.io/mongodb/x:1. The tag or digest is kept,
// so a pull by digest gets exactly that content from the mirror, or fails.
func HubMirror(namespace, prefix string) Mirror {
	return func(ref string) (string, bool) {
		path, ok := dockerHubPath(ref)
		if !ok || !strings.HasPrefix(path, namespace+"/") {
			return "", false
		}
		return prefix + "/" + path, true
	}
}

// firstMirror returns the name the first of mirrors gives ref, or "" if
// none has one.
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

// splitRegistry separates the registry from a repository name. Docker treats
// the first part of a name as a registry only if it has a dot or a port, or
// is localhost; otherwise the name is on Docker Hub.
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

// MirrorError is a pull that Docker Hub's rate limit refused and whose
// mirror failed too. errors.Is matches either failure, so it matches
// dockerclient.ErrRateLimited.
type MirrorError struct {
	Image    string // the reference asked for, on Docker Hub
	Mirror   string // the mirror's name for it
	Limited  error  // Docker Hub's refusal
	Mirrored error  // the mirror's failure
}

func (e *MirrorError) Error() string {
	return fmt.Sprintf("Docker Hub's rate limit refused %s, and its mirror %s failed too: %s",
		e.Image, e.Mirror, daemonMessage(e.Mirrored))
}

func (e *MirrorError) Unwrap() []error { return []error{e.Limited, e.Mirrored} }

// daemonMessage returns what the daemon said about a failed pull, without
// the advice a client adds to it, which is about the image asked for rather
// than its mirror.
func daemonMessage(err error) string {
	var pull *dockerclient.PullError
	if errors.As(err, &pull) {
		return pull.Message
	}
	var status *dockerclient.StatusError
	if errors.As(err, &status) {
		return status.Message
	}
	return err.Error()
}
