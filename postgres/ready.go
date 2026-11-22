package postgres

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

// The parts of PostgreSQL's wire protocol a readiness check needs. Every
// message from the server is a type byte, then a big-endian int32 length
// that counts itself and the body.
const (
	// protocolVersion is 3.0, which every supported server speaks.
	protocolVersion = 3 << 16
	// probeTimeout bounds one attempt, dial and reply together, so a port
	// that accepts and then says nothing cannot use up the start timeout.
	probeTimeout = 2 * time.Second
	// maxReplyLen is the most that is read of a reply. The server's refusal
	// is under a kilobyte, so a peer that claims more is not believed.
	maxReplyLen = 1 << 16
	// minAuthRequestLen is the length of the shortest request to
	// authenticate: the length itself and the int32 code that says how.
	minAuthRequestLen = 8
)

// Why the server was not found ready, inside the NotReadyError a caller
// sees. Only the first two put the address in doubt.
var (
	errNoReply     = errors.New("no PostgreSQL server answered at the address")
	errNotPostgres = errors.New("something other than PostgreSQL answered at the address")
	errStartingUp  = errors.New("the server is still starting up")
)

// startingUpError is the server refusing a connection while it starts up.
// It matches errStartingUp.
type startingUpError struct{ code string }

func (e *startingUpError) Error() string {
	return fmt.Sprintf("%v (SQLSTATE %s)", errStartingUp, e.code)
}

func (e *startingUpError) Is(target error) bool { return target == errStartingUp }

// sqlstateStartingUp is the SQLSTATE of "the database system is starting
// up", the one refusal that means the server is not up yet.
const sqlstateStartingUp = "57P03"

// probe asks the server at host and port to start a session, and reports
// whether it is accepting logins. A connection alone proves nothing:
// Docker accepts on a published port and hangs up while nothing listens
// behind it. The image's temporary server, which sets up the database,
// listens on no TCP port, so whatever answers here is the final one.
func probe(ctx context.Context, host string, port int, user, database string) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return fmt.Errorf("%w: %w", errNoReply, err)
	}
	defer conn.Close()
	// Closing the connection is what makes a read give up when ctx ends,
	// whether the caller cancelled it or the attempt's time ran out.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	if _, err := conn.Write(startupMessage(user, database)); err != nil {
		return fmt.Errorf("%w: %w", errNoReply, err)
	}
	return readStartupReply(conn)
}

// startupMessage asks to start a session as user on database.
func startupMessage(user, database string) []byte {
	params := "user\x00" + user + "\x00database\x00" + database + "\x00\x00"
	msg := binary.BigEndian.AppendUint32(nil, uint32(8+len(params)))
	msg = binary.BigEndian.AppendUint32(msg, protocolVersion)
	return append(msg, params...)
}

// readStartupReply reads the server's first answer to a startup message,
// and decides as libpq's PQping does, which pg_isready uses. A request to
// authenticate means the server accepts logins. So does any refusal but
// "starting up" (SQLSTATE 57P03): the server is up, and the caller's first
// query reports the real cause sooner than a timeout would. Otherwise the
// error matches errNoReply, errNotPostgres or errStartingUp.
func readStartupReply(r io.Reader) error {
	var header [5]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return fmt.Errorf("%w: %w", errNoReply, err)
	}
	kind, length := header[0], binary.BigEndian.Uint32(header[1:])
	if err := checkLength(kind, length); err != nil {
		return err
	}
	body := make([]byte, length-4)
	if _, err := io.ReadFull(r, body); err != nil {
		return fmt.Errorf("%w: %w", errNoReply, err)
	}
	if kind == 'E' {
		return refusal(body)
	}
	return nil
}

// checkLength refuses a reply that is not a request to authenticate or a
// refusal, or whose length that kind of message cannot have.
func checkLength(kind byte, length uint32) error {
	switch {
	case kind != 'R' && kind != 'E':
		return fmt.Errorf("%w: the reply had the unknown message type %q", errNotPostgres, kind)
	case length < 4 || length > maxReplyLen:
		return fmt.Errorf("%w: a reply claimed an impossible length of %d bytes", errNotPostgres, length)
	case kind == 'R' && length < minAuthRequestLen:
		return fmt.Errorf("%w: a request to authenticate was too short to say how", errNotPostgres)
	}
	return nil
}

// refusal reads the server's refusal, a list of fields, each a code byte
// and a string ending in a zero byte, with one more zero byte last. It
// returns an error only for a refusal that says the server is starting up.
func refusal(fields []byte) error {
	code := ""
	for len(fields) > 0 && fields[0] != 0 {
		value, rest, found := bytes.Cut(fields[1:], []byte{0})
		if !found {
			return fmt.Errorf("%w: an error reply was malformed", errNotPostgres)
		}
		if fields[0] == 'C' {
			code = string(value)
		}
		fields = rest
	}
	switch {
	case len(fields) == 0:
		return fmt.Errorf("%w: an error reply was malformed", errNotPostgres)
	case code == sqlstateStartingUp:
		return &startingUpError{code: code}
	}
	return nil
}
