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

An instance from `Attach` has no container: check `inst.Container` for nil
first.

## Running a script

`RunScript` runs JavaScript in the image's shell, `mongosh`, or the legacy
`mongo` where the image has only that. When the container has credentials,
the shell logs in as the root user first. It returns what the script printed,
trimmed, which for a one-line script is the value of its last expression.
Multi-line scripts are fine:

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
shell formats a document its own way, so end with `JSON.stringify(...)` to
parse the result. A script that throws returns a `*mongod.ScriptError`, which
matches `mongod.ErrScriptFailed` and holds everything the shell wrote.

## Running a command

`Exec` runs any program in the image and returns a `dockerclient.ExecResult`
with `Stdout`, `Stderr` and `ExitCode`. A command that exits non-zero is a
result, not an error.

| Command | What for |
| --- | --- |
| `mongoimport --db app --collection roster --file /tmp/roster.json` | Load a fixture you copied in. |
| `mongod --version` | Confirm which server version the image runs. |
| `cat /proc/meminfo` | Debug a slow or dying container. |

## Seeding from a fixture file

`Exec` cannot feed a file to a command's standard input, so write the data
inline:

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

`RunMongoScriptOnContainer` and `ExecCommandInMongoContainer` still work. On
failure, both return what the process wrote and an error matching
`mongotest.ErrCommandFailed`; the second returns standard output followed by
standard error. Both return `mongotest.ErrNoContainer` for an instance from
`Attach`.
