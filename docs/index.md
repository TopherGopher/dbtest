---
template: home.html
title: mongotest
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
    // Starts MongoDB in a container, connects, and removes the container
    // when the test ends, pass or fail.
    orders := mongotest.Run(t).Database("shop").Collection("orders")

    _, err := orders.InsertOne(t.Context(), bson.M{"item": "book"})
    require.NoError(t, err)
}
```
