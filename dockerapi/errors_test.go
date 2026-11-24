package dockerapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tophergopher/dbtest/dockermock"
)

func TestStatusErrorMapping(t *testing.T) {
	mk := func(code int, body string) *StatusError {
		resp := &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body))}
		return newStatusError(resp, "GET", "/containers/x/json")
	}
	e := mk(404, `{"message":"No such container: x"}`)
	assert.ErrorIs(t, e, ErrNotFound, "404 must match ErrNotFound")
	assert.True(t, IsNotFound(e), "IsNotFound is the convenience for 404")
	assert.NotErrorIs(t, e, ErrConflict, "404 must not match ErrConflict")
	assert.Equal(t, StatusError{StatusCode: 404, Message: "No such container: x", Method: "GET", Path: "/containers/x/json"}, *e,
		"the error carries what was asked and what the daemon said")
	assert.Contains(t, e.Error(), e.Message, "the message carries what the daemon said")
	assert.NotEmpty(t, e.Hint(), "a 404 must carry a hint about what to check")

	c := mk(409, `{"message":"in use"}`)
	assert.ErrorIs(t, c, ErrConflict, "409 must match ErrConflict")
	assert.NotErrorIs(t, c, ErrNotFound, "409 must not match ErrNotFound")
	assert.NotEmpty(t, c.Hint(), "a 409 must carry a retry hint")

	u := mk(401, `{"message":"unauthorized"}`)
	assert.ErrorIs(t, u, ErrUnauthorized, "401 must match ErrUnauthorized")
	assert.ErrorIs(t, mk(403, ""), ErrUnauthorized, "403 must match ErrUnauthorized")
	assert.NotEmpty(t, u.Hint(), "an auth failure must say what to do")

	raw := mk(500, "<html>oops</html>")
	assert.Equal(t, "<html>oops</html>", raw.Message, "a non-JSON body is kept verbatim as the message")
	assert.Contains(t, raw.Error(), "500", "the status code must appear in the message")
	assert.NotEmpty(t, raw.Hint(), "a 500 must tell the user where to look")
	hints := map[string]bool{e.Hint(): true, c.Hint(): true, u.Hint(): true, raw.Hint(): true}
	assert.Len(t, hints, 4, "a 404, 409, 401 and 500 each get their own hint")

	assert.Equal(t, "Bad Gateway", mk(502, "").Message, "an empty body falls back to the status text")
	assert.Equal(t, "the daemon gave no message", mk(368, "").Message, "an unregistered status code with no body still gets a message")
	assert.Len(t, mk(500, strings.Repeat("x", 2*maxErrorMessage)).Message, maxErrorMessage, "long bodies are trimmed to the byte cap so an HTML error page from a proxy cannot fill a screen")

	wrapped := &wrapErr{err: mk(404, "")}
	assert.True(t, IsNotFound(wrapped), "wrapped status errors must still match through errors.Is")
}

type wrapErr struct{ err error }

func (w *wrapErr) Error() string { return "wrapped: " + w.err.Error() }
func (w *wrapErr) Unwrap() error { return w.err }

// The two most common reasons a container fails to start, in the daemon's
// words. Each ends with the part a reader needs, the cause or the fix, so a
// short cap cuts it off.
const (
	portInUseMessage = "failed to set up container networking: driver failed programming external connectivity on endpoint mongotest-142b9626 (55af60e4cb6279dfdb15e8011417b3719c213f1c7416c329161d4247d0acb7ac): failed to bind host port 127.0.0.1:39065/tcp: address already in use"
	nameInUseMessage = `Conflict. The container name "/mongotest-142b9626" is already in use by container "994d366dd8a63feef82f8cd344cd46a78395f036757529d11093534ac83760e0". You have to remove (or rename) that container to be able to reuse that name.`
)

func TestStatusErrorKeepsTheDaemonsWholeMessage(t *testing.T) {
	fd := newDaemon(t)
	fd.ServeVersion("1.54", "1.40")
	fd.Handle("POST", "/containers/{id}/start", func(w http.ResponseWriter, _ *http.Request) {
		dockermock.Error(w, http.StatusInternalServerError, portInUseMessage)
	})
	fd.Handle("POST", "/containers/create", func(w http.ResponseWriter, _ *http.Request) {
		dockermock.Error(w, http.StatusConflict, nameInUseMessage)
	})
	c, err := New(WithHost(fd.Host()))
	require.NoError(t, err, "constructing a client against the fake daemon must succeed")

	var status *StatusError
	err = c.ContainerStart(context.Background(), "abc123")
	require.True(t, errors.As(err, &status), "a refused start must carry a StatusError")
	assert.Equal(t, portInUseMessage, status.Message, "a port already in use must keep the cause, which the daemon puts last")

	_, _, err = c.ContainerCreate(context.Background(), "mongotest-142b9626", ContainerConfig{Image: "mongo:8"})
	require.True(t, errors.As(err, &status), "a refused create must carry a StatusError")
	assert.Equal(t, nameInUseMessage, status.Message, "a duplicate name must keep the daemon's whole message, which says what to do")
}
