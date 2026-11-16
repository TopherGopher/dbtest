package dockerapi

import (
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/url"
)

// createResponse is the body of a successful POST /containers/create.
type createResponse struct {
	ID       string   `json:"Id"`
	Warnings []string `json:"Warnings"`
}

// ContainerCreate creates a container. name may be empty. The config is
// validated first (InvalidArgumentError); a missing image yields an error
// matching ErrNotFound so callers can pull and retry.
func (c *Client) ContainerCreate(ctx context.Context, name string, cfg ContainerConfig) (id string, warnings []string, err error) {
	if err := cfg.Validate(); err != nil {
		return "", nil, err
	}
	name, err = checkContainerName(name)
	if err != nil {
		return "", nil, err
	}
	var q url.Values
	if name != "" {
		q = url.Values{"name": {name}}
	}
	const path = "/containers/create"
	resp, err := c.do(ctx, http.MethodPost, path, q, cfg)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		if resp.StatusCode >= 400 {
			return "", nil, newStatusError(resp, http.MethodPost, path)
		}
		return "", nil, unexpectedStatus(http.MethodPost, path, resp.StatusCode)
	}
	var out createResponse
	if err := json.UnmarshalRead(resp.Body, &out); err != nil {
		return "", nil, decodeError(http.MethodPost, path, err)
	}
	return out.ID, out.Warnings, nil
}

// ContainerStart starts a created container. An already running container
// (304) is not an error.
func (c *Client) ContainerStart(ctx context.Context, id string) error {
	id, err := checkID("container", id)
	if err != nil {
		return err
	}
	path := "/containers/" + id + "/start"
	resp, err := c.do(ctx, http.MethodPost, path, nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNoContent, resp.StatusCode == http.StatusNotModified:
		return nil
	case resp.StatusCode >= 400:
		return newStatusError(resp, http.MethodPost, path)
	}
	return unexpectedStatus(http.MethodPost, path, resp.StatusCode)
}

// ContainerRemove deletes a container. Use RemoveOptions{Force: true} for a
// running one. A container that no longer exists yields ErrNotFound.
func (c *Client) ContainerRemove(ctx context.Context, id string, opts RemoveOptions) error {
	id, err := checkID("container", id)
	if err != nil {
		return err
	}
	path := "/containers/" + id
	q := url.Values{}
	if opts.Force {
		q.Set("force", "1")
	}
	if opts.RemoveVolumes {
		q.Set("v", "1")
	}
	resp, err := c.do(ctx, http.MethodDelete, path, q, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNoContent:
		return nil
	case resp.StatusCode >= 400:
		return newStatusError(resp, http.MethodDelete, path)
	}
	return unexpectedStatus(http.MethodDelete, path, resp.StatusCode)
}

// ContainerInspect returns the state and published ports of a container.
func (c *Client) ContainerInspect(ctx context.Context, id string) (ContainerInspect, error) {
	var out ContainerInspect
	id, err := checkID("container", id)
	if err != nil {
		return out, err
	}
	err = c.getJSON(ctx, "/containers/"+id+"/json", &out)
	return out, err
}

// ContainerLogsTo copies what a container has written so far, stdout and
// stderr each to its own writer, and returns. It does not wait for more
// output. Either writer may be nil to discard that stream.
func (c *Client) ContainerLogsTo(ctx context.Context, id string, stdout, stderr io.Writer) error {
	id, err := checkID("container", id)
	if err != nil {
		return err
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	path := "/containers/" + id + "/logs"
	resp, err := c.do(ctx, http.MethodGet, path, url.Values{"stdout": {"1"}, "stderr": {"1"}}, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode >= 400:
		return newStatusError(resp, http.MethodGet, path)
	default:
		return unexpectedStatus(http.MethodGet, path, resp.StatusCode)
	}
	// A container with a TTY has one raw stream; the daemon says so in the
	// content type. Without a TTY, stdout and stderr arrive mixed together
	// in frames.
	if resp.Header.Get("Content-Type") == rawStream {
		_, err = io.Copy(stdout, resp.Body)
		return ctxErr(ctx, err)
	}
	return ctxErr(ctx, demux(resp.Body, stdout, stderr))
}

// rawStream is the content type of a log the daemon sends unframed.
const rawStream = "application/vnd.docker.raw-stream"

// ContainerTop lists the processes running inside a container.
func (c *Client) ContainerTop(ctx context.Context, id string) (Top, error) {
	var out Top
	id, err := checkID("container", id)
	if err != nil {
		return out, err
	}
	err = c.getJSON(ctx, "/containers/"+id+"/top", &out)
	return out, err
}

// getJSON performs a GET expecting a 200 JSON body decoded into out.
func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	resp, err := c.do(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode >= 400:
		return newStatusError(resp, http.MethodGet, path)
	default:
		return unexpectedStatus(http.MethodGet, path, resp.StatusCode)
	}
	if err := json.UnmarshalRead(resp.Body, out); err != nil {
		return decodeError(http.MethodGet, path, err)
	}
	return nil
}
