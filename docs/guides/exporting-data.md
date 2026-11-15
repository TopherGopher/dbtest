---
title: Exporting data
description: Writing a collection out as JSON or CSV with the exporter module, for example to keep a test's data as a fixture.
status: current
---

# Exporting data

The `exporter` module writes a collection to any `io.Writer` as JSON or CSV,
using `mongoexport` from MongoDB's database tools. It is a module of its own,
so only code that exports pulls in those tools:

```sh
go get github.com/tophergopher/mongotest/exporter
```

Give it a connection string, a database and a collection, choose the
documents, and write:

```go
f, err := os.Create("testdata/people.json")
require.NoError(t, err)
defer f.Close()

n, err := exporter.New(c.URI(), "app", "people").
    Query(`{"city": "Gotham"}`).
    Sort(`{"name": 1}`).
    Limit(100).
    WriteJSON(f, exporter.JSONOptions{Canonical: true})
require.NoError(t, err)
```

`n` is how many documents were written. Each method returns a changed copy,
so one exporter can be the base of several exports.

## Choosing documents

| Method | Effect |
| --- | --- |
| `Query(json)` | Only documents that match a filter, such as `{"age": {"$gt": 30}}`. |
| `Sort(json)` | In this order, such as `{"age": -1}`. |
| `Skip(n)`, `Limit(n)` | Leave out the first `n`, or export at most `n`. |

## Formats

`WriteJSON(w, opts)` writes Extended JSON, one document per line unless
`JSONOptions` says otherwise:

| Field | Effect |
| --- | --- |
| `Array` | One JSON array instead of one document per line. |
| `Pretty` | Indented, for a person to read. |
| `Canonical` | Canonical Extended JSON, which keeps every BSON type exactly. Use it for fixtures you will import again. The default, relaxed JSON, is easier to read in a diff. |

`WriteCSV(w, fields)` writes a header naming the fields, then a row for each
document. Name nested fields with dots, such as `address.city`.

## Files and compression

The exporter writes to whatever it is given, so a file, a temporary file or
gzip is a wrapper of your choosing:

```go
f, err := os.CreateTemp("", "people-*.json.gz")
require.NoError(t, err)
t.Cleanup(func() { _ = os.Remove(f.Name()) })

compressed := gzip.NewWriter(f)
_, err = exporter.New(c.URI(), "app", "people").WriteJSON(compressed, exporter.JSONOptions{})
require.NoError(t, err)
require.NoError(t, compressed.Close())
require.NoError(t, f.Close())
```

## Errors

A failed export matches `exporter.ErrExport`, and wraps `mongoexport`'s own
error, which says what went wrong: a server that cannot be reached, a query
that is not JSON, or CSV without fields.

## Moving from `DatabaseExporter`

The root package's `DatabaseExporter` is replaced by this module and goes
when the old implementation is removed
([#44](https://github.com/TopherGopher/mongotest/issues/44)).

| Before | After |
| --- | --- |
| `conn.DatabaseExporter(db, coll)` | `exporter.New(uri, db, coll)` |
| `Database(name)`, `Collection(name)` | Pass them to `New`. |
| `ToJSONFile(...)`, which wrote to a temporary file and returned its path | `os.CreateTemp`, then `WriteJSON` into it. |
| `CSVFile()`, which could not be given fields | `WriteCSV(w, fields)` |
| `Filepath(path)`, gzip | Wrap the writer: `os.Create`, `gzip.NewWriter`. |
