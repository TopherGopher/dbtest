package exporter_test

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/tophergopher/mongotest/exporter"
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
