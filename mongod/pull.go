package mongod

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/internal/pull"
)

// pullShared pulls image through the shared pulls, and adds to the error what
// a caller of this package can do about it. inStart says the pull is part of
// Start rather than PullImage.
func pullShared(ctx context.Context, docker dockerclient.Client, image string, timeout time.Duration, inStart bool) error {
	err := pull.Shared(ctx, docker, image, timeout)
	var timedOut *pull.TimeoutError
	if errors.As(err, &timedOut) {
		return &pullTimeoutError{err: timedOut, inStart: inStart}
	}
	return adviseOnRateLimit(image, err)
}

// adviseOnRateLimit adds a way round Docker Hub's rate limit to a pull of
// the official image that hit it, since ImagePublicECR mirrors that image.
func adviseOnRateLimit(image string, err error) error {
	if errors.Is(err, dockerclient.ErrRateLimited) && isOfficialOnDockerHub(image) {
		return &publicECRAdvice{err: err}
	}
	return err
}

// isOfficialOnDockerHub reports whether ref names the official image on
// Docker Hub, at any tag or digest.
func isOfficialOnDockerHub(ref string) bool {
	rest, ok := strings.CutPrefix(ref, registryDockerHub+"/"+repositoryOfficial)
	return ok && rest != "" && strings.ContainsRune(":@", rune(rest[0]))
}

// publicECRAdvice is a rate-limited pull of the official image. It matches
// the daemon's error, and so ErrRateLimited.
type publicECRAdvice struct{ err error }

func (e *publicECRAdvice) Error() string {
	return e.err.Error() + ". To avoid Docker Hub's limit, pull the same image from Amazon ECR Public: " +
		"mongod.DefaultOptions().WithMongoImage(mongod.ImagePublicECR)"
}

func (e *publicECRAdvice) Unwrap() error { return e.err }
