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
	// probeTimeout bounds one attempt, so a port that accepts and then says
	// nothing cannot use up the whole start timeout.
	probeTimeout = 2 * time.Second
	// maxReplyLen is the most that is read of a reply. The server's refusal
	// is under a kilobyte, so a peer that claims more is not believed.
	maxReplyLen = 1 << 16
	// authCodeLen is the int32 every authentication request starts with.
	authCodeLen = 4
)

// Why the server was not found ready, inside the NotReadyError a caller
// sees. Only the first two put the address in doubt.
var (
	errNoReply     = errors.New("nothing answered as PostgreSQL does at the address")
	errNotPostgres = errors.New("something answered at the address, but not as PostgreSQL does")
	errStartingUp  = errors.New("the server is still starting up")
)

// startingUpError is the server refusing a connection while it starts up,
// in its own words. It matches errStartingUp.
type startingUpError struct {
	Code    string // the SQLSTATE, such as "57P03", or "" if the server sent none
	Message string
}

func (e *startingUpError) Error() string {
	return fmt.Sprintf("%v: %s (SQLSTATE %s)", errStartingUp, e.Message, e.Code)
}

func (e *startingUpError) Is(target error) bool { return target == errStartingUp }

// sqlstateStartingUp is the SQLSTATE of "the database system is starting
// up", the one refusal that means the server is not up yet.
const sqlstateStartingUp = "57P03"

// probe asks the server at host and port to start a session, and reports
// whether it is accepting connections. A connection alone proves nothing:
// Docker accepts on a published port and hangs up while nothing listens
// behind it. The image's temporary server, which sets up the database,
// listens on no TCP port, so whatever answers here is the final one.
func probe(ctx context.Context, host string, port int, user, database string) error {
	dialer := net.Dialer{Timeout: probeTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return fmt.Errorf("%w: %w", errNoReply, err)
	}
	defer conn.Close()
	// Closing the connection is what makes a read give up when ctx ends.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(probeTimeout))

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
// authenticate means the server accepts connections. So does any refusal
// but "starting up" (SQLSTATE 57P03): the server is up, and the caller's
// first query reports the real cause sooner than a timeout would. Otherwise
// the error matches errNoReply, errNotPostgres or errStartingUp.
func readStartupReply(r io.Reader) error {
	var header [5]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return fmt.Errorf("%w: %w", errNoReply, err)
	}
	kind, length := header[0], binary.BigEndian.Uint32(header[1:])
	if length < 4 || length > maxReplyLen {
		return fmt.Errorf("%w: a reply claimed a length of %d bytes", errNotPostgres, length)
	}
	body := io.LimitReader(r, int64(length-4))
	switch kind {
	case 'R':
		return readAuthRequest(body)
	case 'E':
		return readServerError(body)
	}
	return fmt.Errorf("%w: the reply was of type %q", errNotPostgres, kind)
}

// readAuthRequest checks that an authentication request holds its code.
// Which method the server asks for does not matter.
func readAuthRequest(body io.Reader) error {
	var code [authCodeLen]byte
	_, err := io.ReadFull(body, code[:])
	switch {
	case errors.Is(err, io.EOF):
		return fmt.Errorf("%w: an authentication request had no code", errNotPostgres)
	case err != nil:
		return fmt.Errorf("%w: %w", errNoReply, err)
	}
	return nil
}

// readServerError reads the fields of the server's refusal, each a code
// byte and a string ending in a zero byte, with one more zero byte last. It
// returns an error only for a refusal that says the server is starting up.
func readServerError(body io.Reader) error {
	fields, err := io.ReadAll(body)
	if err != nil {
		return fmt.Errorf("%w: %w", errNoReply, err)
	}
	refused := &startingUpError{}
	for len(fields) > 0 && fields[0] != 0 {
		code := fields[0]
		value, rest, found := bytes.Cut(fields[1:], []byte{0})
		if !found {
			return fmt.Errorf("%w: an error field did not end", errNotPostgres)
		}
		refused.set(code, string(value))
		fields = rest
	}
	if len(fields) == 0 {
		return fmt.Errorf("%w: an error reply was cut short", errNotPostgres)
	}
	if refused.Code != sqlstateStartingUp {
		return nil
	}
	return refused
}

// set records the SQLSTATE and the message.
func (e *startingUpError) set(code byte, value string) {
	switch code {
	case 'C':
		e.Code = value
	case 'M':
		e.Message = value
	}
}
