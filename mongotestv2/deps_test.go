package mongotestv2_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestImportsNoDriverV1 checks that a caller of this module builds only
// driver v2. A module that pulled in driver v1 as well would give every
// caller both.
func TestImportsNoDriverV1(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "./...").Output()
	require.NoError(t, err, "go list reports the module's dependencies")

	for _, pkg := range strings.Fields(string(out)) {
		assert.False(t, isDriverV1(pkg), "the module imports %s, from driver v1", pkg)
	}
}

// isDriverV1 reports whether an import path is a package of MongoDB's Go
// driver v1, whose paths share a prefix with driver v2's.
func isDriverV1(pkg string) bool {
	return strings.HasPrefix(pkg, "go.mongodb.org/mongo-driver/") && !strings.HasPrefix(pkg, "go.mongodb.org/mongo-driver/v2/")
}
