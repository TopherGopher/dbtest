---
title: Getting started
description: What dbtest is, what it needs from your machine, and where to go first.
status: current
---

# Getting started

dbtest gives a Go test its own MongoDB server. It starts the official
`mongo` image in a container, connects a client to it, and removes the
container when the test ends.

<div class="grid cards" markdown>

-   :material-download:{ .lg .middle } **Installation**

    ---

    Add the module, confirm a daemon is reachable, and run the first test.

    [:octicons-arrow-right-24: Install](installation.md)

-   :material-rocket-launch:{ .lg .middle } **Quickstart**

    ---

    A complete test file that starts a server, writes a document and reads it
    back, with every line explained.

    [:octicons-arrow-right-24: Quickstart](quickstart.md)

-   :material-cog-outline:{ .lg .middle } **How it works**

    ---

    What happens between `mongotest.Run(t)` and the first query, and why
    each step is there.

    [:octicons-arrow-right-24: How it works](how-it-works.md)

-   :material-book-open-page-variant:{ .lg .middle } **Guides**

    ---

    Usage patterns: parallel tests, replica sets, running scripts, exporting
    fixtures, cleanup and CI.

    [:octicons-arrow-right-24: Browse the guides](../guides/index.md)

</div>

## What you need

| Requirement | Why |
| --- | --- |
| **Go 1.26 or newer** | It is the oldest Go release still supported. Requiring dbtest raises your module's `go` line to 1.26 at most. |
| **A Docker-compatible daemon** | Docker Engine, Docker Desktop or Podman. dbtest talks to the Engine API; it never shells out to the `docker` binary. |
| **Network access to Docker Hub on the first run** | The `mongo` image is pulled once and cached by the daemon. Later runs need no network. |
| **MongoDB's Go driver, v1 or v2** | Your test uses the driver's own client. `mongotest` gives a v1 client, and `mongotestv2` a v2 client. |

Without a daemon, `mongotest.Run` fails the test; it never skips it.

!!! tip "Already running a server?"
    `mongotest.Attach(ctx, uri)` connects to a server you already run, such
    as a CI service container, and starts nothing. Its instance has no
    container, so scripts and commands need `Run` or `Start`. See
    [Starting a server](../guides/test-connection.md#a-server-you-already-run).
