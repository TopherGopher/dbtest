package mongotestv2_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/dbtest/mongotestv2"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// insertInto writes one document to a database, which is what makes the
// server create it.
func insertInto(t *testing.T, db *mongo.Database) {
	t.Helper()
	_, err := db.Collection("people").InsertOne(context.Background(), bson.M{"name": "Diana"})
	require.NoError(t, err, "the server accepts a write to the test's database")
}

// databaseExists asks the server whether it has a database of that name.
func databaseExists(t *testing.T, client *mongo.Client, name string) bool {
	t.Helper()
	names, err := client.ListDatabaseNames(context.Background(), bson.M{"name": name})
	require.NoError(t, err, "the server lists its databases")
	return len(names) > 0
}

// recorder stands in for a test. It keeps the cleanups a helper registers and
// the errors it reports, so that a test can end it when it chooses and see
// what was said. It answers to name, if it is given one.
type recorder struct {
	testing.TB
	name     string
	cleanups []func()
	errors   []string
}

func (r *recorder) Name() string {
	if r.name == "" {
		return r.TB.Name()
	}
	return r.name
}

func (r *recorder) Cleanup(f func()) { r.cleanups = append(r.cleanups, f) }

func (r *recorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

// end runs the cleanups, last first, as the end of a test does.
func (r *recorder) end() {
	for i := len(r.cleanups) - 1; i >= 0; i-- {
		r.cleanups[i]()
	}
}

func TestIntegrationDatabaseForTakesAnyTestName(t *testing.T) {
	inst := mongotestv2.Run(t)

	// Each of these makes a database name the server refuses, if it is used
	// as it is.
	for _, name := range []string{
		"a plain name",
		"v1.5 upgrade",
		"price $10",
		`a "quoted" word`,
		`back\slash`,
		"café au lait",
		"a name that is a whole sentence, as long as people really write them in a table of cases",
	} {
		t.Run(name, func(t *testing.T) {
			insertInto(t, inst.DatabaseFor(t))
		})
	}
}

func TestIntegrationDatabaseForGivesEachCallItsOwn(t *testing.T) {
	inst := mongotestv2.Run(t)

	first, second := inst.DatabaseFor(t), inst.DatabaseFor(t)
	insertInto(t, first)

	n, err := second.Collection("people").CountDocuments(context.Background(), bson.M{})
	require.NoError(t, err, "the second database can be read")
	assert.Zero(t, n, "two databases for the same test name do not share data")
}

func TestIntegrationDatabaseForKeepsApartNamesTheServerWouldConfuse(t *testing.T) {
	inst := mongotestv2.Run(t)

	for reason, names := range map[string][2]string{
		"names that differ only in case": {"TestOrders/Orders", "TestOrders/orders"},
		"names with a refused character": {"TestOrders/a.b", "TestOrders/a$b"},
	} {
		t.Run(reason, func(t *testing.T) {
			// Two tests whose databases exist at the same time.
			one, other := &recorder{TB: t, name: names[0]}, &recorder{TB: t, name: names[1]}
			t.Cleanup(one.end)
			t.Cleanup(other.end)
			insertInto(t, inst.DatabaseFor(one))
			second := inst.DatabaseFor(other)
			insertInto(t, second)

			n, err := second.Collection("people").CountDocuments(context.Background(), bson.M{})
			require.NoError(t, err, "the second test's database can be read")
			assert.EqualValues(t, 1, n, "the second test sees its own document and not the first's")
		})
	}
}

func TestIntegrationDatabaseForDropsTheDatabaseWhenTheTestEnds(t *testing.T) {
	inst := mongotestv2.Run(t)

	var name string
	t.Run("a test that writes", func(t *testing.T) {
		db := inst.DatabaseFor(t)
		name = db.Name()
		insertInto(t, db)
		require.True(t, databaseExists(t, inst.Client, name), "the database is there during the test")
	})

	assert.False(t, databaseExists(t, inst.Client, name), "and is dropped once the test has ended")
}

func TestIntegrationDatabaseForReportsNothingOnceTheInstanceIsStopped(t *testing.T) {
	inst := mongotestv2.Run(t)
	test := &recorder{TB: t}
	insertInto(t, inst.DatabaseFor(test))

	// A deferred Stop runs before a test's cleanups, so this is the order a
	// test that starts and stops its own server ends in.
	require.NoError(t, inst.Stop(context.Background()), "the instance stops")
	test.end()

	assert.Empty(t, test.errors, "a database that went with its server is not a failed drop")
}

func TestIntegrationDatabaseForReportsADropThatFails(t *testing.T) {
	ctx := context.Background()
	inst := mongotestv2.Run(t)
	attached, err := mongotestv2.Attach(ctx, inst.URI())
	require.NoError(t, err, "a second client connects to the same server")
	// Disconnecting from a server that has gone would wait for it too.
	t.Cleanup(func() {
		soon, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = attached.Stop(soon)
	})
	test := &recorder{TB: t}
	insertInto(t, attached.DatabaseFor(test))

	// The server goes away while the second client is still connected.
	require.NoError(t, inst.Stop(ctx), "the server is removed")
	began := time.Now()
	test.end()

	assert.Len(t, test.errors, 1, "a drop the server never answered is reported on the test")
	assert.Less(t, time.Since(began), 25*time.Second,
		"and gives up before the driver's half minute, which every test would otherwise wait")
}
