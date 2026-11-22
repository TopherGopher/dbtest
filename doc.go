// Package dbtest is the root of a set of packages that start real databases
// in containers for Go tests. This package has no code: import one of the
// packages below.
//
//   - [github.com/tophergopher/dbtest/mongotest] starts MongoDB and gives a
//     test a connected client of MongoDB's Go driver v1,
//     go.mongodb.org/mongo-driver: inst := mongotest.Run(t).
//   - [github.com/tophergopher/dbtest/mongotestv2] does the same with a client
//     of driver v2, go.mongodb.org/mongo-driver/v2: inst := mongotestv2.Run(t).
//   - [github.com/tophergopher/dbtest/mongod] starts MongoDB with no driver
//     imported, and gives its connection string.
//   - [github.com/tophergopher/dbtest/postgres] starts PostgreSQL with no
//     driver imported, and gives its connection string for any driver, such
//     as pgx or lib/pq: c, err := postgres.Start(ctx).
//   - [github.com/tophergopher/dbtest/dockerclient] is the Docker client
//     interface. Its implementations are
//     [github.com/tophergopher/dbtest/dockerapi], the default, and
//     [github.com/tophergopher/dbtest/mobyclient], for code that already
//     uses Docker's Go client.
package dbtest
