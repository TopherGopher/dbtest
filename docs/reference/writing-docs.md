---
title: Writing a new page
description: How this site is built, and the conventions for adding a guide.
---

# Writing a new page

This site is plain Markdown under `docs/`, built with
[Zensical](https://zensical.org/) from the settings in `mkdocs.yml`. The
Pages workflow, `.github/workflows/docs.yml`, rebuilds and publishes it when
a push to `main` changes the files that workflow lists.

## Preview locally

```sh
python3 -m venv .venv && . .venv/bin/activate
pip install -r docs/requirements.txt
zensical serve
```

Open <http://localhost:8000>. The page reloads as you save.

Before you push, run `zensical build --strict`, as the Pages workflow does.
It fails on a broken link or anchor, or on an included file or section that
is missing. Then run `go test` in the repository root. It fails if `nav`
names a page that does not exist, or if a block of Go neither includes its
code nor is titled as an illustration.

If the build says a page does not exist, look at the end of its output: a
missing file or section shows there as `SnippetMissingError`.

## Add a guide

1. Create `docs/guides/<slug>.md` with front matter. `tracks` lists the
   issues the page follows; see [Conventions](#conventions).

    ```markdown
    ---
    title: Short title
    description: One sentence for search results and the card on the guides page.
    status: current
    tracks: ["#40"]
    ---

    # Short title

    One paragraph on the problem this pattern solves, then the code.
    ```

2. Add it to `nav` in `mkdocs.yml` under **Guides**, in the position it
   belongs in the reading order.

3. Add a card for it on `docs/guides/index.md` so it is discoverable from the
   section landing page.

## Conventions

**Lead with working code.** After a short paragraph on the problem, a guide
shows a complete example, then explains it. The code comes from the
repository, so it compiles; see [Go on a page](#go-on-a-page).

**Badge what is true today.** `status` is one of `current`, `changing` or
`landing`, and `tracks` lists the issues (`"#40"`) and pull requests
(`"PR #53"`) the page follows. The site renders a banner and a nav icon from
them. [Keeping the docs current](../project/docs-maintenance.md) says what
each status means, and how to change it when something lands.

**Say where a limit is.** Where a feature has a limit, say so where the
reader would trip over it, with the issue number, as "Not here yet" does in
[the mongod package](mongod.md#not-here-yet). A `!!! warning` block suits a
limit in the middle of a page.

**Use the theme's blocks.** Admonitions, tabs and code annotations are
described in the
[Zensical documentation](https://zensical.org/docs/authoring/admonitions/).

**Link to pkg.go.dev for signatures.** Describe behaviour here; let
pkg.go.dev carry the exact types.

**One idea per section.** Headings are navigation. If a section needs a
second heading to stay readable, it is probably two sections.

## Go on a page

Each block of Go on a page includes its code from a test file in the
repository, so CI compiles it. Code that needs Docker comes from an
integration test, which runs it against a real server. Other code usually
comes from an `Example`, which is run too when it has an `// Output:`
comment, and is only compiled when it has none.

````markdown
```go
;--8<-- "mongod/example_test.go:run-script"
```
````

In this page's Markdown source, each `--8<--` is written with a `;` in
front, which stops the site from acting on it here. Leave the `;` out of
yours.

`run-script` names a section of the file. A comment holding
`;--8<-- [start:run-script]` marks where it begins, and one holding
`;--8<-- [end:run-script]` where it ends. The lines that hold the markers are
left out. Leave out the name to include a whole file.

To show a whole function body, put the markers on the `func` line and the
closing-brace line. Those lines are left out, and pkg.go.dev does not show
the comments:

````markdown
```go
func ExampleStart() /* ;--8<-- [start:start] */ {
	...
} // ;--8<-- [end:start]
```
````

A section that ends at an example's closing brace includes its `// Output:`
comment, which shows the reader what the code prints.

To show part of a body, put each marker on a line of its own. pkg.go.dev
shows a marker inside an example's body, so prefer a whole body there; a
test is not shown on pkg.go.dev, so markers inside it are hidden. An
`Example` takes no arguments, so one that shows the body of a `TestMain`
declares `var m *testing.M` and ends that line with the start marker, which
keeps the declaration off the page.

A block can wrap the included lines in a function written on the page, to
show a whole test or `TestMain`:

````markdown
```go
func TestMain(m *testing.M) {
	;--8<-- "mongod/example_test.go:pull-test-main"
}
```
````

A block that is not included from the repository, such as a sketch of code
the reader writes, must have the title `Illustration, not compiled`:

````markdown
```go title="Illustration, not compiled"
repo, err := NewRepository(inst.URI())
```
````

`go test` in the repository root fails on any other Go block that has no
include, and on an include from a file that `go test` does not compile.
