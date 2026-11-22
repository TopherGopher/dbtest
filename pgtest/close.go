package pgtest

import (
	"database/sql"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// closeClients closes db and then the pool it draws from. pgxpool's Close
// waits for every connection to be released, which a leaked Rows or Tx never
// is, so a pool with connections still in use is closed in the background
// and reported instead. Call it at most once per pool: Close runs in a
// sync.Once, so a second call would wait on the first.
func closeClients(db *sql.DB, pool *pgxpool.Pool) error {
	dbErr := db.Close()
	if held := heldConnections(pool); held > 0 {
		// The goroutine ends when the code under test releases the
		// connections, which may be never. A test process can afford that.
		go pool.Close()
		return errors.Join(dbErr, &heldError{count: held})
	}
	pool.Close()
	return dbErr
}

// heldGrace is how long a connection may take to come back after the test
// has finished with it.
const heldGrace = 200 * time.Millisecond

// heldConnections returns how many connections are in use, after giving them
// heldGrace to come back. pgxpool's health check holds idle connections for
// a moment, and database/sql closes the Rows of a cancelled context in a
// goroutine of its own, so a single look could count a connection that is on
// its way back.
func heldConnections(pool *pgxpool.Pool) int32 {
	deadline := time.Now().Add(heldGrace)
	for {
		held := pool.Stat().AcquiredConns()
		if held == 0 || time.Now().After(deadline) {
			return held
		}
		time.Sleep(5 * time.Millisecond)
	}
}
