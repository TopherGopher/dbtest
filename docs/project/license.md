---
title: License
description: mongotest is released under the MIT License.
---

# License

mongotest is released under the MIT License. The full text is in the
[`LICENSE`](https://github.com/TopherGopher/mongotest/blob/main/LICENSE) file
at the root of the repository and reproduced below.

```text
--8<-- "LICENSE"
```

## Third-party software

mongotest runs the official
[`mongo`](https://hub.docker.com/_/mongo) image, which is distributed under
MongoDB's own licence terms. The root module depends on the official
[MongoDB Go driver](https://github.com/mongodb/mongo-go-driver), released
under the Apache License 2.0. The separate `exporter` module uses
[mongo-tools](https://github.com/mongodb/mongo-tools), released under the
Apache License 2.0. The optional `mobyclient` module depends on
[moby](https://github.com/moby/moby), also Apache License 2.0.
