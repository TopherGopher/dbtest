package exporter_test

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"

	"github.com/tophergopher/mongotest/exporter"
)

// The exporter writes to any io.Writer, so a file or gzip is a wrapper of
// the caller's choosing: os.Create, then gzip.NewWriter over it. Here the
// server cannot be reached, so the export fails.
func ExampleExporter_WriteJSON() {
	compressed := gzip.NewWriter(io.Discard)
	defer compressed.Close()

	_, err := exporter.New(unreachable, "app", "people").
		Query(`{"active": true}`).
		WriteJSON(compressed, exporter.JSONOptions{Array: true})

	fmt.Println(errors.Is(err, exporter.ErrExport))
	// Output: true
}
