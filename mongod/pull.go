package mongod

import (
	"context"
	"errors"
	"time"

	"github.com/tophergopher/dbtest/dockerclient"
	"github.com/tophergopher/dbtest/internal/pull"
)

// mongoDBImages finds MongoDB's own images, such as the community server,
// on quay.io, where MongoDB publishes the same images as on Docker Hub.
var mongoDBImages = pull.HubMirror("mongodb", "quay.io")

// pullShared pulls image through the shared pulls, from its mirrors in turn
// if its registry cannot serve it, and returns the reference it was pulled
// as. MongoDB's images try quay.io before mirror.gcr.io, which caches any
// Docker Hub image.
// inStart says the pull is part of Start rather than PullImage, which
// decides what a timed-out pull advises.
func pullShared(ctx context.Context, docker dockerclient.Client, image string, timeout time.Duration, inStart bool) (string, error) {
	ref, err := pull.Shared(ctx, docker, image, timeout, pull.OfficialImages, mongoDBImages, pull.DockerHubCache)
	var timedOut *pull.TimeoutError
	if errors.As(err, &timedOut) {
		return "", &pullTimeoutError{err: timedOut, inStart: inStart}
	}
	var everyFailed *pull.SourcesError
	if errors.As(err, &everyFailed) {
		return "", &sourcesFailedError{err: err, image: image}
	}
	return ref, err
}

// announceMirror logs that an image is used under a mirror's name, which a
// caller reading the log would otherwise not expect.
func announceMirror(logger dockerclient.Logger, image, pulled string) {
	if pulled != image {
		logger.Info("Using a mirror's copy of the image, because its own registry could not provide it.", "image", image, "mirror", pulled)
	}
}

// sourcesFailedError is a pull that failed at the image's registry and at
// every mirror, with what a caller of this package can do about it. It
// matches each failure, so it matches dockerclient.ErrRateLimited when a
// rate limit was among them.
type sourcesFailedError struct {
	err   error
	image string
}

func (e *sourcesFailedError) Error() string {
	return e.err.Error() + ". Pull it with a Docker CLI that is logged in to Docker Hub (docker login, then docker pull " +
		e.image + "), or set " + EnvImage + " to an image on a registry this host can reach"
}

func (e *sourcesFailedError) Unwrap() error { return e.err }
