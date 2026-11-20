---
title: Exporting data
description: Writing a collection out as JSON or CSV with the exporter module, for example to keep a test's data as a fixture.
status: current
---

# Exporting data

The `exporter` module writes a collection to any `io.Writer` as JSON or CSV.
It runs `mongoexport`, from MongoDB's database tools, as a Go library, so no
`mongoexport` program needs to be installed. It is a module of its own, so
only code that exports downloads those tools:

```sh
go get github.com/tophergopher/dbtest/mongotest/exporter
```

Pass `New` a connection string, a database and a collection, choose which
documents to export, then call `WriteJSON` or `WriteCSV`. Here `c` is a
container from `mongod.Start`, and `c.URI()` its connection string:

```go
f, err := os.Create("testdata/people.json")
require.NoError(t, err)
defer f.Close()

n, err := exporter.New(c.URI(), "app", "people").
    Query(`{"city": "Gotham"}`).
    Sort(`{"name": 1}`).
    Limit(100).
    WriteJSON(t.Context(), f, exporter.JSONOptions{Canonical: true})
require.NoError(t, err)
```

`n` is how many documents were written. Each method returns a new exporter
and leaves the original unchanged, so one exporter can be the base of
several exports.

## Choosing documents

| Method | Effect |
| --- | --- |
| `Query(filter)` | Exports only documents that match the filter, a query document such as `{"age": {"$gt": 30}}`. |
| `Sort(order)` | Exports documents in this order, a JSON sort document such as `{"age": -1}`. |
| `Skip(n)` | Leaves out the first `n` documents. |
| `Limit(n)` | Exports at most `n` documents. Zero means no limit. |

## Formats

`WriteJSON(ctx, w, opts)` writes
[MongoDB Extended JSON](https://www.mongodb.com/docs/manual/reference/mongodb-extended-json/).
By default it writes relaxed Extended JSON, one document per line.
`JSONOptions` changes that:

| Field | Effect |
| --- | --- |
| `Array` | One JSON array instead of one document per line. |
| `Pretty` | Indented over several lines, for a person to read. |
| `Canonical` | Canonical Extended JSON, which keeps every BSON type exactly, such as `{"n": {"$numberInt": "1"}}`. Use it for fixtures you will import again. |

`WriteCSV(ctx, w, fields)` writes a header naming the fields, then a row for
each document. It needs at least one field. Name a nested field with dots,
such as `address.city`.


## Timeouts and cancellation

A deadline on the context limits how long an export waits to reach the
server, unless the connection string sets `serverSelectionTimeoutMS`, which
then applies instead. A deadline later than the driver's thirty seconds
changes nothing.

Once the context is cancelled or its deadline passes, the export stops at
its next write, and its error matches the context's error as well as
`ErrExport`. JSON is written a document at a time, CSV in blocks of several.

The context cannot stop a read the server never answers: `mongoexport` does
not pass it to MongoDB's Go driver.

## Files and compression

The exporter writes to any `io.Writer`, so you choose where the output goes.
To write a compressed temporary file, wrap the file in a gzip writer:

```go
f, err := os.CreateTemp("", "people-*.json.gz")
require.NoError(t, err)
t.Cleanup(func() { _ = os.Remove(f.Name()) })

compressed := gzip.NewWriter(f)
_, err = exporter.New(c.URI(), "app", "people").WriteJSON(t.Context(), compressed, exporter.JSONOptions{})
require.NoError(t, err)
require.NoError(t, compressed.Close())
require.NoError(t, f.Close())
```

Close the gzip writer before the file, or the file is incomplete.

## Errors

Every failed export satisfies `errors.Is(err, exporter.ErrExport)`. The
error names the collection and wraps `mongoexport`'s own error. An export
fails, rather than writing nothing, when:

- the server cannot be reached;
- the collection does not exist, which is usually a misspelt name;
- the connection string, database or collection is empty;
- the query or sort is not valid JSON;
- `WriteCSV` is given no fields;
- the writer returns an error.

## Moving from `DatabaseExporter`

The root package's `DatabaseExporter` is replaced by this module, and is
removed with the rest of the old implementation
([#44](https://github.com/TopherGopher/dbtest/issues/44)). The connection
string the old `conn` used is `conn.URI()`.

| Before | After |
| --- | --- |
| `conn.DatabaseExporter(db, coll)` | `exporter.New(conn.URI(), db, coll)` |
| `Database(name)`, `Collection(name)` | Pass them to `New`. |
| `ToJSONFile(...)`, which wrote to a temporary file and returned its path | `os.CreateTemp`, then `WriteJSON` into it. |
| `ToJSONFile`'s `compressToGZIP` argument | Wrap the file in `gzip.NewWriter`. |
| `CSVFile()`, which could not be given fields | `WriteCSV(ctx, w, fields)` |
| `Filepath(path)` | `os.Create(path)`, then write into it. |
