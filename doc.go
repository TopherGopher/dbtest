// Package dbtest is the root of a set of packages that start real databases
// in containers for Go tests. This package has no code: import the one for
// your database.
//
//   - [github.com/tophergopher/dbtest/mongotest] starts MongoDB and gives a
//     test a connected client of MongoDB's Go driver: inst := mongotest.Run(t).
//   - [github.com/tophergopher/dbtest/mongod] starts MongoDB with no driver
//     imported, and gives its connection string.
//   - [github.com/tophergopher/dbtest/dockerclient] is the Docker client
//     interface, with [github.com/tophergopher/dbtest/dockerapi] and
//     [github.com/tophergopher/dbtest/mobyclient] as its implementations.
package dbtest
