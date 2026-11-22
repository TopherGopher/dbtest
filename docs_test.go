package dbtest_test

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// navPage is a page path at the end of a nav line in mkdocs.yml.
var navPage = regexp.MustCompile(`(\S+\.md)\s*$`)

// Zensical's strict build checks links in pages but not the nav, so a page
// renamed or removed without its nav entry would publish a dead link.
func TestNavPagesExist(t *testing.T) {
	f, err := os.Open("mkdocs.yml")
	require.NoError(t, err, "the site's settings must be readable")
	defer f.Close()

	inNav, pages := false, 0
	lines := bufio.NewScanner(f)
	for lines.Scan() {
		line := lines.Text()
		if strings.HasPrefix(line, "nav:") {
			inNav = true
			continue
		}
		m := navPage.FindStringSubmatch(line)
		if !inNav || m == nil {
			continue
		}
		pages++
		assert.FileExists(t, filepath.Join("docs", m[1]), "the nav must point at a page that exists")
	}
	require.NoError(t, lines.Err(), "mkdocs.yml must read to the end")
	assert.NotZero(t, pages, "the nav must list pages, or this test checks nothing")
}
