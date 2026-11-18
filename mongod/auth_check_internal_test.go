package mongod

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The credentials check is written by hand, so it is sent to a real mongod
// that lets anyone in, which a container started with no credentials is.
// The check must see it as open; a real mongod that enforces credentials is
// covered through Start by TestIntegrationWithAuthIsEnforced.
func TestIntegrationTheCredentialsCheckSeesARealOpenMongod(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	c, err := StartWithOptions(ctx, DefaultOptions())
	require.NoError(t, err, "a container with no credentials starts")
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	probe := readyProbe{name: c.Name(), id: c.ID(), host: c.Host(), port: c.Port(), requireAuth: true}
	err = probe.ask(ctx)

	var open *AuthNotEnforcedError
	assert.ErrorAs(t, err, &open, "a real mongod that answers getCmdLineOpts without credentials is seen as open")
}

// replyToAuthCheck wraps a document as mongod's reply to getCmdLineOpts.
func replyToAuthCheck(document string) []byte {
	reply := replyTo(document)
	binary.LittleEndian.PutUint32(reply[8:12], authCheckID)
	return reply
}

// The reply to the credentials check comes from whatever listens on the
// port, so any bytes must end in one of the three outcomes, and a stated
// length is never followed past the cap.
func FuzzAuthCheckReply(f *testing.F) {
	f.Add(replyToAuthCheck(okReply))
	f.Add(replyToAuthCheck(refusal(codeUnauthorized, "requires authentication")))
	f.Add(replyToAuthCheck(errorReply))
	f.Add(replyToAuthCheck(okReply)[:20])
	f.Add(replyTo(okReply)) // a reply to some other request
	f.Add([]byte("\x8f\x02\x93\x11garbage that is not a reply\x00\xff\xfe"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, reply []byte) {
		after := &endless{}
		peer := io.MultiReader(bytes.NewReader(reply), after)

		open, err := readAuthReply(peer)

		settled := err == nil
		assert.True(t, settled || errors.Is(err, errAuthUnknown), "every reply is open, enforced, or unknown: got %v", err)
		assert.False(t, open && err != nil, "an open mongod is reported without an error alongside")
		assert.False(t, isOneOf(err, errNoReply, errNotMongod), "mongod answered the hello, so nothing here puts the address in doubt")
		assert.LessOrEqual(t, after.read, maxReplyLen, "a peer's stated length is never followed past the cap")
	})
}
