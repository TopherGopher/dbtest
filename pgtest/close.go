package pgtest

import (
	"database/sql"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// closeClients closes db and then the pool it draws from, and reports the
// connections still in use. pgxpool's Close waits for every connection to
// come back, which one held by a leaked Rows or Tx never does, so Close runs
// in a goroutine and is given heldGrace before the connections still out are
// counted. Call it at most once per pool: Close runs in a sync.Once, so a
// second call would wait on the first.
func closeClients(db *sql.DB, pool *pgxpool.Pool) error {
	dbErr := db.Close()
	closed := make(chan struct{})
	go func() {
		// This goroutine ends when the code under test releases its
		// connections, which may be never. A test process can afford that.
		pool.Close()
		close(closed)
	}()
	select {
	case <-closed:
		return dbErr
	case <-time.After(heldGrace):
	}
	// A closing pool hands out no connection, so the count can only fall.
	if held := pool.Stat().AcquiredConns(); held > 0 {
		return errors.Join(dbErr, &heldError{count: held})
	}
	// Nothing is held: Close is only closing idle connections, which pgx
	// bounds with a timeout of its own.
	<-closed
	return dbErr
}

// heldGrace is how long connections may take to come back once the clients
// are closing. database/sql closes the Rows of a cancelled context, such as
// a test's own, in a goroutine of its own, and pgx takes a moment to close a
// connection whose query was cancelled.
const heldGrace = 200 * time.Millisecond
