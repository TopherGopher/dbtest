package mobyclient

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
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
		return statusError(http.StatusForbidden, method, path, err)
	case cerrdefs.IsInvalidArgument(err):
		return statusError(http.StatusBadRequest, method, path, err)
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
	wrapped := dockerclient.WrapConnectionError(host, restorePermission(err))
	var connErr *dockerclient.ConnectionError
	if errors.As(wrapped, &connErr) {
		return wrapped
	}
	if causeDropped(err) {
		return dockerclient.ConnectionFailed(host, "the connection was refused or timed out",
			"Is the docker daemon running? Start it, or set DOCKER_HOST to a daemon that is", err)
	}
	if m := execUpgradeFailed.FindStringSubmatch(err.Error()); m != nil {
		code, _ := strconv.Atoi(m[1])
		return &dockerclient.StatusError{StatusCode: code, Method: method, Path: path,
			Message: "the daemon did not upgrade the exec stream to a raw connection"}
	}
	return &dockerclient.ResponseError{Method: method, Path: path, Err: err,
		Problem: "the docker daemon returned an error mobyclient could not classify"}
}

// restorePermission puts fs.ErrPermission back under the moby client's error
// for a socket the user may not open, so that WrapConnectionError reports it
// as it does for dockerapi. The moby client drops that cause, and only its
// message still says what happened.
func restorePermission(err error) error {
	if causeDropped(err) && strings.Contains(err.Error(), "permission denied") {
		return fmt.Errorf("%w (%w)", err, fs.ErrPermission)
	}
	return err
}

// causeDropped reports whether err is a moby connection failure that carries
// no cause. For a refused or timed-out connection, or a Unix socket it cannot
// dial or may not open, the moby client returns a new message that wraps
// nothing, so WrapConnectionError has nothing to inspect. Its own error wraps
// that message, so a kept cause would sit one level further down. The moby
// client's other connection failures keep their cause, and some of them, such
// as a broken upload, happen after the daemon was reached. err must be the
// error the moby client returned, not wrapped further.
func causeDropped(err error) bool {
	return client.IsErrConnectionFailed(err) && errors.Unwrap(errors.Unwrap(err)) == nil
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
