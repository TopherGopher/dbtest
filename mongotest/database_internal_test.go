package mongotest

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// FuzzDatabaseName checks, for any test name, the two rules the server holds
// a database name to that a test name can break.
func FuzzDatabaseName(f *testing.F) {
	for _, test := range []string{
		"",
		"TestOrders",
		"TestOrders/v1.5_upgrade",
		"TestOrders/price_$10",
		`TestOrders/a_"quoted"_word`,
		"TestOrders/café_au_lait",
		strings.Repeat("TestOrders/", 20),
	} {
		f.Add(test)
	}
	f.Fuzz(func(t *testing.T, test string) {
		name := databaseName(test)

		assert.LessOrEqual(t, len(name), 63, "the server refuses a database name longer than 63 bytes")
		assert.Equal(t, -1, strings.IndexFunc(name, refusedInAName),
			"the name holds only letters, digits, _ and -, which the server accepts: %q", name)
	})
}

// refusedInAName reports whether a character is outside the plain ones a
// database name is kept to.
func refusedInAName(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return false
	}
	return r != '_' && r != '-'
}
