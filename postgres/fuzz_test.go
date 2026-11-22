package postgres

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/tophergopher/dbtest/dockerclient"
)

// This reads bytes from whatever answers at the container's address. It is
// inside the package because no caller can hand it bytes through Start.

func FuzzReadStartupReply(f *testing.F) {
	for _, seed := range []string{authRequest, startingUp, "", "R", "E\x00\x00\x00\x05\x00", "E\x00\x00\x00\x0eC28000\x00Mx\x00\x00", "HTTP/1.1 400"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, reply string) {
		err := readStartupReply(strings.NewReader(reply))
		if err == nil {
			assert.True(t, strings.HasPrefix(reply, "R") || strings.HasPrefix(reply, "E"),
				"only a request to authenticate, or a refusal, counts as ready")
			return
		}
		known := errors.Is(err, errNoReply) || errors.Is(err, errNotPostgres) || errors.Is(err, errStartingUp)
		assert.True(t, known, "every failure says whether anything answered, so a timeout can advise")
	})
}

// This reads what a caller passes to WithImage or sets in
// DBTEST_POSTGRES_IMAGE.
func FuzzParseImage(f *testing.F) {
	for _, seed := range []string{"", " ", "17", "18-alpine", "postgres", "pgvector/pgvector:pg18", "localhost:5000/pg@sha256:" + strings.Repeat("a", 64), "https://x", "1/2"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, ref string) {
		image, err := parseImage("WithImage", ref)
		if err != nil {
			assert.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "a refused reference is the caller's to correct")
			return
		}
		trimmed := strings.TrimSpace(ref)
		assert.Contains(t, []string{trimmed, "docker.io/library/postgres:" + trimmed}, image,
			"a reference is used as written, or a lone version names the official image")
		again, err := parseImage("WithImage", image)
		assert.NoError(t, err, "an accepted image is accepted again")
		assert.Equal(t, image, again, "and means the same")
	})
}
