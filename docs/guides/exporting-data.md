---
title: Exporting data
description: Writing a collection out as JSON or CSV with the exporter module, to a file or any other io.Writer.
status: current
---

# Exporting data

The `exporter` module writes a collection to any `io.Writer` as JSON or CSV.
It runs `mongoexport`, from MongoDB's database tools, as a Go library, so no
`mongoexport` program needs to be installed. It is a module of its own, so
only projects that import it download those tools:

```sh
go get github.com/tophergopher/dbtest/mongotest/exporter
```

Pass `New` a connection string, a database and a collection, choose which
documents to export, then call `WriteJSON` or `WriteCSV`. Here `uri` is the
server's connection string, such as `c.URI()` for a container `c` from
`mongod.Start`:

```go
func TestExportToFile(t *testing.T) {
	--8<-- "mongotest/exporter/guides_test.go:file"
}
```

`n` is how many documents were written.

## Choosing documents

`Query`, `Sort`, `Skip` and `Limit` each return a new exporter and leave the
original unchanged, so one exporter can be the base of several exports.

| Method | Effect |
| --- | --- |
| `Query(filter)` | Exports only documents that match the filter, a JSON query document such as `{"age": {"$gt": 30}}`. |
| `Sort(order)` | Exports documents in this order, a JSON sort document such as `{"age": -1}`. |
| `Skip(n)` | Leaves out the first `n` documents. |
| `Limit(n)` | Exports at most `n` documents. Zero means no limit. |

## Formats

`WriteJSON(ctx, w, opts)` writes
[MongoDB Extended JSON](https://www.mongodb.com/docs/manual/reference/mongodb-extended-json/).
By default it writes relaxed Extended JSON, one document per line. Relaxed
JSON writes most numbers as plain JSON numbers, so a 32-bit and a 64-bit integer
look the same.
`JSONOptions` changes that:

| Field | Effect |
| --- | --- |
| `Array` | One JSON array instead of one document per line. |
| `Pretty` | Indented over several lines, for a person to read. |
| `Canonical` | Canonical Extended JSON, which keeps every BSON type exactly, such as `{"n": {"$numberInt": "1"}}`. Use it for data you will import again. |

`WriteCSV(ctx, w, fields)` writes a header naming the fields, then a row for
each document. It needs at least one field. Name a nested field with dots,
such as `address.city`.


## Timeouts and cancellation

The context's deadline limits how long an export waits to reach the server,
up to the driver's default limit of 30 seconds. A `serverSelectionTimeoutMS`
in the connection string replaces both.

Once the context is cancelled or its deadline passes, the export stops at
its next write: after the current document for JSON, or after the current
block of rows for CSV. Its error then matches the context's error as well
as `ErrExport`.

Nothing bounds a read that the server never answers. The context cannot
stop it, because `mongoexport` does not pass the context to MongoDB's Go
driver. No setting in the connection string bounds it either, because
`mongoexport` passes neither `socketTimeoutMS` nor `timeoutMS` on to the
driver. Such a read ends only when the connection closes, for example when
the server stops.

## Files and compression

The exporter writes to any `io.Writer`, so you choose where the output goes.
To write a compressed temporary file, wrap the file in a gzip writer:

```go
func TestExportCompressed(t *testing.T) {
	--8<-- "mongotest/exporter/guides_test.go:gzip"
}
```

Close the gzip writer before the file, or the file is incomplete.

## Errors

Every failed export satisfies `errors.Is(err, exporter.ErrExport)`. The
error names the collection and wraps `mongoexport`'s own error. An export
returns an error, rather than an empty result, when:

- the server cannot be reached;
- the collection does not exist, which is usually a misspelt name;
- the connection string, database or collection is empty;
- the query or sort is not valid JSON;
- `WriteCSV` is given no fields;
- the writer returns an error.

## Moving from `DatabaseExporter`

The root package's `DatabaseExporter` has been removed
([#44](https://github.com/TopherGopher/dbtest/issues/44)), and this module
replaces it. If `conn` is your old connection, its connection string is
`conn.URI()`.

| Before | After |
| --- | --- |
| `conn.DatabaseExporter(db, coll)` | `exporter.New(conn.URI(), db, coll)` |
| `Database(name)`, `Collection(name)` | Pass them to `New`. |
| `ToJSONFile(...)`, which wrote to a temporary file and returned its path | `os.CreateTemp`, then `WriteJSON` into it. |
| `ToJSONFile`'s `compressToGZIP` argument | Wrap the file in `gzip.NewWriter`. |
| `CSVFile()`, which could not be given fields | `WriteCSV(ctx, w, fields)` |
| `Filepath(path)` | `os.Create(path)`, then write into it. |
