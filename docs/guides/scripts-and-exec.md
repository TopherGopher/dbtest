---
title: Scripts and exec
description: Running JavaScript or any command inside the mongo container.
status: current
---

# Scripts and exec

The container an instance started can run a script in MongoDB's shell, or
any command in the image.

```go
inst := mongotest.Run(t)

ok, err := inst.Container.RunScript(t.Context(), "db.runCommand({ping: 1}).ok")
result, err := inst.Container.Exec(t.Context(), "mongod", "--version")
```

An instance from `Attach` has no container: its `Container` is nil, and
calling these methods on it panics. Check it first.

## Running a script

`RunScript` runs JavaScript in the image's MongoDB shell: `mongosh`, or the
older `mongo` shell on images that lack `mongosh`. When the container has
credentials, the shell logs in as the root user first. It returns what the
shell printed, with surrounding white space removed. The shell prints the
value of the last expression, so a script ending in an expression returns
that value:

```go
out, err := inst.Container.RunScript(t.Context(), `
    const app = db.getSiblingDB('app');
    app.roster.insertMany([
        { name: 'Diana', city: 'Themyscira' },
        { name: 'Bruce', city: 'Gotham' },
    ]);
    app.roster.countDocuments()
`)
require.NoError(t, err)
require.Equal(t, "2", out)
```

`db` is the `test` database; use `db.getSiblingDB(name)` for another. Each
shell formats documents differently. To get output you can parse in Go, end
the script with `JSON.stringify(...)`.

A script that throws returns a `*mongod.ScriptError`. It holds everything the
shell wrote, and `errors.Is(err, mongod.ErrScriptFailed)` is true.

## Running a command

`Exec` runs any program in the image and returns a `dockerclient.ExecResult`
with `Stdout`, `Stderr` and `ExitCode`. A non-zero exit is reported in
`ExitCode`, not as an error; check it yourself.

| Command | What for |
| --- | --- |
| `mongod --version` | Confirm which server version the image runs. |
| `cat /proc/meminfo` | Debug a slow or dying container. |

## Seeding a few documents with mongoimport

`Exec` cannot feed a file to a command's standard input, so write the data
inline. The shell quoting means the data must not contain a single quote:

```go
docs := `{"name":"Diana","city":"Themyscira"}
{"name":"Bruce","city":"Gotham"}`

result, err := inst.Container.Exec(t.Context(),
    "sh", "-c", "printf '%s' '"+docs+"' | mongoimport --quiet --db app --collection roster")
require.NoError(t, err)
require.Zero(t, result.ExitCode, result.Stderr)
```

For anything larger than a few lines, insert through the driver instead; it
is faster and avoids quoting.

## The earlier methods

`RunMongoScriptOnContainer` and `ExecCommandInMongoContainer` are kept for
existing code; new code uses `RunScript` and `Exec`. On failure, both return
what the process wrote and an error matching `mongotest.ErrCommandFailed`.
`ExecCommandInMongoContainer` always returns standard output followed by
standard error. Both return `mongotest.ErrNoContainer` for an instance from
`Attach`.
