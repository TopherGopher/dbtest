package mongod

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// The parts of MongoDB's wire protocol a hello needs. Every message starts
// with four little-endian int32s: its length, its id, the id of the message
// it answers, and its kind.
const (
	messageHeaderLen = 16
	// opMsg is the one kind of message a current mongod speaks.
	opMsg = 2013
	// helloID is the id the hello is sent with, which the reply quotes.
	helloID = 1
	// authCheckID is the id getCmdLineOpts is sent with.
	authCheckID = 2
	// helloTimeout bounds one attempt, so a port that accepts and then says
	// nothing cannot use up the whole start timeout.
	helloTimeout = 2 * time.Second
	// flagsAndKindLen is what follows the header: four bytes of flags and
	// one saying a single document comes next.
	flagsAndKindLen = 5
	// minReplyLen is the smallest reply there can be: one empty document.
	minReplyLen = messageHeaderLen + flagsAndKindLen + emptyDocumentLen
	// maxReplyLen is the most that is read of a reply. A hello's is under a
	// kilobyte, so a peer that claims more is not believed.
	maxReplyLen = 1 << 20
)

// helloElements are the two fields of {hello: 1, $db: "admin"} in BSON: an
// int32, then a string.
const helloElements = "\x10hello\x00\x01\x00\x00\x00" +
	"\x02$db\x00\x06\x00\x00\x00admin\x00"

// authCheckElements are the fields of {getCmdLineOpts: 1, $db: "admin"}, a
// command that changes nothing and needs authentication. Only whether mongod
// answers it matters, not what it says.
const authCheckElements = "\x10getCmdLineOpts\x00\x01\x00\x00\x00" +
	"\x02$db\x00\x06\x00\x00\x00admin\x00"

// wrapDocument wraps fields as BSON does: the document's own length first,
// counted here so it cannot disagree with them, and a zero last.
func wrapDocument(elements string) []byte {
	doc := binary.LittleEndian.AppendUint32(nil, uint32(len(elements)+emptyDocumentLen))
	doc = append(doc, elements...)
	return append(doc, 0)
}

// Why mongod was not found ready, inside the NotReadyError a caller sees.
// Only the first two put the address in doubt.
var (
	errNoReply      = errors.New("nothing answered a MongoDB command at the address")
	errNotMongod    = errors.New("something answered at the address, but not as mongod does")
	errHelloRefused = errors.New("mongod answered, but with an error")
	// errAuthUnknown: mongod answered the hello but did not say whether it
	// enforces the credentials.
	errAuthUnknown = errors.New("mongod did not say whether it enforces the credentials")
)

// helloMessage returns a complete hello command.
func helloMessage() []byte { return commandMessage(helloID, helloElements) }

// commandMessage returns a complete command with the given id and fields.
func commandMessage(id uint32, elements string) []byte {
	le := binary.LittleEndian
	document := wrapDocument(elements)
	msg := le.AppendUint32(nil, uint32(messageHeaderLen+flagsAndKindLen+len(document)))
	msg = le.AppendUint32(msg, id)
	msg = le.AppendUint32(msg, 0) // answers nothing
	msg = le.AppendUint32(msg, opMsg)
	msg = le.AppendUint32(msg, 0) // no flags
	msg = append(msg, 0)          // one document follows
	return append(msg, document...)
}

// ask sends mongod a hello over the published port and reads the reply. A
// connection alone proves nothing: Docker refuses connections on a published
// port for a moment after the container starts, then accepts them and hangs
// up while no mongod is listening behind it.
//
// When whileOpen is not nil, it runs once mongod has answered, and then the
// same connection sends a second hello; see helloAgain. When the container
// has credentials, the same connection then sends getCmdLineOpts without
// them, to learn whether mongod enforces them.
func (p readyProbe) ask(ctx context.Context, whileOpen func(context.Context) error) error {
	conn, err := p.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	// Closing the connection is what makes a read give up when ctx ends.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(helloTimeout))

	err = hello(conn)
	if err == nil && whileOpen != nil {
		err = helloAgain(ctx, conn, whileOpen)
	}
	if err != nil || !p.requireAuth {
		return err
	}
	return p.checkAuth(conn)
}

// hello sends a hello on conn and reads the reply.
func hello(conn net.Conn) error {
	if _, err := conn.Write(helloMessage()); err != nil {
		return fmt.Errorf("%w: %w", errNoReply, err)
	}
	return readHelloReply(conn)
}

// helloAgain runs whileOpen, then sends a second hello on the same
// connection. If the mongod that answered the first was about to be
// replaced, the connection has dropped and the second gets no reply.
func helloAgain(ctx context.Context, conn net.Conn, whileOpen func(context.Context) error) error {
	if err := whileOpen(ctx); err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Now().Add(helloTimeout))
	return hello(conn)
}

// checkAuth sends getCmdLineOpts on a connection that has not
// authenticated. A mongod that answers does not enforce the credentials.
func (p readyProbe) checkAuth(conn net.Conn) error {
	_ = conn.SetDeadline(time.Now().Add(helloTimeout))
	if _, err := conn.Write(commandMessage(authCheckID, authCheckElements)); err != nil {
		// %v, not %w: mongod answered the hello, so a failure here must not
		// match errNoReply and put the address in doubt.
		return fmt.Errorf("%w: %v", errAuthUnknown, err)
	}
	err := readAuthReply(conn)
	if errors.Is(err, errAuthOpen) {
		return &AuthNotEnforcedError{Name: p.name, ID: p.id}
	}
	return err
}

// dial connects to the published port, over TLS when mongod requires it.
func (p readyProbe) dial(ctx context.Context) (net.Conn, error) {
	dialer := net.Dialer{Timeout: helloTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr(p.host, p.port))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errNoReply, err)
	}
	if p.tlsConfig == nil {
		return conn, nil
	}
	return p.handshake(ctx, conn)
}

// handshake starts TLS on a connection. A certificate this start did not
// make means something other than its mongod answered. Any other failure,
// such as Docker hanging up before mongod listens, is no reply yet.
func (p readyProbe) handshake(ctx context.Context, conn net.Conn) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, helloTimeout)
	defer cancel()
	tlsConn := tls.Client(conn, p.tlsConfig)
	err := tlsConn.HandshakeContext(ctx)
	if err == nil {
		return tlsConn, nil
	}
	_ = conn.Close()
	var untrusted *tls.CertificateVerificationError
	if errors.As(err, &untrusted) {
		return nil, fmt.Errorf("%w: %w", errNotMongod, err)
	}
	return nil, fmt.Errorf("%w: %w", errNoReply, err)
}

// readHelloReply reads one reply and says what it means: nil for a serving
// mongod, or one of the three errors above.
func readHelloReply(peer io.Reader) error {
	reply, err := readReply(peer, helloID)
	if err != nil {
		return err
	}
	return reply.verdict()
}

// codeUnauthorized is how mongod refuses a client that has not
// authenticated.
const codeUnauthorized = "Unauthorized"

// errAuthOpen: mongod answered a client that gave no credentials.
var errAuthOpen = errors.New("mongod answered a client that gave no credentials")

// readAuthReply reads the reply to getCmdLineOpts. It returns nil when
// mongod refused as unauthorized, and errAuthOpen when mongod answered. Any
// other outcome returns errAuthUnknown, which does not put the address in
// doubt; the next poll asks again.
func readAuthReply(peer io.Reader) error {
	reply, err := readReply(peer, authCheckID)
	switch {
	case err != nil:
		// %v, not %w, for the reason given in checkAuth.
		return fmt.Errorf("%w: %v", errAuthUnknown, err)
	case reply.ok:
		return errAuthOpen
	case reply.codeName == codeUnauthorized:
		return nil
	}
	return fmt.Errorf("%w: %s: %q", errAuthUnknown,
		truncate(reply.codeName, maxCodeNameLen), truncate(reply.errmsg, maxErrmsgLen))
}

// readReply reads the reply to the command sent with id. No reply is
// errNoReply; anything else that is not a well-formed reply to it is
// errNotMongod.
func readReply(peer io.Reader, id uint32) (commandReply, error) {
	body, err := readReplyBody(peer, id)
	if err != nil {
		return commandReply{}, err
	}
	elements, wellFormed := replyElements(body)
	if !wellFormed {
		return commandReply{}, errNotMongod
	}
	reply, wellFormed := parseCommandReply(elements)
	if !wellFormed {
		return commandReply{}, errNotMongod
	}
	return reply, nil
}

// readReplyBody reads a message's header and then exactly the bytes the
// header promises. The length comes from the peer, so it is checked against
// maxReplyLen before a byte of the body is read or a buffer made for it.
func readReplyBody(peer io.Reader, id uint32) ([]byte, error) {
	var header [messageHeaderLen]byte
	if _, err := io.ReadFull(peer, header[:]); err != nil {
		return nil, headerError(err)
	}
	length := binary.LittleEndian.Uint32(header[0:4])
	if !plausibleLength(length) || !answers(header, id) {
		return nil, errNotMongod
	}
	body := make([]byte, length-messageHeaderLen)
	if _, err := io.ReadFull(peer, body); err != nil {
		return nil, fmt.Errorf("%w: the reply was cut short: %w", errNotMongod, err)
	}
	return body, nil
}

// headerError tells a peer that said nothing from one that said too little.
func headerError(err error) error {
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("%w: the reply was cut short: %w", errNotMongod, err)
	}
	return fmt.Errorf("%w: %w", errNoReply, err)
}

// plausibleLength reports whether a message length a peer states can be a
// hello's reply.
func plausibleLength(length uint32) bool {
	return length >= minReplyLen && length <= maxReplyLen
}

// answers reports whether a message header is a reply to the command sent
// with id.
func answers(header [messageHeaderLen]byte, id uint32) bool {
	le := binary.LittleEndian
	return le.Uint32(header[8:12]) == id && le.Uint32(header[12:16]) == opMsg
}

// replyElements returns the fields of the one document a reply carries,
// without the document's length and closing zero.
func replyElements(body []byte) ([]byte, bool) {
	const singleDocument = 0
	if len(body) < flagsAndKindLen+emptyDocumentLen || body[flagsAndKindLen-1] != singleDocument {
		return nil, false
	}
	return documentElements(body[flagsAndKindLen:])
}

// commandReply is what mongod said to a command.
type commandReply struct {
	hasOK    bool
	ok       bool
	codeName string
	errmsg   string
}

// parseCommandReply reads the fields a verdict needs from a reply's elements.
// It reports false for anything that is not well-formed BSON.
func parseCommandReply(elements []byte) (commandReply, bool) {
	var reply commandReply
	wellFormed := eachElement(elements, func(kind byte, name string, value []byte) {
		switch name {
		case "ok":
			reply.hasOK, reply.ok = true, isOne(kind, value)
		case "codeName":
			reply.codeName = stringValue(kind, value)
		case "errmsg":
			reply.errmsg = stringValue(kind, value)
		}
	})
	return reply, wellFormed && reply.hasOK
}

// The most of a server's own words that is quoted back to a caller.
const (
	maxErrmsgLen   = 200
	maxCodeNameLen = 64
)

// codeCommandNotFound is how a server says it does not know a command.
const codeCommandNotFound = "CommandNotFound"

// verdict turns a reply into nil or a helloRefusal.
//
// A server from before the hello command answers that it does not know it.
// That counts as ready: only a serving mongod can say so.
func (r commandReply) verdict() error {
	if r.ok || r.codeName == codeCommandNotFound {
		return nil
	}
	return &helloRefusal{codeName: truncate(r.codeName, maxCodeNameLen), errmsg: truncate(r.errmsg, maxErrmsgLen)}
}

// helloRefusal is a server's own reason for refusing the hello, cut to a
// length that is safe to quote. It matches errHelloRefused.
type helloRefusal struct {
	codeName string
	errmsg   string
}

func (e *helloRefusal) Error() string {
	return fmt.Sprintf("%v: %s: %q", errHelloRefused, e.codeName, e.errmsg)
}

func (e *helloRefusal) Is(target error) bool { return target == errHelloRefused }
