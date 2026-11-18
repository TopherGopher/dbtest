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
	return ref, err
}
