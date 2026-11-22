package exporter_test

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tophergopher/dbtest/mongod"
	"github.com/tophergopher/dbtest/mongotest/exporter"
)

// The exporter writes to any io.Writer, so you choose where the output
// goes: os.Create for a file, and gzip.NewWriter over it to compress. Here no
// server answers at the connection string, so the export fails.
func ExampleExporter_WriteJSON() {
	const uri = "mongodb://127.0.0.1:1/?serverSelectionTimeoutMS=1000"
	compressed := gzip.NewWriter(io.Discard)

	_, err := exporter.New(uri, "app", "people").
		Query(`{"active": true}`).
		WriteJSON(context.Background(), compressed, exporter.JSONOptions{Array: true})
	// gzip writes its last bytes on Close, so check its error too.
	err = errors.Join(err, compressed.Close())

	fmt.Println(errors.Is(err, exporter.ErrExport))
	// Output: true
}

// A test can write a collection to a file, choosing the documents and their
// order. It is compiled but not run, because it needs a server.
func ExampleExporter_WriteJSON_file() {
	var t *testing.T        // the test's own t
	var c *mongod.Container // a container from mongod.Start --8<-- [start:file]
	f, err := os.Create("testdata/people.json")
	require.NoError(t, err)
	defer f.Close()

	n, err := exporter.New(c.URI(), "app", "people").
		Query(`{"city": "Gotham"}`).
		Sort(`{"name": 1}`).
		Limit(100).
		WriteJSON(t.Context(), f, exporter.JSONOptions{Canonical: true})
	require.NoError(t, err)
	t.Logf("wrote %d documents", n)
} // --8<-- [end:file]

// A gzip writer over the file compresses the export. It is compiled but not
// run, because it needs a server.
func ExampleExporter_WriteJSON_gzip() {
	var t *testing.T        // the test's own t
	var c *mongod.Container // a container from mongod.Start --8<-- [start:gzip]
	f, err := os.CreateTemp("", "people-*.json.gz")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Remove(f.Name()) })

	compressed := gzip.NewWriter(f)
	_, err = exporter.New(c.URI(), "app", "people").WriteJSON(t.Context(), compressed, exporter.JSONOptions{})
	require.NoError(t, err)
	require.NoError(t, compressed.Close())
	require.NoError(t, f.Close())
} // --8<-- [end:gzip]
