package dbtest_test

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// navPage is a page path at the end of a nav line in mkdocs.yml, quoted or
// not.
var navPage = regexp.MustCompile(`([^\s'"]+\.md)['"]?\s*$`)

// Zensical's strict build checks links in pages but not the nav, so a page
// renamed or removed without its nav entry would publish a dead link.
func TestNavPagesExist(t *testing.T) {
	f, err := os.Open("mkdocs.yml")
	require.NoError(t, err, "the site's settings must be readable")
	defer f.Close()

	pages, err := navPages(f)
	require.NoError(t, err, "mkdocs.yml must read to the end")
	require.NotEmpty(t, pages, "the nav must list pages, or this test checks nothing")
	for _, page := range pages {
		assert.FileExists(t, filepath.Join("docs", page), "the nav must point at a page that exists")
	}
}

// navPages returns the pages the nav in mkdocs.yml lists.
func navPages(r io.Reader) ([]string, error) {
	var pages []string
	inNav := false
	lines := bufio.NewScanner(r)
	for lines.Scan() {
		line := lines.Text()
		if isTopLevelKey(line) {
			inNav = strings.HasPrefix(line, "nav:")
			continue
		}
		if m := navPage.FindStringSubmatch(line); inNav && m != nil {
			pages = append(pages, m[1])
		}
	}
	return pages, lines.Err()
}

// isTopLevelKey reports whether a line of YAML starts a key that is not
// inside another: it is not indented, a comment or a list item.
func isTopLevelKey(line string) bool {
	return line != "" && !strings.ContainsAny(line[:1], " \t#-")
}

// fence is a line that opens or closes a fenced block, at any indentation:
// three or more backticks or tildes, then what the opening line says about
// the block.
var fence = regexp.MustCompile("^\\s*(`{3,}|~{3,})\\s*(.*?)\\s*$")

// goInfo is what follows the fence on a line that opens a block of Go: go,
// or an attribute list whose first class is .go.
var goInfo = regexp.MustCompile(`^(?:go|\{\s*\.go)\b(.*)$`)

// include is a line the site replaces with Go from a file: the whole file,
// or the lines between its --8<-- [start:name] and [end:name] markers.
var include = regexp.MustCompile(`^\s*--8<-- "([^":]+)(?::([a-z][-_0-9a-z]*))?"\s*$`)

// wrapper is a line a block may hold around an include: the opening of a
// test function or TestMain, or a closing brace. Go fixes their shape, so
// they cannot drift from the code.
var wrapper = regexp.MustCompile(`^\s*(func Test\w*\((t \*testing\.T|m \*testing\.M)\) \{|\})\s*$`)

// illustration is the title a block of Go carries when it is not compiled.
const illustration = `title="Illustration, not compiled"`

// errUnclosedFence is a fenced block that is still open at the end of a page.
var errUnclosedFence = errors.New("a fenced block is never closed; close it with the fence that opened it")

// errNotCompiled is an include of a file that go test does not compile.
var errNotCompiled = errors.New("the site includes Go only from a _test.go file that go test compiles")

// errNoSection is an include of a section its file does not mark.
var errNoSection = errors.New("the file must mark where the section starts and ends")

// goBlock is a fenced block of Go code on a page.
type goBlock struct {
	where string // the page and the line the block opens on
	info  string // what follows the fence and the language on the opening line
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
					assert.NoError(t, checkInclude(m[1], m[2]), "a page must include Go that go test compiles")
					continue
				}
				assert.Regexp(t, wrapper, line, "a compiled block holds only includes and the function around them; mark it %s otherwise", illustration)
			}
			assert.NotZero(t, includes, "a block of Go must include its code from a Go file, or be marked %s", illustration)
		})
	}
}

// checkInclude checks that a Go file the site includes exists and, when a
// section is named, marks where that section starts and ends.
func checkInclude(path, section string) error {
	if !compiledByGoTest(path) {
		return fmt.Errorf("%s: %w", path, errNotCompiled)
	}
	src, err := os.ReadFile(path)
	if err != nil || section == "" {
		return err
	}
	for _, marker := range []string{"start", "end"} {
		if !strings.Contains(string(src), "--8<-- ["+marker+":"+section+"]") {
			return fmt.Errorf("%s has no [%s:%s]: %w", path, marker, section, errNoSection)
		}
	}
	return nil
}

// compiledByGoTest reports whether go test compiles the file at path: a
// _test.go file in no directory that the go command ignores.
func compiledByGoTest(path string) bool {
	if !strings.HasSuffix(path, "_test.go") {
		return false
	}
	dirs := strings.Split(filepath.ToSlash(filepath.Dir(filepath.Clean(path))), "/")
	return !slices.ContainsFunc(dirs, ignoredByGo)
}

// ignoredByGo reports whether the go command skips a directory of this name.
func ignoredByGo(dir string) bool {
	switch {
	case dir == "testdata", strings.HasPrefix(dir, "_"):
		return true
	case dir == ".":
		return false
	default:
		return strings.HasPrefix(dir, ".")
	}
}

// docsGoBlocks returns every fenced block of Go on the site's pages.
func docsGoBlocks(t *testing.T) []goBlock {
	t.Helper()
	var blocks []goBlock
	err := filepath.WalkDir("docs", func(path string, d fs.DirEntry, err error) error {
		if err != nil || !isPage(d) {
			return err
		}
		found, err := goBlocksInFile(path)
		blocks = append(blocks, found...)
		return err
	})
	require.NoError(t, err, "the pages must be readable, with every fence closed")
	return blocks
}

// isPage reports whether an entry under docs is a Markdown page.
func isPage(d fs.DirEntry) bool {
	return !d.IsDir() && filepath.Ext(d.Name()) == ".md"
}

// goBlocksInFile returns the fenced blocks of Go in one page.
func goBlocksInFile(path string) ([]goBlock, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return goBlocks(f, path)
}

// openFence is a fenced block that has been opened and not yet closed.
type openFence struct {
	marker string   // the backticks or tildes that opened it
	block  *goBlock // the block, if it is Go
}

// goBlocks returns the fenced blocks of Go in a page, naming each by the
// page and its line. Fences match as Markdown matches them, so a block quoted
// inside a longer fence is text, not Go.
func goBlocks(r io.Reader, page string) ([]goBlock, error) {
	var blocks []goBlock
	var open *openFence
	lines := bufio.NewScanner(r)
	for n := 1; lines.Scan(); n++ {
		line := lines.Text()
		switch {
		case open == nil:
			open = opening(line, fmt.Sprintf("%s:%d", page, n))
		case closes(open.marker, line):
			if open.block != nil {
				blocks = append(blocks, *open.block)
			}
			open = nil
		case open.block != nil:
			open.block.lines = append(open.block.lines, line)
		}
	}
	if err := lines.Err(); err != nil {
		return nil, err
	}
	if open != nil {
		return nil, fmt.Errorf("%s: %w", page, errUnclosedFence)
	}
	return blocks, nil
}

// opening returns the fence a line opens, or nil if it opens none.
func opening(line, where string) *openFence {
	m := fence.FindStringSubmatch(line)
	if m == nil {
		return nil
	}
	open := &openFence{marker: m[1]}
	if g := goInfo.FindStringSubmatch(m[2]); g != nil {
		open.block = &goBlock{where: where, info: g[1]}
	}
	return open
}

// closes reports whether a line closes the fence that marker opened: the
// same character, at least as many times, and nothing after it.
func closes(marker, line string) bool {
	m := fence.FindStringSubmatch(line)
	if m == nil || m[2] != "" {
		return false
	}
	return m[1][0] == marker[0] && len(m[1]) >= len(marker)
}

func TestNavPagesReadsOnlyTheNav(t *testing.T) {
	yml := "site_name: x\n" +
		"docs_dir: notes.md\n" +
		"nav:\n" +
		"  - Home: index.md\n" +
		"  - Podman: \"guides/podman.md\"\n" +
		"  - Group:\n" +
		"      - 'a/b.md'\n" +
		"extra:\n" +
		"  page: other.md\n"

	pages, err := navPages(strings.NewReader(yml))

	require.NoError(t, err, "the settings must read to the end")
	assert.Equal(t, []string{"index.md", "guides/podman.md", "a/b.md"}, pages,
		"every page under nav is checked, quoted or not, and nothing under another key")
}

func TestGoBlocksFindsEveryFenceThatRendersAsGo(t *testing.T) {
	for name, page := range map[string]string{
		"backticks":           "```go\nx\n```\n",
		"a space before go":   "``` go\nx\n```\n",
		"tildes":              "~~~go\nx\n~~~\n",
		"an attribute list":   "```{.go title=\"t\"}\nx\n```\n",
		"four backticks":      "````go\nx\n````\n",
		"indented in a tab":   "=== \"v1\"\n\n    ```go\n    x\n    ```\n",
		"after a shell block": "```sh\ngo test\n```\n\n```go\nx\n```\n",
	} {
		t.Run(name, func(t *testing.T) {
			blocks, err := goBlocks(strings.NewReader(page), "page.md")

			require.NoError(t, err, "a page whose fences all close must read")
			require.Len(t, blocks, 1, "a block that renders as Go must be checked")
			assert.Equal(t, []string{"x"}, trimmed(blocks[0].lines), "the block holds the lines inside its fences")
		})
	}
}

func TestGoBlocksSkipsGoThatAPageShowsAsMarkdown(t *testing.T) {
	page := "````markdown\n```go\nx\n```\n````\n"

	blocks, err := goBlocks(strings.NewReader(page), "page.md")

	require.NoError(t, err, "a page whose fences all close must read")
	assert.Empty(t, blocks, "a fence quoted inside a Markdown block is text, not Go")
}

func TestAnUnclosedFenceIsAnError(t *testing.T) {
	for name, page := range map[string]string{
		"go":            "```go\nx\n",
		"another kind":  "```sh\nx\n",
		"a shorter end": "````go\nx\n```\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := goBlocks(strings.NewReader(page), "page.md")

			assert.ErrorIs(t, err, errUnclosedFence, "a block left open would hide the rest of the page from the check")
		})
	}
}

func TestCheckIncludeAcceptsOnlyWhatGoTestCompiles(t *testing.T) {
	t.Chdir(t.TempDir())
	marked := "package p\n\n// --8<-- [start:s]\nvar x = 1\n\n// --8<-- [end:s]\n"
	for _, path := range []string{"p/a_test.go", "p/a.go", "p/testdata/a_test.go", "_p/a_test.go", ".p/a_test.go"} {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755), "the fixture's directory must be created")
		require.NoError(t, os.WriteFile(path, []byte(marked), 0o644), "the fixture must be written")
	}

	assert.NoError(t, checkInclude("p/a_test.go", "s"), "a marked section of a test file is includable")
	assert.NoError(t, checkInclude("p/a_test.go", ""), "and so is the whole file")
	assert.ErrorIs(t, checkInclude("p/a_test.go", "t"), errNoSection, "a section must be marked")
	assert.ErrorIs(t, checkInclude("p/gone_test.go", ""), fs.ErrNotExist, "the file must exist")
	for _, path := range []string{"p/a.go", "p/testdata/a_test.go", "_p/a_test.go", ".p/a_test.go"} {
		assert.ErrorIs(t, checkInclude(path, "s"), errNotCompiled, "%s is not compiled by go test, so its code is not checked", path)
	}
}

// trimmed returns the lines without their surrounding white space.
func trimmed(lines []string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = strings.TrimSpace(line)
	}
	return out
}
