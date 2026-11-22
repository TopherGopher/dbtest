package pgtest

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// plainIdentifier is what a database name keeps to: lowercase ASCII letters,
// digits and underscores, which read the same quoted or not.
var plainIdentifier = regexp.MustCompile(`^[a-z0-9_]+$`)

// FuzzDatabaseName checks, for any test name, the rules a database name is
// held to.
func FuzzDatabaseName(f *testing.F) {
	for _, test := range []string{
		"",
		"TestOrders",
		"TestOrders/v1.5_upgrade",
		`TestOrders/a_"quoted"_word`,
		"TestOrders/drop_table;--",
		"TestOrders/café_au_lait",
		strings.Repeat("TestOrders/", 20),
		"TestOrders/" + strings.Repeat("é", 40),
	} {
		f.Add(test)
	}
	f.Fuzz(func(t *testing.T, test string) {
		name := databaseName(test)

		assert.LessOrEqual(t, len(name), 63, "the server cuts a name longer than 63 bytes, so two tests could share one")
		assert.Regexp(t, plainIdentifier, name, "the name needs no care in a caller's own SQL")
	})
}

func TestDatabaseNameIsNewEachTime(t *testing.T) {
	assert.NotEqual(t, databaseName("TestOrders"), databaseName("TestOrders"),
		"two databases for one test, or for the same test in two processes, do not collide")
}
