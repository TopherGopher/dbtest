package postgres

import (
	"strings"

	"github.com/tophergopher/dbtest/dockerclient"
)

// officialRepository is the official image, which a lone version names.
const officialRepository = "docker.io/library/postgres"

// parseImage checks a reference and returns the image it names: the
// official image at that tag for a lone version, or the reference as
// written. source names the option or variable that gave it, for the error.
func parseImage(source, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", ErrEmptyImage
	}
	image := ref
	if isLoneVersion(ref) {
		image = officialRepository + ":" + ref
	}
	if _, err := dockerclient.ParseImageRef(image); err != nil {
		return "", invalidImage(source, ref)
	}
	return image, nil
}

// isLoneVersion reports whether a reference is a tag and nothing else: one
// word that starts with a digit.
func isLoneVersion(ref string) bool {
	return !strings.ContainsAny(ref, "/:@") && ref[0] >= '0' && ref[0] <= '9'
}
