package mobyclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"

	"github.com/tophergopher/dbtest/dockerclient"
)

// daemonErrorPrefix is what the moby client's checkResponseErr prepends to
// every daemon-reported message ("Error response from daemon: <message>").
// Trimming it leaves the same bare message dockerapi reports, so a caller
// sees identical text whichever client produced the error.
const daemonErrorPrefix = "Error response from daemon: "

// execUpgradeFailed matches the plain error the moby client returns when an
// exec start does not get the 101 upgrade it asked for. Unlike every other
// failure path, this one is not classified with containerd/errdefs, because
// the moby client builds it before the response is otherwise interpreted;
// the status code is still in the message, so it is recovered here.
var execUpgradeFailed = regexp.MustCompile(`^unable to upgrade to \w+, received (\d+)$`)

// mapError turns an error the moby client returned for one request into the
// shared dockerclient sentinels and carriers, so callers branch with
// errors.Is regardless of which client produced the failure. method and
// path describe the request that failed, for StatusError and ResponseError
// to report; host is the daemon address, for ConnectionError to report.
func mapError(host, method, path string, err error) error {
	if err == nil {
		return nil
	}
	// Context errors are returned as-is so callers can compare them
	// directly, exactly as dockerclient.WrapConnectionError does.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	switch {
	case cerrdefs.IsNotFound(err):
		return statusError(http.StatusNotFound, method, path, err)
	case cerrdefs.IsConflict(err):
		return statusError(http.StatusConflict, method, path, err)
	case cerrdefs.IsUnauthorized(err):
		return statusError(http.StatusUnauthorized, method, path, err)
	case cerrdefs.IsPermissionDenied(err):
		return refusal(method, path, err)
	case cerrdefs.IsInvalidArgument(err):
		// Go's answer to plain HTTP on a TLS port is a 400 too.
		return dockerclient.WrapConnectionError(host, statusError(http.StatusBadRequest, method, path, err))
	case cerrdefs.IsResourceExhausted(err):
		return statusError(http.StatusTooManyRequests, method, path, err)
	case cerrdefs.IsUnavailable(err):
		return statusError(http.StatusServiceUnavailable, method, path, err)
	case cerrdefs.IsInternal(err):
		// The daemon reports a registry's failure, a rate limit included,
		// as a 500 whose message is the registry's answer. Kept a
		// StatusError, as dockerapi does, so that message can be read.
		return statusError(http.StatusInternalServerError, method, path, err)
	}
	if causeDropped(err) {
		// Only the moby client's message tells a denied socket from a refused
		// or timed-out connection. A denied socket gets fs.ErrPermission back,
		// so it is reported as dockerapi reports it.
		if strings.Contains(err.Error(), "permission denied") {
			return dockerclient.WrapConnectionError(host, fmt.Errorf("%w (%w)", err, fs.ErrPermission))
		}
		return dockerclient.ConnectionFailed(host, "the connection was refused or timed out",
			"Is the docker daemon running? Start it, or set DOCKER_HOST to a daemon that is", err)
	}
	// Not a classified daemon response: it may be that the daemon could not
	// be reached at all. WrapConnectionError inspects the error chain
	// itself (fs.ErrNotExist, a timed-out net.Error, a DNS failure, ...) so
	// this works whichever moby code path produced the error, and returns
	// err unchanged when it does not recognise the cause.
	// WrapConnectionError signals "not a connection problem" by returning
	// its input unchanged, so the test is whether it produced a
	// ConnectionError. It cannot be a == comparison: two interface values
	// of the same uncomparable dynamic type panic at run time, and an
	// error type is free to be a struct carrying a slice.
	wrapped := dockerclient.WrapConnectionError(host, err)
	var connErr *dockerclient.ConnectionError
	if errors.As(wrapped, &connErr) {
		return wrapped
	}
	if m := execUpgradeFailed.FindStringSubmatch(err.Error()); m != nil {
		code, _ := strconv.Atoi(m[1])
		return &dockerclient.StatusError{StatusCode: code, Method: method, Path: path,
			Message: "the daemon did not upgrade the exec stream to a raw connection"}
	}
	if client.IsErrConnectionFailed(err) {
		// The moby client's name for any request it could not complete,
		// such as an upload the daemon cut off.
		return dockerclient.RequestFailed(method, path, host, err)
	}
	return &dockerclient.ResponseError{Method: method, Path: path, Err: err,
		Problem: "the docker daemon returned an error mobyclient could not classify",
		Fix:     "Check `docker version` against this client and any DOCKER_API_VERSION pin; the moby client's own message is above"}
}

// refusal builds the StatusError for a 403, and says whether a proxy in
// front of the daemon sent it. The daemon puts its own refusals in its JSON
// envelope, which the moby client reports as the bare message. A proxy's
// HTML page is reported as it is, and an empty body or a JSON body with no
// message gets the moby client's own sentence instead.
func refusal(method, path string, err error) *dockerclient.StatusError {
	e := statusError(http.StatusForbidden, method, path, err)
	e.FromProxy = proxyMessage(e.Message)
	return e
}

// proxyMessage reports whether the moby client's message for a 403 came
// from something other than the daemon, by the rule dockerapi applies to the
// body: an HTML page, an empty body, or JSON without the daemon's message.
// The moby client passes a body through as it is unless its Content-Type is
// exactly application/json, and otherwise replaces an empty body, broken
// JSON or JSON with no message with a sentence of its own. A plain-text body
// cannot be told from the daemon's own message, so it is not counted.
func proxyMessage(msg string) bool {
	if strings.HasPrefix(msg, "{") {
		return !hasDaemonMessage(msg)
	}
	for _, mark := range []string{"<", "request returned ", "API returned a ", "error reading JSON"} {
		if strings.HasPrefix(msg, mark) {
			return true
		}
	}
	return false
}

// hasDaemonMessage reports whether body is the daemon's JSON error, with a
// message in it.
func hasDaemonMessage(body string) bool {
	var e struct {
		Message string `json:"message"`
	}
	return json.Unmarshal([]byte(body), &e) == nil && e.Message != ""
}

// causeDropped reports whether err is a moby connection failure with no
// network error behind it. The moby client replaces a refused or timed-out
// connection, and a socket it cannot dial or may not open, with a message
// that wraps nothing. Its other connection failures keep the transport's
// error, and some of them, such as a broken upload, happen after the daemon
// was reached.
func causeDropped(err error) bool {
	return client.IsErrConnectionFailed(err) && !errors.As(err, new(net.Error))
}

// statusError builds a dockerclient.StatusError for a classified daemon
// response, with the daemon's own message and no "Error response from
// daemon:" prefix.
func statusError(code int, method, path string, err error) *dockerclient.StatusError {
	return &dockerclient.StatusError{StatusCode: code, Method: method, Path: path, Message: daemonMessage(err)}
}

// daemonMessage strips the moby client's own wrapping prefix, leaving the
// daemon's message exactly as dockerapi reports it.
func daemonMessage(err error) string {
	return strings.TrimPrefix(err.Error(), daemonErrorPrefix)
}
