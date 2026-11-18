// Package exporter writes a MongoDB collection out as JSON or CSV, using
// mongoexport from MongoDB's database tools. It lives in its own module so
// that only callers who export download those tools.
//
//	c, err := mongod.Start(ctx) // or any server's connection string
//	...
//	n, err := exporter.New(c.URI(), "app", "people").WriteJSON(ctx, file, exporter.JSONOptions{})
//
// The context bounds how long an export waits to reach the server, and
// stops an export between documents once it ends. It cannot interrupt a read
// the server never answers, because mongoexport does not pass it to the
// driver.
package exporter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mongodb/mongo-tools/common/progress"
	"github.com/mongodb/mongo-tools/mongoexport"
)

// ErrExport is what every error from WriteJSON and WriteCSV matches. The
// error names the collection and wraps mongoexport's own error.
var ErrExport = errors.New("the export failed")

// Exporter is an export of one collection. Each method returns a new
// Exporter and leaves the original unchanged, so one Exporter can be the
// base of several exports.
type Exporter struct {
	uri, database, collection string
	query, sort               string
	skip, limit               int64
}

// New returns an Exporter for every document in database.collection on the
// server at uri, a MongoDB connection string.
func New(uri, database, collection string) Exporter {
	return Exporter{uri: uri, database: database, collection: collection}
}

// Query limits the export to documents that match filter, a query document
// in Extended JSON such as `{"age": {"$gt": 30}}`.
func (e Exporter) Query(filter string) Exporter {
	e.query = filter
	return e
}

// Sort sets the order of the export. order is a JSON sort document such as
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

// Limit exports at most n documents. Zero means no limit.
func (e Exporter) Limit(n int64) Exporter {
	e.limit = n
	return e
}

// JSONOptions controls the layout of WriteJSON's output. The zero value
// writes relaxed Extended JSON, one document per line.
type JSONOptions struct {
	Array     bool // one JSON array instead of one document per line
	Pretty    bool // indented for a person to read
	Canonical bool // canonical Extended JSON, which keeps every BSON type
}

// WriteJSON writes the documents to w as Extended JSON, and returns how many
// documents it wrote. If it fails partway, w holds what was written so far.
// See the package documentation for what ctx can and cannot stop.
func (e Exporter) WriteJSON(ctx context.Context, w io.Writer, opts JSONOptions) (documents int64, err error) {
	format := []string{"--type=json"}
	if opts.Array {
		format = append(format, "--jsonArray")
	}
	if opts.Pretty {
		format = append(format, "--pretty")
	}
	if opts.Canonical {
		format = append(format, "--jsonFormat=canonical")
	}
	return e.export(ctx, w, format)
}

// WriteCSV writes the documents to w as CSV: a header naming the fields,
// then a row for each document. It returns how many documents it wrote.
// fields must not be empty; name a nested field with dots, as in
// "address.city". See the package documentation for what ctx can and cannot
// stop.
func (e Exporter) WriteCSV(ctx context.Context, w io.Writer, fields []string) (documents int64, err error) {
	return e.export(ctx, w, []string{"--type=csv", "--fields=" + strings.Join(fields, ",")})
}

// errEmptyName is why an export with an empty connection string, database
// or collection is refused: mongoexport would quietly use localhost or the
// test database instead.
var errEmptyName = errors.New("the connection string, database and collection given to New must not be empty")

// export runs mongoexport with the format's arguments and the export's own.
func (e Exporter) export(ctx context.Context, w io.Writer, format []string) (int64, error) {
	if e.uri == "" || e.database == "" || e.collection == "" {
		return 0, e.failed(errEmptyName)
	}
	if err := ctx.Err(); err != nil {
		return 0, e.failed(err)
	}
	args := append(format, e.args(ctx)...)
	opts, err := mongoexport.ParseOptions(args, "mongotest", "")
	if err != nil {
		return 0, e.failed(err)
	}
	export, err := mongoexport.New(opts)
	if err != nil {
		return 0, e.failed(err)
	}
	stopProgressBar(export)
	defer export.Close()
	n, err := export.Export(contextWriter{ctx: ctx, w: w})
	if err != nil {
		return n, e.failed(withContextError(ctx, err))
	}
	return n, nil
}

// contextWriter refuses to write once ctx has ended. mongoexport writes one
// document at a time, so this stops an export between documents. It cannot
// stop a read blocked on the server: mongo-tools passes no context to the
// driver, and closing its client from another goroutine would race.
type contextWriter struct {
	ctx context.Context
	w   io.Writer
}

func (c contextWriter) Write(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.w.Write(p)
}

// withContextError adds ctx's error to err once ctx has ended, since
// mongoexport may report the failed write without wrapping its cause.
func withContextError(ctx context.Context, err error) error {
	if ctx.Err() == nil || errors.Is(err, ctx.Err()) {
		return err
	}
	return fmt.Errorf("%w: %w", ctx.Err(), err)
}

// selectionTimeout returns mongoexport's --serverSelectionTimeout for ctx's
// deadline, in whole seconds rounded up, and false when ctx has none or the
// connection string sets its own, which mongoexport refuses to have
// overridden. mongoexport reads zero as the driver's thirty seconds, so the
// least it returns is one.
func selectionTimeout(ctx context.Context, uri string) (int, bool) {
	deadline, ok := ctx.Deadline()
	if !ok || setsSelectionTimeout(uri) {
		return 0, false
	}
	seconds := int(math.Ceil(time.Until(deadline).Seconds()))
	return max(seconds, 1), true
}

// setsSelectionTimeout reports whether a connection string sets its own
// server selection timeout.
func setsSelectionTimeout(uri string) bool {
	u, err := url.Parse(uri)
	if err != nil {
		return false
	}
	for key := range u.Query() {
		if strings.EqualFold(key, "serverSelectionTimeoutMS") {
			return true
		}
	}
	return false
}

// stopProgressBar stops the progress bar mongoexport starts, which nothing
// shows. Its goroutine reads mongo-tools' global log level without a lock,
// so it races with another export setting that level. Close would stop it a
// second time and block, so the field is cleared.
func stopProgressBar(export *mongoexport.MongoExport) {
	if bar, ok := export.ProgressManager.(*progress.BarWriter); ok {
		bar.Stop()
	}
	export.ProgressManager = nil
}

// args returns mongoexport's arguments for the collection and the choice of
// documents. Each value is joined to its flag with "=", so a value that
// starts with a dash is not read as a flag. --quiet stops mongoexport writing
// to standard error; it sets mongo-tools' log level for the whole process.
// --assertExists makes a misspelt collection an error, not an empty export.
func (e Exporter) args(ctx context.Context) []string {
	args := []string{"--quiet", "--assertExists", "--db=" + e.database, "--collection=" + e.collection}
	if seconds, ok := selectionTimeout(ctx, e.uri); ok {
		args = append(args, "--serverSelectionTimeout="+strconv.Itoa(seconds))
	}
	if e.query != "" {
		args = append(args, "--query="+e.query)
	}
	if e.sort != "" {
		args = append(args, "--sort="+e.sort)
	}
	if e.skip != 0 {
		args = append(args, "--skip="+strconv.FormatInt(e.skip, 10))
	}
	if e.limit != 0 {
		args = append(args, "--limit="+strconv.FormatInt(e.limit, 10))
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
	return fmt.Sprintf("exporter: cannot export %s.%s: %v. Check that the server is reachable at the "+
		"connection string, and the database, collection, query and sort given", e.database, e.collection, e.err)
}

func (e *exportError) Unwrap() error { return e.err }

func (e *exportError) Is(target error) bool { return target == ErrExport }
