package mongotest

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// FuzzDatabaseName checks, for any test name, the rules the server holds a
// database name to that a test name can break.
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
		assert.False(t, strings.ContainsAny(name, "/\\. \"$\x00"),
			"the server refuses a database name with any of / \\ . \" $, a space or a NUL: %q", name)
	})
}
