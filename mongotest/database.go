// mongotest/database.go and mongotestv2/database.go differ only in the
// driver version they use. A change to one belongs in the other too.

package mongotest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/mongo"
)

// DatabaseFor returns a new database that only this test uses, and drops it
// when the test ends. It is for tests that share a server, such as one
// started in TestMain or one reached with Attach. Call it once per test and
// pass the database on, because each call returns a different database.
//
// The database's name is built from the test's name, so it can be recognised
// in the server's logs, and ends in a random part, so no two tests share one.
// A drop that fails fails the test.
func (inst *Instance) DatabaseFor(tb testing.TB) *mongo.Database {
	tb.Helper()
	db := inst.Database(databaseName(tb.Name()))
	tb.Cleanup(func() {
		if err := drop(db); err != nil {
			tb.Errorf("mongotest: cannot drop the database %s: %v", db.Name(), err)
		}
	})
	return db
}

// dropTimeout is how long a drop may take. Without it, a server that has
// gone costs every test the driver's half minute of looking for it.
const dropTimeout = 10 * time.Second

// drop drops a test's database. Its context is a new one, because the test's
// has ended by the time cleanup runs. A client that Stop has disconnected is
// no error: the database is out of reach, or gone with its container, and a
// Stop deferred inside a test function runs before that test's cleanups.
func drop(db *mongo.Database) error {
	ctx, cancel := context.WithTimeout(context.Background(), dropTimeout)
	defer cancel()
	err := db.Drop(ctx)
	if errors.Is(err, mongo.ErrClientDisconnected) {
		return nil
	}
	return err
}

// maxDatabaseName is the longest database name the server accepts, in bytes.
const maxDatabaseName = 63

// databaseName returns a database name for a test: the test's name, cut
// short, then a random ending. The ending keeps apart two tests with the same
// name, names that differ only in case (the server will not hold both), and
// the same test run in another process.
func databaseName(test string) string {
	random := make([]byte, 4)
	rand.Read(random) // never fails: see crypto/rand.Read
	ending := "_" + hex.EncodeToString(random)
	readable := strings.Map(allowedInADatabaseName, test)
	return readable[:min(len(readable), maxDatabaseName-len(ending))] + ending
}

// allowedInADatabaseName returns r if it is a letter, a digit, - or _ in
// ASCII, and _ otherwise. The server refuses / \ . " $, a space and a NUL;
// keeping to what is known to be allowed needs no list of what is not, and
// leaves every character one byte long, so the cut cannot split one.
func allowedInADatabaseName(r rune) rune {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
		return r
	}
	return '_'
}
