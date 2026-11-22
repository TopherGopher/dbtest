module github.com/tophergopher/dbtest/pgtest

go 1.26.0

toolchain go1.27.4

require (
	github.com/jackc/pgx/v5 v5.13.0
	github.com/stretchr/testify v1.12.1
	github.com/tophergopher/dbtest v0.3.0
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.3 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/text v0.41.0 // indirect
)

// Inside this repository, the root module comes from the working tree, so a
// change to it is tested here before it is tagged. Go ignores replace lines
// in a module that another module requires, so this line stays in released
// versions: a user gets the root module at the version required above (see
// "Releasing" in docs/project/contributing.md).
replace github.com/tophergopher/dbtest => ..
