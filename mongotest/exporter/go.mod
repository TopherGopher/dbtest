module github.com/tophergopher/dbtest/mongotest/exporter

go 1.26.8

toolchain go1.27.4

require (
	github.com/mongodb/mongo-tools v0.0.0-20261117220938-eb3792147072
	github.com/stretchr/testify v1.12.1
	github.com/tophergopher/dbtest v0.0.0
	go.mongodb.org/mongo-driver/v2 v2.10.0
)

require (
	github.com/Azure/azure-sdk-for-go/sdk/azcore v1.24.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/azidentity v1.15.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/internal v1.13.0 // indirect
	github.com/AzureAD/microsoft-authentication-library-for-go v1.10.1 // indirect
	github.com/ccoveille/go-safecast/v2 v2.0.1 // indirect
	github.com/deckarep/golang-set/v3 v3.0.0 // indirect
	github.com/goccy/go-yaml v1.19.2 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jessevdk/go-flags v1.6.1 // indirect
	github.com/klauspost/compress v1.20.4 // indirect
	github.com/kylelemons/godebug v1.1.0 // indirect
	github.com/pkg/browser v0.0.0-20240102092130-5ac0b6a4141c // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/samber/lo v1.53.0 // indirect
	github.com/xdg-go/scram v1.4.0 // indirect
	github.com/xdg-go/stringprep v1.0.4 // indirect
	github.com/youmark/pkcs8 v0.0.0-20240726163527-a2c0da244d78 // indirect
	go.yaml.in/yaml/v3 v3.0.6 // indirect
	golang.org/x/crypto v0.59.0 // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/sync v0.25.0 // indirect
	golang.org/x/sys v0.50.0 // indirect
	golang.org/x/term v0.48.0 // indirect
	golang.org/x/text v0.44.0 // indirect
)

// Inside this repository, the root module comes from the working tree, so a
// change to it is tested here before it is tagged. Go ignores replace lines
// in a module that another module requires, so this line stays in released
// versions: a user gets the root module at the version required above (see
// "Releasing" in docs/project/contributing.md).
replace github.com/tophergopher/dbtest => ../..
