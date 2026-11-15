package mongod

import (
	"context"
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

// helloDocument wraps the fields as BSON does: the document's own length
// first, counted here so it cannot disagree with them, and a zero last.
func helloDocument() []byte {
	doc := binary.LittleEndian.AppendUint32(nil, uint32(len(helloElements)+emptyDocumentLen))
	doc = append(doc, helloElements...)
	return append(doc, 0)
}

// Why a hello did not find a mongod ready, inside the NotReadyError a caller
// sees. The first two put the address in doubt; the third does not.
var (
	errNoReply      = errors.New("nothing answered a MongoDB command at the address")
	errNotMongod    = errors.New("something answered at the address, but not as mongod does")
	errHelloRefused = errors.New("mongod answered, but with an error")
)

// helloMessage returns a complete hello command.
func helloMessage() []byte {
	le := binary.LittleEndian
	document := helloDocument()
	msg := le.AppendUint32(nil, uint32(messageHeaderLen+flagsAndKindLen+len(document)))
	msg = le.AppendUint32(msg, helloID)
	msg = le.AppendUint32(msg, 0) // answers nothing
	msg = le.AppendUint32(msg, opMsg)
	msg = le.AppendUint32(msg, 0) // no flags
	msg = append(msg, 0)          // one document follows
	return append(msg, document...)
}

// hello sends mongod a command over the published port and reads the reply.
// A connection alone proves nothing: Docker accepts on a published port from
// the moment the container exists, and hangs up if no mongod is listening
// behind it.
func (p readyProbe) hello(ctx context.Context) error {
	dialer := net.Dialer{Timeout: helloTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr(p.host, p.port))
	if err != nil {
		return fmt.Errorf("%w: %w", errNoReply, err)
	}
	defer conn.Close()
	// Closing the connection is what makes a read give up when ctx ends.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(helloTimeout))

	if _, err := conn.Write(helloMessage()); err != nil {
		return fmt.Errorf("%w: %w", errNoReply, err)
	}
	return readHelloReply(conn)
}

// readHelloReply reads one reply and says what it means: nil for a serving
// mongod, or one of the three errors above.
func readHelloReply(peer io.Reader) error {
	body, err := readReplyBody(peer)
	if err != nil {
		return err
	}
	elements, wellFormed := replyElements(body)
	if !wellFormed {
		return errNotMongod
	}
	reply, wellFormed := parseHelloReply(elements)
	if !wellFormed {
		return errNotMongod
	}
	return reply.verdict()
}

// readReplyBody reads a message's header and then exactly the bytes the
// header promises. The length comes from the peer, so it is checked against
// maxReplyLen before a byte of the body is read or a buffer made for it.
func readReplyBody(peer io.Reader) ([]byte, error) {
	var header [messageHeaderLen]byte
	if _, err := io.ReadFull(peer, header[:]); err != nil {
		return nil, headerError(err)
	}
	length := binary.LittleEndian.Uint32(header[0:4])
	if !plausibleLength(length) || !answersHello(header) {
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

// answersHello reports whether a message header is a reply to the hello.
func answersHello(header [messageHeaderLen]byte) bool {
	le := binary.LittleEndian
	return le.Uint32(header[8:12]) == helloID && le.Uint32(header[12:16]) == opMsg
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

// helloReply is what mongod said to the hello.
type helloReply struct {
	hasOK    bool
	ok       bool
	codeName string
	errmsg   string
}

// parseHelloReply reads the fields a verdict needs from a reply's elements.
// It reports false for anything that is not well-formed BSON.
func parseHelloReply(elements []byte) (helloReply, bool) {
	var reply helloReply
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

// verdict turns a reply into nil or errHelloRefused.
//
// A server from before the hello command answers that it does not know it.
// That counts as ready: only a serving mongod can say so.
func (r helloReply) verdict() error {
	if r.ok || r.codeName == codeCommandNotFound {
		return nil
	}
	return fmt.Errorf("%w: %s: %q", errHelloRefused,
		truncate(r.codeName, maxCodeNameLen), truncate(r.errmsg, maxErrmsgLen))
}
