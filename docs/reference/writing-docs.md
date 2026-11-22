---
title: Writing a new page
description: How this site is built, and the conventions for adding a guide.
---

# Writing a new page

This site is plain Markdown under `docs/`, built with
[Zensical](https://zensical.org/) from the settings in `mkdocs.yml`. Every
push to `main` that changes the site rebuilds and publishes it.

## Preview locally

```sh
python3 -m venv .venv && . .venv/bin/activate
pip install -r docs/requirements.txt
zensical serve
```

Open <http://localhost:8000>. The page reloads as you save.
`zensical build --strict` is what CI runs. It fails on a broken link or
anchor, so run it before you push. It does not check the navigation:
`go test` in the repository root fails if `nav` names a page that does not
exist.

## Add a guide

1. Create `docs/guides/<slug>.md` with front matter:

    ```markdown
    ---
    title: Short title
    description: One sentence for search results and the card on the guides page.
    status: current
    tracks: ["#40"]
    ---

    # Short title

    One paragraph on what problem this pattern solves. Then the code.
    ```

2. Add it to `nav` in `mkdocs.yml` under **Guides**, in the position it
   belongs in the reading order.

3. Add a card for it on `docs/guides/index.md` so it is discoverable from the
   section landing page.

## Conventions

**Lead with working code.** A guide is a complete example first and
explanation second. The code comes from the repository, so it compiles; see
[Go on a page](#go-on-a-page).

**Say what is true today, and badge it.** `status` is one of `current`,
`changing` or `landing`, and `tracks` lists the issues (`"#40"`)
and pull requests (`"PR #53"`) the page follows. The site renders a banner
and a nav icon from them. Where a feature has a limit, say so where the reader
would trip over it, with the issue number, as "Not here yet" does in
[the mongod package](mongod.md#not-here-yet); a `!!! warning` block suits a
limit in the middle of a page.
[Keeping the docs current](../project/docs-maintenance.md) has the checklist
for flipping a badge when something lands.

**Use the theme's blocks.** Admonitions, tabs and code annotations are
described in the
[Zensical documentation](https://zensical.org/docs/authoring/admonitions/).

**Link to the generated reference for signatures.** Describe behaviour here;
let pkg.go.dev carry the exact types.

**One idea per section.** Headings are navigation. If a section needs a
second heading to stay readable, it is probably two sections.

## Go on a page

A page does not hold its own Go. Each block includes the code from an
`Example` function or a test in the repository, so CI compiles it, and runs
it when the example has an `// Output:` comment:

````markdown
```go
;--8<-- "mongod/example_test.go:start"
```
````

`start` names a section of the file. A comment holding
`;--8<-- [start:start]` marks where it begins, and one holding
`;--8<-- [end:start]` where it ends. The lines that hold the markers are left
out. To show a whole example body, put the start marker between the
function's parentheses and its opening brace, and the end marker after its
closing brace, where pkg.go.dev does not show them. Leave out the name to
include a whole file.

Most examples have no `*testing.T`, so they declare one with
`var t *testing.T` and start the section on the line below. A block may
put the function around the included lines, so a page can show a whole test
or `TestMain`:

````markdown
```go
func TestMain(m *testing.M) {
	;--8<-- "mongod/example_test.go:pull-test-main"
}
```
````

An example that needs Docker has no `// Output:` comment, so it is compiled
but never run. A block that cannot be compiled, such as a sketch of code
the reader writes, is titled so the reader knows:

````markdown
```go title="Illustration, not compiled"
repo, err := NewRepository(inst.URI())
```
````

`go test` in the repository root fails on a block of Go that does neither.
