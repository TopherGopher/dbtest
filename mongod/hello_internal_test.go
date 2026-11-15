package mongod

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests are inside the package because what a reply means is told
// apart by private errors: a caller only ever sees them inside a
// NotReadyError.

// Replies as a mongod sends them, as BSON documents.
var (
	okReply    = document("\x01ok\x00\x00\x00\x00\x00\x00\x00\xf0\x3f")
	errorReply = refusal("InterruptedAtShutdown", "shutting down")
)

// replyTo wraps a document as mongod's reply to the hello.
func replyTo(document string) []byte {
	le := binary.LittleEndian
	msg := le.AppendUint32(nil, uint32(messageHeaderLen+5+len(document)))
	msg = le.AppendUint32(msg, 99)      // the reply's own id
	msg = le.AppendUint32(msg, helloID) // the request it answers
	msg = le.AppendUint32(msg, opMsg)
	msg = le.AppendUint32(msg, 0) // no flags
	msg = append(msg, 0)          // one document follows
	return append(msg, document...)
}

// document builds a BSON document from already-encoded elements.
func document(elements string) string {
	return string(binary.LittleEndian.AppendUint32(nil, uint32(len(elements)+5))) + elements + "\x00"
}

// refusal is an error reply with the given code name and message.
func refusal(codeName, message string) string {
	str := func(name, value string) string {
		return "\x02" + name + "\x00" + string(binary.LittleEndian.AppendUint32(nil, uint32(len(value)+1))) + value + "\x00"
	}
	return document("\x01ok\x00\x00\x00\x00\x00\x00\x00\x00\x00" + str("errmsg", message) + str("codeName", codeName))
}

func TestWhatAReplyToTheHelloMeans(t *testing.T) {
	oversized := binary.LittleEndian.AppendUint32(nil, maxReplyLen+1)
	oversized = append(oversized, replyTo(okReply)[4:]...)
	cases := []struct {
		name  string
		reply []byte
		want  error
	}{
		{name: "mongod says ok", reply: replyTo(okReply)},
		{name: "ok as an integer", reply: replyTo(document("\x10ok\x00\x01\x00\x00\x00"))},
		{name: "ok after other fields", reply: replyTo(document("\x08helloOk\x00\x01" + "\x01ok\x00\x00\x00\x00\x00\x00\x00\xf0\x3f"))},
		{name: "a server too old to know the command is still a mongod", reply: replyTo(refusal("CommandNotFound", "no such command: 'hello'"))},

		{name: "mongod answers with an error", reply: replyTo(errorReply), want: errHelloRefused},
		{name: "a reply with no ok field", reply: replyTo(document("\x08helloOk\x00\x01")), want: errNotMongod},
		{name: "a reply cut short", reply: replyTo(okReply)[:20], want: errNotMongod},
		{name: "a header cut short", reply: replyTo(okReply)[:7], want: errNotMongod},
		{name: "a length too large to believe", reply: oversized, want: errNotMongod},
		{name: "a length too small to hold a reply", reply: append([]byte{4, 0, 0, 0}, replyTo(okReply)[4:]...), want: errNotMongod},
		{name: "a reply to some other request", reply: bytes.Replace(replyTo(okReply), []byte{helloID, 0, 0, 0}, []byte{7, 0, 0, 0}, 1), want: errNotMongod},
		{name: "another protocol", reply: []byte("HTTP/1.1 400 Bad Request\r\n\r\n"), want: errNotMongod},
		{name: "a document that claims more than it has", reply: replyTo("\xff\x00\x00\x00\x01ok\x00"), want: errNotMongod},
		{name: "an element of a kind BSON does not have", reply: replyTo(document("\x63what\x00\x01")), want: errNotMongod},
		{name: "a string longer than the document", reply: replyTo(document("\x02errmsg\x00\xff\xff\xff\x7fx\x00")), want: errNotMongod},
		{name: "nothing at all", reply: nil, want: errNoReply},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := readHelloReply(bytes.NewReader(tc.reply))

			if tc.want == nil {
				assert.NoError(t, err, "a serving mongod answered")
				return
			}
			assert.ErrorIs(t, err, tc.want, "the three ways of not being ready are told apart")
		})
	}
}

func TestAServersErrorIsQuotedButNeverAtLength(t *testing.T) {
	long := strings.Repeat("\u20ac", 5000)
	cases := []struct {
		name     string
		reply    string
		codeName string
	}{
		{"a long message", refusal("InterruptedAtShutdown", long), "InterruptedAtShutdown"},
		{"a long name for the error", refusal(long, long), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := readHelloReply(bytes.NewReader(replyTo(tc.reply)))

			var refused *helloRefusal
			require.ErrorAs(t, err, &refused, "the server refused the command")
			if tc.codeName != "" {
				assert.Equal(t, tc.codeName, refused.codeName, "its name for the error is passed on")
			}
			assert.NotEmpty(t, refused.errmsg, "and so is what it said")
			assert.LessOrEqual(t, len(refused.codeName), maxCodeNameLen, "but a peer cannot make the name as long as it likes")
			assert.LessOrEqual(t, len(refused.errmsg), maxErrmsgLen, "nor the message")
			assert.True(t, strings.HasPrefix(long, refused.errmsg), "what is kept is the start of what it said")
			assert.True(t, utf8.ValidString(refused.codeName+refused.errmsg), "cut between characters, not through one")
		})
	}
}

// captured decodes one of capturedReplies.
func captured(t testing.TB, encoded string) []byte {
	t.Helper()
	reply, err := hex.DecodeString(encoded)
	require.NoError(t, err, "a captured reply is written in hex")
	return reply
}

func TestARealServersReplyIsUnderstood(t *testing.T) {
	for _, tc := range capturedReplies {
		t.Run(tc.name, func(t *testing.T) {
			err := readHelloReply(bytes.NewReader(captured(t, tc.hex)))

			if !tc.refused {
				assert.NoError(t, err, "a serving mongod answered, whatever else its reply carries")
				return
			}
			var refused *helloRefusal
			require.ErrorAs(t, err, &refused, "a mongod that answers with an error is not ready")
			assert.Equal(t, "InterruptedAtShutdown", refused.codeName, "and its name for the error is the one the server gave")
		})
	}
}

// The captured replies earn their place by holding the kinds of value a
// reply built by hand in these tests never has. Replacing them with ones
// that hold fewer would quietly stop testing those kinds.
func TestTheCapturedRepliesHoldTheKindsOfValueARealReplyHas(t *testing.T) {
	seen := map[byte]bool{}
	for _, tc := range capturedReplies {
		elements, wellFormed := replyElements(captured(t, tc.hex)[messageHeaderLen:])
		require.True(t, wellFormed, "%s holds one document", tc.name)
		wellFormed = eachElement(elements, func(kind byte, _ string, _ []byte) { seen[kind] = true })
		require.True(t, wellFormed, "%s is read to its end", tc.name)
	}

	kinds := map[string]byte{
		"a nested document": bsonDocument, "an array": bsonArray, "a date": bsonDateTime,
		"a timestamp": bsonTimestamp, "an ObjectID": bsonObjectID,
	}
	for name, kind := range kinds {
		assert.True(t, seen[kind], "at least one captured reply has %s among its fields", name)
	}
}

// endless is a peer that never stops sending, to show how much is read.
type endless struct{ read int }

func (e *endless) Read(p []byte) (int, error) {
	e.read += len(p)
	return len(p), nil
}

func FuzzHelloReply(f *testing.F) {
	f.Add(replyTo(okReply))
	f.Add(replyTo(errorReply))
	f.Add(replyTo(okReply)[:20])
	f.Add(append(binary.LittleEndian.AppendUint32(nil, 1<<30), replyTo(okReply)[4:]...))
	f.Add([]byte("\x8f\x02\x93\x11garbage that is not a reply\x00\xff\xfe"))
	f.Add([]byte{})
	for _, tc := range capturedReplies {
		f.Add(captured(f, tc.hex))
	}
	f.Fuzz(func(t *testing.T, reply []byte) {
		after := &endless{}
		peer := io.MultiReader(bytes.NewReader(reply), after)

		err := readHelloReply(peer)

		known := err == nil || isOneOf(err, errHelloRefused, errNotMongod, errNoReply)
		assert.True(t, known, "every reply is ok, an error from the server, not mongod, or nothing: got %v", err)
		assert.LessOrEqual(t, after.read, maxReplyLen, "a peer's stated length is never followed past the cap")
	})
}

// isOneOf reports whether err matches any of the targets.
func isOneOf(err error, targets ...error) bool {
	for _, target := range targets {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}
