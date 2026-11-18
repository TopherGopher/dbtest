package mongod

import (
	"context"
	"errors"
	"time"

	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/internal/pull"
)

// mongoDBImages finds MongoDB's own images, such as the community server,
// on quay.io, where MongoDB publishes the same images as on Docker Hub.
var mongoDBImages = pull.HubMirror("mongodb", "quay.io")

// pullShared pulls image through the shared pulls, from a mirror if Docker
// Hub's rate limit refuses it, and returns the reference it was pulled as.
// inStart says the pull is part of Start rather than PullImage, which
// decides what a timed-out pull advises.
func pullShared(ctx context.Context, docker dockerclient.Client, image string, timeout time.Duration, inStart bool) (string, error) {
	ref, err := pull.Shared(ctx, docker, image, timeout, pull.OfficialImages, mongoDBImages)
	var timedOut *pull.TimeoutError
	if errors.As(err, &timedOut) {
		return "", &pullTimeoutError{err: timedOut, inStart: inStart}
	}
	var bothFailed *pull.MirrorError
	if errors.As(err, &bothFailed) {
		return "", &mirrorFailedError{err: err, image: image}
	}
	return ref, err
}

// announceMirror logs that an image is used under its mirror's name, which
// a caller reading the log would otherwise not expect.
func announceMirror(logger dockerclient.Logger, image, pulled string) {
	if pulled != image {
		logger.Info("Using the mirror's copy of the image, because Docker Hub's rate limit refused it.", "image", image, "mirror", pulled)
	}
}

// mirrorFailedError is a pull that Docker Hub's rate limit refused and whose
// mirror failed too, with what a caller of this package can do about it. It
// matches both failures, and so dockerclient.ErrRateLimited.
type mirrorFailedError struct {
	err   error
	image string
}

func (e *mirrorFailedError) Error() string {
	return e.err.Error() + ". Pull it with a Docker CLI that is logged in to Docker Hub (docker login, then docker pull " +
		e.image + "), or set " + EnvImage + " to an image on a registry this host can reach"
}

func (e *mirrorFailedError) Unwrap() error { return e.err }
