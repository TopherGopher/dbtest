package exporter_test

import (
	"bufio"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/dbtest/mongotest/exporter"
)

// The exporting guide on the documentation site shows the code between the
// --8<-- markers in these tests. In it, uri is the server's connection
// string.

func TestIntegrationAnExportCanGoToAFile(t *testing.T) {
	uri := seeded(t)

	// --8<-- [start:file]
	f, err := os.Create(filepath.Join(t.TempDir(), "people.json"))
	require.NoError(t, err, "the file can be created")
	defer f.Close() // in case a check below fails first

	n, err := exporter.New(uri, "app", "people").
		Query(`{"n": {"$gt": 2}}`).
		Sort(`{"name": 1}`).
		Limit(100).
		WriteJSON(t.Context(), f, exporter.JSONOptions{Canonical: true})
	require.NoError(t, err, "the export succeeds")
	require.NoError(t, f.Close(), "the file is written out")
	t.Logf("wrote %d documents", n)
	// --8<-- [end:file]

	assert.EqualValues(t, 3, n, "only the documents the query matches are counted")
	assert.Equal(t, 3, fileLines(t, f.Name()), "the file holds those documents, one to a line")
}

func TestIntegrationAGzipWriterCompressesAnExport(t *testing.T) {
	uri := seeded(t)

	// --8<-- [start:gzip]
	f, err := os.Create(filepath.Join(t.TempDir(), "people.json.gz"))
	require.NoError(t, err, "the file can be created")
	defer f.Close() // in case a check below fails first

	compressed := gzip.NewWriter(f)
	_, err = exporter.New(uri, "app", "people").WriteJSON(t.Context(), compressed, exporter.JSONOptions{})
	require.NoError(t, err, "the export succeeds")
	require.NoError(t, compressed.Close(), "gzip writes its last bytes on Close")
	require.NoError(t, f.Close(), "the file is written out")
	// --8<-- [end:gzip]

	assert.Equal(t, 5, gzippedLines(t, f.Name()), "the file holds every document, one to a line")
}

// fileLines counts the lines in a file.
func fileLines(t *testing.T, path string) int {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err, "the file can be opened")
	defer f.Close()
	return countLines(t, f)
}

// gzippedLines counts the lines in a gzip file.
func gzippedLines(t *testing.T, path string) int {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err, "the file can be opened")
	defer f.Close()
	r, err := gzip.NewReader(f)
	require.NoError(t, err, "the file is gzip")
	return countLines(t, r)
}

// countLines counts the lines r holds, and fails the test if r cannot be read
// to the end, as a gzip stream with a bad checksum cannot.
func countLines(t *testing.T, r io.Reader) int {
	t.Helper()
	lines := 0
	s := bufio.NewScanner(r)
	for s.Scan() {
		lines++
	}
	require.NoError(t, s.Err(), "the file reads to the end")
	return lines
}
