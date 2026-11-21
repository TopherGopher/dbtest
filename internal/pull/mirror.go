package pull

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/tophergopher/dbtest/dockerclient"
)

// A Mirror returns another registry's name for an image, if it has one. A
// pull that the image's own registry cannot serve tries each mirror's name in
// turn.
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

// sources returns where an image can be pulled from, in the order to try:
// the image itself, then each mirror's name for it.
func sources(image string, mirrors []Mirror) []string {
	list := []string{image}
	for _, mirror := range mirrors {
		if name, ok := mirror(image); ok && !slices.Contains(list, name) {
			list = append(list, name)
		}
	}
	return list
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

// SourcesError is a pull that failed at every source it tried: the image
// itself and its mirrors, in that order. It matches each failure, so it
// matches dockerclient.ErrRateLimited only when a rate limit was among them.
type SourcesError struct {
	Image    string          // the reference asked for
	Failures []SourceFailure // one for each source tried, in order
}

// SourceFailure is why one source could not provide an image.
type SourceFailure struct {
	Ref string // the reference pulled from that source
	Err error
}

func (e *SourcesError) Error() string {
	parts := make([]string, len(e.Failures))
	for i, f := range e.Failures {
		parts[i] = f.Ref + ": " + daemonMessage(f.Err)
	}
	return fmt.Sprintf("no registry could provide %s (%s)", e.Image, strings.Join(parts, "; "))
}

func (e *SourcesError) Unwrap() []error {
	errs := make([]error, len(e.Failures))
	for i, f := range e.Failures {
		errs[i] = f.Err
	}
	return errs
}

// failedAt returns the error for a pull that failed at the sources in
// failures: the one failure as it is when only one source was tried, and a
// *SourcesError when more were.
func failedAt(image string, failures []SourceFailure) error {
	switch len(failures) {
	case 0:
		return nil
	case 1:
		return failures[0].Err
	}
	return &SourcesError{Image: image, Failures: failures}
}

// daemonMessage returns what the daemon said about a failed pull, without
// the advice a client adds to it, which is about the image asked for rather
// than one source of it.
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
