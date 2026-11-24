---
title: Errors
description: The error sentinels and typed carriers every Docker client implementation reports.
status: current
---

# Errors

Every implementation of `dockerclient.Client` reports failures as one of a
small set of sentinels, or as a typed value that wraps one. Branch with
`errors.Is` or `errors.As`; never parse a message. Messages state what went
wrong, the value at fault and what to do about it, and are free to change.

## Sentinels

| Sentinel | Carrier type | Meaning |
| --- | --- | --- |
| `ErrInvalidArgument` | `*InvalidArgumentError` | A value was rejected before any request was sent. The error names the argument, the problem and the fix. |
| `ErrConnectionFailed` | `*ConnectionError` | The daemon could not be reached, or no TLS connection could be made: a certificate was rejected, or plain HTTP was sent to a daemon that accepts only TLS. The error names the socket or address tried. |
| `ErrAPIVersion` | `*APIVersionError` | The client's and daemon's API version ranges do not overlap, or a feature needs a newer daemon. Names the product, so Podman users are not told to upgrade Docker. |
| `ErrNotFound` | `*StatusError` | The daemon answered 404. For `ContainerCreate` this means the image is not local: pull and retry. |
| `ErrConflict` | `*StatusError` | The daemon answered 409, for instance a container name already in use. |
| `ErrUnauthorized` | `*StatusError` | The daemon, or a proxy in front of it, answered 401 or 403. `StatusError.FromProxy` is true when a proxy refused the request. |
| `ErrDaemonResponse` | `*ResponseError` | The daemon's answer could not be used, or a request that reached the daemon broke off partway. |
| `ErrStream` | `*StreamError` | An exec output stream reported a problem mid-way. |
| `ErrPull` | `*PullError` | The pull progress stream carried an error line from the daemon. |
| `ErrExecUnfinished` | | An exec process was still running when the client stopped waiting for its exit code. |

Three fixed-message cases are predeclared values so they can be compared
directly:

| Value | Wraps | When |
| --- | --- | --- |
| `ErrNoCommand` | `ErrInvalidArgument` | `Exec` or `ExecCreate` was given no program. |
| `ErrNoFiles` | `ErrInvalidArgument` | `CopyToContainer` was given an empty list. |
| `ErrEmptyHost` | `ErrInvalidArgument` | `WithHost("")`. |

## Branching

```go
--8<-- "dockerclient/example_test.go:branching"
```

## Reading the details

Each carrier exposes what it knows. `errors.As` gets you there:

```go
--8<-- "dockerclient/example_test.go:status-fields"
```

An `*APIVersionError` says which API versions each side accepts:

```go
--8<-- "dockerclient/example_test.go:version-fields"
```

`StatusError` also carries a hint, a sentence suggesting the usual fix for
that status on that endpoint, which is included in the message.

## Validation happens first

Both implementations and the `Fake` double apply the same argument checks
before anything is sent: container and exec IDs must be non-empty and
well-formed, image references must parse, destination directories must be
absolute, file modes must be valid, port bindings must name a protocol. This
means a test against `dockermock.NewFake()` rejects the same bad input a real
daemon would, and the error is identical.
