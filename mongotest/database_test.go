package mongotest_test

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/dbtest/mongotest"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
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
// the failures and skips it reports, so that a test can end it when it
// chooses and see what happened. Fatal and Skip end the goroutine, as they
// do a test's, so call them through inGoroutine. It answers to name, if it is
// given one.
type recorder struct {
	testing.TB
	name     string
	cleanups []func()
	failures []string
	failed   bool
	skipped  bool
}

func (r *recorder) Name() string {
	if r.name == "" {
		return r.TB.Name()
	}
	return r.name
}

func (r *recorder) Cleanup(f func()) { r.cleanups = append(r.cleanups, f) }

func (r *recorder) Fail() { r.failed = true }

func (r *recorder) Error(args ...any) { r.failures = append(r.failures, fmt.Sprint(args...)) }

func (r *recorder) Errorf(format string, args ...any) {
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
}

func (r *recorder) FailNow() { r.Fail(); runtime.Goexit() }

func (r *recorder) Fatal(args ...any) { r.Error(args...); runtime.Goexit() }

func (r *recorder) Fatalf(format string, args ...any) { r.Errorf(format, args...); runtime.Goexit() }

func (r *recorder) Failed() bool { return r.failed || len(r.failures) > 0 }

func (r *recorder) SkipNow() { r.skipped = true; runtime.Goexit() }

func (r *recorder) Skip(...any) { r.SkipNow() }

func (r *recorder) Skipf(string, ...any) { r.SkipNow() }

func (r *recorder) Skipped() bool { return r.skipped }

// end runs the cleanups, last first, as the end of a test does.
func (r *recorder) end() {
	for i := len(r.cleanups) - 1; i >= 0; i-- {
		inGoroutine(r.cleanups[i])
	}
}

// inGoroutine runs f in a goroutine of its own and waits for it, so that a
// recorder's Fatal or Skip ends f and not the caller.
func inGoroutine(f func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	<-done
}

func TestIntegrationDatabaseForTakesAnyTestName(t *testing.T) {
	inst := mongotest.Run(t)

	// Each case, as a database name, holds a character the server refuses or
	// is longer than the server's 63 bytes. None relies on a space, because
	// t.Run turns spaces into underscores.
	for _, name := range []string{
		"v1.5",
		"price_$10",
		`a_"quoted"_word`,
		`back\slash`,
		"a name that is a whole sentence, as long as people really write them in a table of cases",
		strings.Repeat("é", 40),
	} {
		t.Run(name, func(t *testing.T) {
			insertInto(t, inst.DatabaseFor(t))
		})
	}
}

func TestIntegrationDatabaseForGivesEachCallItsOwn(t *testing.T) {
	inst := mongotest.Run(t)

	first, second := inst.DatabaseFor(t), inst.DatabaseFor(t)
	insertInto(t, first)

	n, err := second.Collection("people").CountDocuments(context.Background(), bson.M{})
	require.NoError(t, err, "the second database can be read")
	assert.Zero(t, n, "two databases for the same test name do not share data")
}

func TestIntegrationDatabaseForKeepsApartNamesTheServerWouldConfuse(t *testing.T) {
	inst := mongotest.Run(t)

	for reason, names := range map[string][2]string{
		"names that differ only in case": {"TestOrders/Orders", "TestOrders/orders"},
		"names with a refused character": {"TestOrders/a.b", "TestOrders/a$b"},
	} {
		t.Run(reason, func(t *testing.T) {
			// Two tests whose databases exist at the same time.
			one, other := &recorder{TB: t, name: names[0]}, &recorder{TB: t, name: names[1]}
			t.Cleanup(func() {
				other.end()
				one.end()
				assert.False(t, one.Failed(), "the first test's database is dropped without error: %v", one.failures)
				assert.False(t, other.Failed(), "and so is the second's: %v", other.failures)
			})
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
	inst := mongotest.Run(t)

	var name string
	ran := t.Run("a test that writes", func(t *testing.T) {
		db := inst.DatabaseFor(t)
		name = db.Name()
		insertInto(t, db)
		require.True(t, databaseExists(t, inst.Client, name), "the database is there during the test")
	})
	require.True(t, ran, "the test that writes passes, so its database was there")

	assert.False(t, databaseExists(t, inst.Client, name), "and is dropped once the test has ended")
}

func TestIntegrationDatabaseForReportsNothingOnceTheInstanceIsStopped(t *testing.T) {
	inst := mongotest.Run(t)
	test := &recorder{TB: t}
	insertInto(t, inst.DatabaseFor(test))

	// A deferred Stop runs before a test's cleanups, so this is the order a
	// test that starts and stops its own server ends in.
	require.NoError(t, inst.Stop(context.Background()), "the instance stops")
	test.end()

	assert.False(t, test.Failed(), "a database that went with its server is not a failed drop: %v", test.failures)
}

func TestIntegrationDatabaseForReportsADropThatFails(t *testing.T) {
	ctx := context.Background()
	inst := mongotest.Run(t)
	attached, err := mongotest.Attach(ctx, inst.URI())
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

	assert.True(t, test.Failed(), "a drop the server never answered is reported on the test")
	assert.Less(t, time.Since(began), 25*time.Second,
		"and gives up before the driver's half minute, which every test would otherwise wait")
}
