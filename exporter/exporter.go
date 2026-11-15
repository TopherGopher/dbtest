// Package exporter writes a MongoDB collection out as JSON or CSV, using
// mongoexport from MongoDB's database tools. It lives in its own module so
// that only callers who export pay for that dependency.
//
//	_, err := exporter.New(c.URI(), "app", "people").WriteJSON(file, exporter.JSONOptions{})
package exporter

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/mongodb/mongo-tools/mongoexport"
)

// ErrExport is what every failed export matches. The error says which
// collection, and wraps mongoexport's own error.
var ErrExport = errors.New("the export failed")

// Exporter is an export of one collection. Its methods return a changed
// copy, so one Exporter can be the base of several exports.
type Exporter struct {
	uri, database, collection string
	query, sort               string
	skip, limit               int64
}

// New returns an export of every document in a collection, from the server
// at uri.
func New(uri, database, collection string) Exporter {
	return Exporter{uri: uri, database: database, collection: collection}
}

// Query exports only the documents that match a filter, written as JSON,
// such as `{"age": {"$gt": 30}}`.
func (e Exporter) Query(filter string) Exporter {
	e.query = filter
	return e
}

// Sort exports the documents in this order, written as JSON, such as
// `{"age": -1}`.
func (e Exporter) Sort(order string) Exporter {
	e.sort = order
	return e
}

// Skip leaves out the first n documents.
func (e Exporter) Skip(n int64) Exporter {
	e.skip = n
	return e
}

// Limit exports at most n documents.
func (e Exporter) Limit(n int64) Exporter {
	e.limit = n
	return e
}

// JSONOptions is how WriteJSON lays out its output. The zero value writes
// one document per line in relaxed Extended JSON.
type JSONOptions struct {
	Array     bool // one JSON array instead of one document per line
	Pretty    bool // indented for a person to read
	Canonical bool // canonical Extended JSON, which keeps every BSON type
}

// WriteJSON writes the documents to w as Extended JSON, and returns how
// many it wrote.
func (e Exporter) WriteJSON(w io.Writer, opts JSONOptions) (int64, error) {
	args := []string{"--type", "json"}
	if opts.Array {
		args = append(args, "--jsonArray")
	}
	if opts.Pretty {
		args = append(args, "--pretty")
	}
	if opts.Canonical {
		args = append(args, "--jsonFormat", "canonical")
	}
	return e.export(w, args)
}

// WriteCSV writes the documents to w as CSV: a header naming the fields,
// then a row for each document. Fields may use dots, such as "address.city".
func (e Exporter) WriteCSV(w io.Writer, fields []string) (int64, error) {
	return e.export(w, []string{"--type", "csv", "--fields", strings.Join(fields, ",")})
}

// export runs mongoexport with the format's arguments and the export's own.
func (e Exporter) export(w io.Writer, format []string) (int64, error) {
	opts, err := mongoexport.ParseOptions(append(format, e.args()...), "mongotest", "")
	if err != nil {
		return 0, e.failed(err)
	}
	export, err := mongoexport.New(opts)
	if err != nil {
		return 0, e.failed(err)
	}
	defer export.Close()
	n, err := export.Export(w)
	if err != nil {
		return n, e.failed(err)
	}
	return n, nil
}

// args returns mongoexport's arguments for the collection and the choice of
// documents. --quiet stops mongoexport writing progress to standard error.
func (e Exporter) args() []string {
	args := []string{"--quiet", "--db", e.database, "--collection", e.collection}
	if e.query != "" {
		args = append(args, "--query", e.query)
	}
	if e.sort != "" {
		args = append(args, "--sort", e.sort)
	}
	if e.skip != 0 {
		args = append(args, "--skip", strconv.FormatInt(e.skip, 10))
	}
	if e.limit != 0 {
		args = append(args, "--limit", strconv.FormatInt(e.limit, 10))
	}
	return append(args, e.uri)
}

func (e Exporter) failed(err error) error {
	return &exportError{database: e.database, collection: e.collection, err: err}
}

// exportError is a failed export of one collection. It matches ErrExport
// and wraps mongoexport's error.
type exportError struct {
	database, collection string
	err                  error
}

func (e *exportError) Error() string {
	return fmt.Sprintf("exporter: cannot export %s.%s: %v", e.database, e.collection, e.err)
}

func (e *exportError) Unwrap() error { return e.err }

func (e *exportError) Is(target error) bool { return target == ErrExport }
