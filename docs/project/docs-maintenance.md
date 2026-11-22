---
title: Keeping the docs current
description: What the status badge means, and what to update when a pull request merges.
status: current
---

# Keeping the docs current

Each page says how current it is. This page says how to keep that true.

## The status badge

Every page about the API declares a status in its front matter, and the
site renders it as a banner under the title and an icon in the navigation:

```yaml
---
title: Replica sets
status: changing
tracks: ["#40"]
---
```

| Status | Use it when |
| --- | --- |
| `current` | The page describes what is on `main`. |
| `changing` | The page describes what works today, and an issue or pull request is reworking it. |
| `landing` | The page describes code in an open pull request. |

`tracks` lists the issues and pull requests the page follows. Write issues as
`"#40"` and pull requests as `"PR #53"`; the banner links them. A page with
no `status` shows no banner, which is fine for pages that are not about the
API, such as the license.

## When a pull request merges

Search the docs for the pull request's number and for each issue it closes,
and work through the hits:

```sh
grep -rnE 'PR #53|#40\b' docs/ mkdocs.yml
```

1. **Pages badged `landing` on that PR** become `current`. Replace prose that
   says "lands in" or "will" with the present tense.
2. **Pages badged `changing`** that the PR affects: rewrite the description
   of current behaviour to the new behaviour, delete the known-limit
   admonitions the PR fixed, drop its number from `tracks`, and set
   `status: current` once `tracks` is empty.
3. **Code samples.** The Go on a page comes from an example or a test, so
   change it there. An example that works around the old behaviour should
   become the straightforward version.

To list every page's status and what it tracks:

```sh
grep -rn '^status:\|^tracks:' docs/
```

## Checking before you push

`zensical build --strict` fails on a broken link or anchor, or an included
file or section that is missing. The Pages workflow,
`.github/workflows/docs.yml`, runs it on every pull request that changes
the site. `go test` in the repository root fails if `nav` in `mkdocs.yml`
names a page that does not exist, or if a block of Go neither includes its
code nor is marked as an illustration; see
[Writing a new page](../reference/writing-docs.md#go-on-a-page).
