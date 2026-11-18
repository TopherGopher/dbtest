package dockerapi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// eofBody is a reply body that records whether it was read to its end. The
// HTTP client reuses a connection only if the body was.
type eofBody struct {
	io.Reader
	atEOF bool
}

func (b *eofBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if err == io.EOF {
		b.atEOF = true
	}
	return n, err
}

func (b *eofBody) Close() error { return nil }

func replyWith(body io.ReadCloser) func(*http.Request) (*http.Response, error) {
	return func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: body, Request: req}, nil
	}
}

func TestAReplyIsReadToItsEnd(t *testing.T) {
	body := &eofBody{Reader: strings.NewReader(`{"Id":"cid"}` + "\n")}
	c := newMockClient(t, replyWith(body))

	_, err := c.ContainerInspect(context.Background(), "cid")

	require.NoError(t, err, "a well-formed reply decodes")
	assert.True(t, body.atEOF, "the reply is read to its end, so the connection can carry the next request")
}

func TestAReplyWithMoreThanOneValueIsRefused(t *testing.T) {
	c := newMockClient(t, replyWith(io.NopCloser(strings.NewReader(`{"Id":"cid"} {"Id":"other"}`))))

	_, err := c.ContainerInspect(context.Background(), "cid")

	assert.ErrorIs(t, err, ErrDaemonResponse, "a reply that is not one JSON value is not what the daemon documents")
}
