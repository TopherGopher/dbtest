package mongod

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// This reads text this package does not write: the user an image declares.
// It is inside the package because it is not reachable with arbitrary bytes
// through Start.

func FuzzKeyFileOwner(f *testing.F) {
	for _, seed := range []string{"", "1001:0", "mongod", "mongodb:mongodb", "-5", "0"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, imageUser string) {
		assert.GreaterOrEqual(t, keyFileOwner(imageUser), 0, "a file cannot be owned by a negative user")
	})
}
