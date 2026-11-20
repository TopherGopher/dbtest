module github.com/tophergopher/dbtest/mobyclient

go 1.26.0

toolchain go1.27.4

require (
	github.com/containerd/errdefs v1.0.0
	github.com/moby/moby/api v1.57.0
	github.com/moby/moby/client v0.6.3
	github.com/stretchr/testify v1.12.1
	github.com/tophergopher/dbtest v0.0.0
)

require (
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/containerd/errdefs/pkg v0.3.0 // indirect
	github.com/distribution/reference v0.6.0 // indirect
	github.com/docker/go-connections v0.8.2 // indirect
	github.com/docker/go-units v0.5.0 // indirect
	github.com/felixge/httpsnoop v1.0.4 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/moby/docker-image-spec v1.3.1 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/opencontainers/image-spec v1.1.1 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.60.0 // indirect
	go.opentelemetry.io/otel v1.47.0 // indirect
	go.opentelemetry.io/otel/log v1.47.0 // indirect
	go.opentelemetry.io/otel/metric v1.47.0 // indirect
	go.opentelemetry.io/otel/trace v1.47.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/sys v0.49.0 // indirect
)

// Inside this repository, the root module comes from the working tree, so a
// change to it is tested here before it is tagged. Go ignores replace lines
// in a module that another module requires, so this line stays in released
// versions: a user gets the root module at the version required above, which
// release.sh has set by then (see "Releasing" in docs/project/contributing.md).
replace github.com/tophergopher/dbtest => ..
