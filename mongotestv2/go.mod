module github.com/tophergopher/dbtest/mongotestv2

go 1.26.0

toolchain go1.27.4

require (
	github.com/stretchr/testify v1.12.1
	github.com/tophergopher/dbtest v0.3.0
	go.mongodb.org/mongo-driver/v2 v2.10.0
)

require (
	github.com/klauspost/compress v1.20.3 // indirect
	github.com/xdg-go/scram v1.4.0 // indirect
	github.com/xdg-go/stringprep v1.0.4 // indirect
	github.com/youmark/pkcs8 v0.0.0-20240726163527-a2c0da244d78 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)

// Inside this repository, the root module comes from the working tree, so a
// change to it is tested here before it is tagged. Go ignores replace lines
// in a module that another module requires, so this line stays in released
// versions: a user gets the root module at the version required above (see
// "Releasing" in docs/project/contributing.md).
replace github.com/tophergopher/dbtest => ..
