package postgres

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
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
