---
template: home.html
title: dbtest
hide:
  - navigation
  - toc
  - footer
---

<!-- The landing page is rendered by docs/overrides/home.html. The code block
     below is the only Markdown on this page: the template drops it into the
     hero so it gets the same syntax highlighting and copy button as every
     other page. -->

```go title="orders_test.go"
func TestOrders(t *testing.T) {
	--8<-- "mongotest/example_test.go:run"
}
```
