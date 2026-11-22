---
title: Scripts and exec
description: Running JavaScript or any command inside the mongo container.
status: current
---

# Scripts and exec

The container an instance started can run a script in MongoDB's shell, or
any command in the image.

```go title="Illustration, not compiled"
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
func TestSeedWithAScript(t *testing.T) {
	--8<-- "mongotest/guides_test.go:script"
}
```

`db` is the `test` database; use `db.getSiblingDB(name)` for another. Each
shell formats documents differently. To get output you can parse in Go, end
the script with `JSON.stringify(...)`.

A script that throws returns an error holding a `*mongod.ScriptError`; get it
with `errors.As`. It holds everything the shell wrote, and
`errors.Is(err, mongod.ErrScriptFailed)` is true.

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
func TestSeedWithMongoimport(t *testing.T) {
	--8<-- "mongotest/guides_test.go:mongoimport"
}
```

For anything larger than a few lines, insert through the driver instead; it
is faster and avoids quoting.
