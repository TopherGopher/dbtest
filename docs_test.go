package dbtest_test

import (
	"bufio"
	"fmt"
	"io/fs"
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

// goFence opens a fenced block of Go code, at any indentation.
var goFence = regexp.MustCompile("^\\s*```go\\b(.*)$")

// outerFence opens or closes a fence of four or more backticks, which a page
// uses to show Markdown, fences and all, as it is written.
var outerFence = regexp.MustCompile("^\\s*````")

// fenceEnd closes a fenced block.
var fenceEnd = regexp.MustCompile("^\\s*```\\s*$")

// include is a line the site replaces with Go from a file: the whole file,
// or the lines between its --8<-- [start:name] and [end:name] markers.
var include = regexp.MustCompile(`^\s*--8<-- "([^":]+)(?::([a-z][-_0-9a-z]*))?"\s*$`)

// wrapper is a line a block may hold around an include: the opening of a
// test function or TestMain, or a closing brace. Go fixes their shape, so
// they cannot drift from the code.
var wrapper = regexp.MustCompile(`^\s*(func Test\w*\((t \*testing\.T|m \*testing\.M)\) \{|\})\s*$`)

// illustration is the title a block of Go carries when it is not compiled.
const illustration = `title="Illustration, not compiled"`

// goBlock is a fenced block of Go code on a page.
type goBlock struct {
	where string // the page and the line the block opens on
	info  string // what follows ```go on the opening line
	lines []string
}

// A reader copies the Go a page shows, so each block is included from Go
// that the tests compile, or says plainly that it is not.
func TestDocsGoIsCompiledOrMarked(t *testing.T) {
	blocks := docsGoBlocks(t)
	require.NotEmpty(t, blocks, "the site must have Go blocks, or this test checks nothing")
	for _, b := range blocks {
		t.Run(b.where, func(t *testing.T) {
			if strings.Contains(b.info, illustration) {
				return
			}
			includes := 0
			for _, line := range b.lines {
				if m := include.FindStringSubmatch(line); m != nil {
					includes++
					assertIncludable(t, m[1], m[2])
					continue
				}
				assert.Regexp(t, wrapper, line, "a compiled block holds only includes and the function around them; mark it %s otherwise", illustration)
			}
			assert.NotZero(t, includes, "a block of Go must include its code from a Go file, or be marked %s", illustration)
		})
	}
}

// assertIncludable checks that a Go file the site includes exists and, when
// a section is named, marks where that section starts and ends.
func assertIncludable(t *testing.T, path, section string) {
	t.Helper()
	assert.True(t, strings.HasSuffix(path, ".go"), "%s: the site must include Go from a .go file", path)
	src, err := os.ReadFile(path)
	if !assert.NoError(t, err, "the file a page includes must exist") || section == "" {
		return
	}
	assert.Contains(t, string(src), "--8<-- [start:"+section+"]", "%s must mark where the section starts", path)
	assert.Contains(t, string(src), "--8<-- [end:"+section+"]", "%s must mark where the section ends", path)
}

// docsGoBlocks returns every fenced block of Go on the site's pages.
func docsGoBlocks(t *testing.T) []goBlock {
	t.Helper()
	var blocks []goBlock
	err := filepath.WalkDir("docs", func(path string, d fs.DirEntry, err error) error {
		if err != nil || !isPage(d) {
			return err
		}
		found, err := goBlocksIn(path)
		blocks = append(blocks, found...)
		return err
	})
	require.NoError(t, err, "the pages must be readable")
	return blocks
}

// isPage reports whether an entry under docs is a Markdown page.
func isPage(d fs.DirEntry) bool {
	return !d.IsDir() && filepath.Ext(d.Name()) == ".md"
}

// goBlocksIn returns the fenced blocks of Go in one page.
func goBlocksIn(path string) ([]goBlock, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var blocks []goBlock
	var open *goBlock
	quoting := false // inside a longer fence that shows Markdown as it is written
	lines := bufio.NewScanner(f)
	for n := 1; lines.Scan(); n++ {
		line := lines.Text()
		switch {
		case outerFence.MatchString(line):
			quoting = !quoting
		case quoting:
		case open != nil && fenceEnd.MatchString(line):
			blocks = append(blocks, *open)
			open = nil
		case open != nil:
			open.lines = append(open.lines, line)
		case goFence.MatchString(line):
			info := goFence.FindStringSubmatch(line)[1]
			open = &goBlock{where: fmt.Sprintf("%s:%d", path, n), info: info}
		}
	}
	return blocks, lines.Err()
}
