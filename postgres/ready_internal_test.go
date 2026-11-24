package postgres

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// message frames a reply as the server does: its type, then its length,
// which counts itself and the body but not the type.
func message(kind byte, body string) string {
	length := binary.BigEndian.AppendUint32(nil, uint32(len(body)+4))
	return string(kind) + string(length) + body
}

// authRequest is the server asking for a password by SCRAM-SHA-256, which
// is what the official image's final server sends.
var authRequest = message('R', "\x00\x00\x00\x0aSCRAM-SHA-256\x00\x00")

// startingUp is what the server says while it is not yet accepting
// connections.
var startingUp = message('E', "SFATAL\x00C57P03\x00Mthe database system is starting up\x00\x00")

// The first reply decides whether the server that will serve the caller is
// up: anything else must keep the start waiting, not end it as ready.
func TestReadStartupReply(t *testing.T) {
	t.Run("an authentication request is ready", func(t *testing.T) {
		assert.NoError(t, readStartupReply(strings.NewReader(authRequest)), "a server that asks for a password is accepting connections")
	})
	t.Run("authentication ok is ready", func(t *testing.T) {
		trust := message('R', "\x00\x00\x00\x00")
		assert.NoError(t, readStartupReply(strings.NewReader(trust)), "an image run with trust authentication is ready too")
	})
	t.Run("starting up is not ready", func(t *testing.T) {
		assert.ErrorIs(t, readStartupReply(strings.NewReader(startingUp)), errStartingUp,
			"the server's refusal is kept, so a timeout can say why")
	})
	t.Run("any other refusal is ready", func(t *testing.T) {
		badRole := message('E', "SFATAL\x00C28000\x00Mrole \"nobody\" does not exist\x00\x00")
		assert.NoError(t, readStartupReply(strings.NewReader(badRole)),
			"a server that refuses for any reason but starting up is up, so the caller's first query reports the real cause")
	})
	t.Run("a hang-up is no reply", func(t *testing.T) {
		assert.ErrorIs(t, readStartupReply(strings.NewReader("")), errNoReply,
			"Docker accepts on a published port and hangs up while nothing listens behind it")
	})
	for name, reply := range map[string]string{
		"a header cut short":  authRequest[:3],
		"a request cut short": authRequest[:5],
		"a refusal cut short": startingUp[:12],
	} {
		t.Run(name+" is no reply", func(t *testing.T) {
			assert.ErrorIs(t, readStartupReply(strings.NewReader(reply)), errNoReply,
				"a connection that ends mid-reply is not ready, and says nothing about the address")
		})
	}
	for name, reply := range map[string]string{
		"HTTP":                                 "HTTP/1.1 400 Bad Request\r\n\r\n",
		"an unknown type":                      message('Z', "I"),
		"a length too long":                    "R\x7f\xff\xff\xff",
		"a length too short":                   "R\x00\x00\x00\x02",
		"an empty request":                     message('R', ""),
		"a request too short to hold its code": message('R', "\x00\x00"),
		"a refusal that never ends":            message('E', "C57P03"),
	} {
		t.Run(name+" is not PostgreSQL", func(t *testing.T) {
			assert.ErrorIs(t, readStartupReply(strings.NewReader(reply)), errNotPostgres,
				"something other than PostgreSQL at the address must not count as ready")
		})
	}
}

// The error's fields come from the server, which may leave any of them out.
func TestARefusalWithoutFieldsIsReady(t *testing.T) {
	assert.NoError(t, readStartupReply(strings.NewReader(message('E', "\x00"))),
		"without SQLSTATE 57P03 the server is not starting up")
}
