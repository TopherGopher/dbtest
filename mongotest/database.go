package mongotest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/mongo"
)

// DatabaseFor returns a database that only this test uses, and drops it when
// the test ends. Tests that share a server, such as one started in TestMain or
// one reached with Attach, each call it for a database of their own:
//
//	people := inst.DatabaseFor(t).Collection("people")
//
// The database's name starts with the test's name, so it can be recognised in
// the server's logs, and is never the same twice.
func (inst *Instance) DatabaseFor(tb testing.TB) *mongo.Database {
	tb.Helper()
	db := inst.Database(databaseName(tb.Name()))
	// The test's context has ended by the time cleanup runs.
	tb.Cleanup(func() {
		if err := db.Drop(context.Background()); dropFailed(err) {
			tb.Errorf("mongotest: cannot drop the database %s: %v", db.Name(), err)
		}
	})
	return db
}

// dropFailed reports whether err is a drop that left the database behind.
// After Stop the client is disconnected and the database is out of reach or
// gone with its container, which is not worth failing a test for: a deferred
// Stop runs before a test's cleanups.
func dropFailed(err error) bool {
	return err != nil && !errors.Is(err, mongo.ErrClientDisconnected)
}

// maxDatabaseName is the longest database name the server accepts, in bytes.
const maxDatabaseName = 63

// databaseName returns a database name for a test: the test's name, cut
// short, then a random ending. The ending keeps apart two tests of one name,
// names that differ only in case, which the server treats as one, and the
// same test in another process.
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
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return r
	case r == '-':
		return r
	}
	return '_'
}
