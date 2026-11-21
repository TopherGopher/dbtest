package dockerclient_test

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tophergopher/dbtest/dockerclient"
)

// The validators and error carriers here are shared by every implementation
// of Client, so these tests pin the behaviour all of them inherit.

func TestParseImageRef(t *testing.T) {
	good := []struct{ ref, name, tag, digest string }{
		{"mongo", "mongo", "latest", ""},
		{"mongo:8", "mongo", "8", ""},
		{"mongo:8.0-noble", "mongo", "8.0-noble", ""},
		{"library/mongo:8", "library/mongo", "8", ""},
		{"docker.io/library/mongo:8", "docker.io/library/mongo", "8", ""},
		{"localhost:5000/mongo", "localhost:5000/mongo", "latest", ""},
		{"localhost:5000/team/mongo:8.0", "localhost:5000/team/mongo", "8.0", ""},
		{"GHCR.io/org-name/img:v1", "GHCR.io/org-name/img", "v1", ""}, // uppercase allowed in the registry host only
		{"mongo:8/json", "mongo:8/json", "latest", ""},                // legal: host "mongo:8", repository "json"
		{"[::1]:5000/mongo", "[::1]:5000/mongo", "latest", ""},
		{"mongo@sha256:" + strings.Repeat("a", 64), "mongo", "", "sha256:" + strings.Repeat("a", 64)},
		{"mongo:8@sha256:" + strings.Repeat("0", 64), "mongo", "8", "sha256:" + strings.Repeat("0", 64)},
		{"my__image.name-x", "my__image.name-x", "latest", ""},
	}
	for _, tc := range good {
		r, err := dockerclient.ParseImageRef(tc.ref)
		require.NoError(t, err, "%q is a valid reference", tc.ref)
		assert.Equal(t, tc.name, r.Name, "%q: repository part", tc.ref)
		assert.Equal(t, tc.tag, r.Tag, "%q: tag (latest when absent and no digest)", tc.ref)
		assert.Equal(t, tc.digest, r.Digest, "%q: digest", tc.ref)
	}
	bad := []string{
		"", " ", "Mongo", "mongo:8:9", "mongo:", ":8", "mongo/", "/mongo", "mongo//x", "mongo:8 ", "mongo:-8",
		"mongo@sha256:short", "mongo@notadigest", "mon go", "mongo:" + strings.Repeat("t", 129),
		strings.Repeat("a", 256), "mongo?x=1", "mongo#1", "ghcr.io/Org-Name/img:v1",
	}
	for _, ref := range bad {
		_, err := dockerclient.ParseImageRef(ref)
		require.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "%q must be rejected as an invalid argument", ref)
		var invalid *dockerclient.InvalidArgumentError
		require.ErrorAs(t, err, &invalid, "%q: the error says what was refused", ref)
		assert.NotEmpty(t, invalid.Fix, "%q: the error must explain the reference grammar", ref)
	}
	_, err := dockerclient.ParseImageRef("Mongo:8")
	// This checks wording on purpose: the problem repeats the daemon's own
	// message, so a user who searches for it finds the same answers.
	assert.Contains(t, err.Error(), "lowercase", "an uppercase repository gets the daemon's familiar wording")
}

func TestCheckID(t *testing.T) {
	for _, in := range []string{"abc123", "  abc123 ", "/mongotest-1", "mongotest_1.x-y", "de686f1e8de9bcabdc4b20b2d9b80d33f90ed79aa7588ad907d953ad63dc9d3c"} {
		got, err := dockerclient.CheckID("container", in)
		require.NoError(t, err, "%q is an acceptable id", in)
		assert.NotEmpty(t, got, "%q: a cleaned id is returned", in)
		assert.False(t, strings.ContainsAny(got, " /"), "%q: whitespace and the leading slash are stripped, got %q", in, got)
	}
	for _, in := range []string{"", "   ", "a/b", "../x", "abc?force=1", "abc#", "-abc", ".abc", "a b", "a\nb"} {
		_, err := dockerclient.CheckID("container", in)
		require.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "%q must be rejected so it cannot alter the URL path", in)
	}
	_, err := dockerclient.CheckID("exec", "")
	var invalid *dockerclient.InvalidArgumentError
	require.ErrorAs(t, err, &invalid, "the error says what was refused")
	assert.Equal(t, "exec id", invalid.Argument, "the error must name the kind of id that was empty")
	assert.NotEmpty(t, invalid.Fix, "the error must say where a valid id comes from")
}

func TestValidateContainerConfig(t *testing.T) {
	ok := dockerclient.ContainerConfig{
		Image:        "mongo:8",
		Labels:       map[string]string{"mongotest": "regression"},
		ExposedPorts: map[string]struct{}{"27017/tcp": {}, "27018": {}},
		HostConfig: &dockerclient.HostConfig{PortBindings: map[string][]dockerclient.PortBinding{
			"27017/tcp": {{HostIP: "127.0.0.1", HostPort: "34819"}, {HostIP: "", HostPort: ""}, {HostPort: "40000-40010"}},
			"53/udp":    {{HostIP: "::1", HostPort: "5353"}},
		},
			Volumes: []dockerclient.VolumeMount{{Name: "cart-tests", Path: "/data/db"}, {Name: "cache_1.v2", Path: "/data/cache", ReadOnly: true}}},
	}
	require.NoError(t, ok.Validate(), "a config with valid ports, bindings, volumes and labels must pass")
	bad := map[string]dockerclient.ContainerConfig{
		"no image":          {},
		"bad image":         {Image: "Mongo"},
		"empty label key":   {Image: "mongo", Labels: map[string]string{"": "x"}},
		"exposed no number": {Image: "mongo", ExposedPorts: map[string]struct{}{"tcp": {}}},
		"exposed port zero": {Image: "mongo", ExposedPorts: map[string]struct{}{"0/tcp": {}}},
		"exposed too big":   {Image: "mongo", ExposedPorts: map[string]struct{}{"70000/tcp": {}}},
		"bad proto":         {Image: "mongo", ExposedPorts: map[string]struct{}{"27017/icmp": {}}},
		"binding key":       {Image: "mongo", HostConfig: &dockerclient.HostConfig{PortBindings: map[string][]dockerclient.PortBinding{"x/tcp": {{}}}}},
		"binding host ip":   {Image: "mongo", HostConfig: &dockerclient.HostConfig{PortBindings: map[string][]dockerclient.PortBinding{"27017/tcp": {{HostIP: "localhost"}}}}},
		"binding host port": {Image: "mongo", HostConfig: &dockerclient.HostConfig{PortBindings: map[string][]dockerclient.PortBinding{"27017/tcp": {{HostPort: "abc"}}}}},
		"binding range":     {Image: "mongo", HostConfig: &dockerclient.HostConfig{PortBindings: map[string][]dockerclient.PortBinding{"27017/tcp": {{HostPort: "40010-40000"}}}}},
		"empty cmd element": {Image: "mongo", Cmd: []string{"--replSet", ""}},
		"host directory":    volumeAt("/srv/data", "/data/db"),
		"relative path":     volumeAt("./data", "/data/db"),
		"empty volume name": volumeAt("", "/data/db"),
		"windows path":      volumeAt(`C:\data`, "/data/db"),
		"one-letter name":   volumeAt("C", "/data/db"),
		"relative target":   volumeAt("cart-tests", "data/db"),
		"no target":         volumeAt("cart-tests", ""),
	}
	for name, cfg := range bad {
		err := cfg.Validate()
		require.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "%s: must be rejected as an invalid argument", name)
		var ia *dockerclient.InvalidArgumentError
		require.True(t, errors.As(err, &ia), "%s: the typed error must be extractable", name)
		assert.NotEmpty(t, ia.Fix, "%s: every validation error must say what to do", name)
	}
}

func TestInvalidArgumentErrorMessage(t *testing.T) {
	err := dockerclient.InvalidArgument("container id", "a/b", "only letters are allowed", "Pass the id from ContainerCreate")
	assert.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "every InvalidArgumentError matches the sentinel")
	for _, part := range []string{err.Argument, err.Value, err.Problem, err.Fix} {
		assert.Contains(t, err.Error(), part, "the message carries the argument, value, problem and fix")
	}
	var ia *dockerclient.InvalidArgumentError
	require.True(t, errors.As(err, &ia), "callers can extract the typed error")
	assert.Equal(t, "a/b", ia.Value, "the offending value is available to callers")
	assert.Same(t, dockerclient.ErrNoCommand, dockerclient.ErrNoCommand, "predeclared argument errors are comparable values")
	assert.ErrorIs(t, dockerclient.ErrNoCommand, dockerclient.ErrInvalidArgument, "predeclared argument errors also match the sentinel")
}

func TestConnectionErrorWrapping(t *testing.T) {
	err := dockerclient.WrapConnectionError("unix:///var/run/docker.sock", fs.ErrPermission)
	require.ErrorIs(t, err, dockerclient.ErrConnectionFailed, "permission denied on the socket is a connection failure")
	assert.ErrorIs(t, err, fs.ErrPermission, "the cause is kept")
	var ce *dockerclient.ConnectionError
	require.True(t, errors.As(err, &ce), "callers can extract the typed error")
	assert.Equal(t, "unix:///var/run/docker.sock", ce.Host, "the host is carried on the error")
	assert.NotEmpty(t, ce.Fix, "the fix must be stated")

	err = dockerclient.WrapConnectionError("unix:///x.sock", fs.ErrNotExist)
	assert.ErrorIs(t, err, dockerclient.ErrConnectionFailed, "a missing socket is a connection failure")
	assert.ErrorIs(t, err, fs.ErrNotExist, "the cause is kept")
	require.True(t, errors.As(err, &ce), "callers can extract the typed error")
	assert.NotEmpty(t, ce.Fix, "the fix must say to start the daemon")
	var denied *dockerclient.ConnectionError
	require.ErrorAs(t, dockerclient.WrapConnectionError("unix:///x.sock", fs.ErrPermission), &denied, "a denied socket is a connection error")
	assert.NotEqual(t, denied.Fix, ce.Fix, "a denied socket and a missing one each get their own fix")

	assert.Nil(t, dockerclient.WrapConnectionError("h", nil), "nil stays nil")
	plain := errors.New("something else")
	assert.Same(t, plain, dockerclient.WrapConnectionError("h", plain), "errors that are not connection problems pass through unchanged")
}

// A client that rejects the daemon's certificate sends no request, so the
// failure is a connection failure, and the certificate error stays as its
// cause.
func TestAnUntrustedDaemonCertificateIsAConnectionFailure(t *testing.T) {
	untrusted := &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}

	err := dockerclient.WrapConnectionError("tcp://build-host:2376", fmt.Errorf("Get \"https://build-host:2376/version\": %w", untrusted))

	assert.ErrorIs(t, err, dockerclient.ErrConnectionFailed, "no request was sent to an untrusted daemon")
	var unknownCA x509.UnknownAuthorityError
	assert.ErrorAs(t, err, &unknownCA, "the certificate problem is kept as the cause")
}

func TestAPIVersionErrorMessages(t *testing.T) {
	feature := &dockerclient.APIVersionError{Feature: "ExecConfig.WorkingDir", Required: "1.35", Negotiated: "1.30"}
	assert.ErrorIs(t, feature, dockerclient.ErrAPIVersion, "feature gates match the sentinel")
	assert.Contains(t, feature.Error(), "1.35", "the required version is named")
	assert.Contains(t, feature.Error(), "1.30", "the negotiated version is named")
	assert.Contains(t, feature.Error(), "ExecConfig.WorkingDir", "the feature is named")
}

func TestResponseErrorMessage(t *testing.T) {
	err := dockerclient.DecodeError("GET", "/containers/x/json", io.ErrUnexpectedEOF)
	assert.ErrorIs(t, err, dockerclient.ErrDaemonResponse, "decode failures match the sentinel")
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF, "the underlying decode error is wrapped")
	assert.NotEmpty(t, err.Fix, "the error says what to check")
	assert.Contains(t, dockerclient.UnexpectedStatus("POST", "/x", 202).Error(), "202", "unexpected statuses are named")
}

// Daemons report a registry's rate limit in three ways: as a 429 status, as
// a 500 whose message carries the registry's 429, or as the last message in
// the pull's stream. A caller must be able to tell it from other failures
// without reading the message itself.
func TestARateLimitMatchesErrRateLimitedHoweverItArrives(t *testing.T) {
	limited := map[string]error{
		"a 429 status": &dockerclient.StatusError{StatusCode: 429, Message: "rate limited"},
		"the registry's 429 when resolving the tag": &dockerclient.StatusError{StatusCode: 500,
			Message: `unknown: failed to resolve reference "docker.io/library/mongo:8.0": unexpected status from HEAD request to https://registry-1.docker.io/v2/library/mongo/manifests/8.0: 429 Too Many Requests`},
		"in the stream": &dockerclient.PullError{Ref: "mongo:8.0",
			Message: "error from registry: You have reached your unauthenticated pull rate limit. https://www.docker.com/increase-rate-limit"},
		"in the stream, from an older daemon": &dockerclient.PullError{Ref: "mongo:8.0",
			Message: "toomanyrequests: You have reached your pull rate limit. You may increase the limit by authenticating and upgrading"},
		"in the stream, as only the registry's code": &dockerclient.PullError{Ref: "public.ecr.aws/docker/library/mongo:8.0",
			Message: "toomanyrequests: Rate exceeded"},
		"ECR Public's data limit, in the stream": &dockerclient.PullError{Ref: "public.ecr.aws/docker/library/mongo:8.0",
			Message: "error from registry: Data limit exceeded"},
		"ECR Public's data limit, when resolving the tag": &dockerclient.StatusError{StatusCode: 500,
			Message: "error from registry: Data limit exceeded"},
	}
	for name, err := range limited {
		assert.ErrorIs(t, err, dockerclient.ErrRateLimited, "%s is a rate limit", name)
	}
	assert.ErrorIs(t, limited["in the stream"], dockerclient.ErrPull, "a rate-limited pull still matches ErrPull")

	notLimited := map[string]error{
		"access denied": &dockerclient.StatusError{StatusCode: 500,
			Message: "pull access denied, repository does not exist or may require authorization: authorization failed: no basic auth credentials"},
		"a missing tag":      &dockerclient.StatusError{StatusCode: 404, Message: "manifest for mongo:0.1 not found"},
		"a dropped download": &dockerclient.PullError{Ref: "mongo:8.0", Message: "short read: expected 100 bytes but got 50: unexpected EOF"},
	}
	for name, err := range notLimited {
		assert.NotErrorIs(t, err, dockerclient.ErrRateLimited, "%s is not a rate limit", name)
	}
}

// A docker context saves its daemon's TLS files where `docker context
// create` puts them. ResolveDaemon finds them for the context discovery
// uses, and only then, with that context's address.
func TestResolveDaemonFindsTheChosenContextsFiles(t *testing.T) {
	setup := func(t *testing.T, withTLS bool) (configDir, tlsDir string) {
		for _, name := range []string{"DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CERT_PATH", "DOCKER_TLS_VERIFY"} {
			t.Setenv(name, "")
		}
		configDir = t.TempDir()
		t.Setenv("DOCKER_CONFIG", configDir)
		sum := sha256.Sum256([]byte("remote"))
		id := hex.EncodeToString(sum[:])
		meta := filepath.Join(configDir, "contexts", "meta", id)
		require.NoError(t, os.MkdirAll(meta, 0o755), "creating the context")
		require.NoError(t, os.WriteFile(filepath.Join(meta, "meta.json"),
			[]byte(`{"Name":"remote","Endpoints":{"docker":{"Host":"tcp://build-host:2376"}}}`), 0o644), "writing the context")
		tlsDir = filepath.Join(configDir, "contexts", "tls", id, "docker")
		if withTLS {
			require.NoError(t, os.MkdirAll(tlsDir, 0o700), "creating the context's TLS directory")
			require.NoError(t, os.WriteFile(filepath.Join(tlsDir, "ca.pem"), []byte("ca"), 0o600), "writing ca.pem")
		}
		return configDir, tlsDir
	}

	t.Run("a context with TLS files", func(t *testing.T) {
		_, want := setup(t, true)
		t.Setenv("DOCKER_CONTEXT", "remote")
		daemon, err := dockerclient.ResolveDaemon()
		require.NoError(t, err, "the context can be read")
		assert.Equal(t, dockerclient.Daemon{Host: "tcp://build-host:2376", ContextTLSDir: want}, daemon,
			"the context's address and TLS directory are found together")
	})
	t.Run("the current context in config.json", func(t *testing.T) {
		configDir, want := setup(t, true)
		require.NoError(t, os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"currentContext":"remote"}`), 0o644), "writing config.json")
		daemon, err := dockerclient.ResolveDaemon()
		require.NoError(t, err, "the context can be read")
		assert.Equal(t, dockerclient.Daemon{Host: "tcp://build-host:2376", ContextTLSDir: want}, daemon,
			"the current context's address and TLS directory are found together")
	})
	t.Run("a context with no TLS files", func(t *testing.T) {
		setup(t, false)
		t.Setenv("DOCKER_CONTEXT", "remote")
		daemon, err := dockerclient.ResolveDaemon()
		require.NoError(t, err, "the context can be read")
		assert.Equal(t, dockerclient.Daemon{Host: "tcp://build-host:2376"}, daemon, "a context without TLS files has none to use")
	})
	t.Run("DOCKER_HOST overrides the context", func(t *testing.T) {
		setup(t, true)
		t.Setenv("DOCKER_CONTEXT", "remote")
		t.Setenv("DOCKER_HOST", "tcp://other:2376")
		daemon, err := dockerclient.ResolveDaemon()
		require.NoError(t, err, "nothing needs reading")
		assert.Equal(t, dockerclient.Daemon{Host: "tcp://other:2376"}, daemon, "the context is not used, so neither are its files")
	})
	t.Run("a context that skips TLS verification with no TLS files", func(t *testing.T) {
		configDir, _ := setup(t, false)
		sum := sha256.Sum256([]byte("remote"))
		require.NoError(t, os.WriteFile(filepath.Join(configDir, "contexts", "meta", hex.EncodeToString(sum[:]), "meta.json"),
			[]byte(`{"Name":"remote","Endpoints":{"docker":{"Host":"tcp://build-host:2376","SkipTLSVerify":true}}}`), 0o644), "writing the context")
		t.Setenv("DOCKER_CONTEXT", "remote")
		_, err := dockerclient.ResolveDaemon()
		assert.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "dbtest never skips verification, and will not send plain HTTP instead")
	})
	t.Run("a ca.pem that cannot be read", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symbolic links need extra rights on Windows")
		}
		_, tlsDir := setup(t, true)
		ca := filepath.Join(tlsDir, "ca.pem")
		require.NoError(t, os.Remove(ca), "removing ca.pem")
		require.NoError(t, os.Symlink(ca, ca), "making ca.pem a link to itself, which even root cannot read")
		t.Setenv("DOCKER_CONTEXT", "remote")
		_, err := dockerclient.ResolveDaemon()
		assert.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "going on without the files could send requests in the clear")
	})
	t.Run("a broken config.json", func(t *testing.T) {
		configDir, _ := setup(t, true)
		require.NoError(t, os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{not json`), 0o644), "writing config.json")
		_, err := dockerclient.ResolveDaemon()
		assert.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "a configuration that cannot be read is a problem to fix")
	})
}

// Go's answer to plain HTTP on its TLS port is only recognised for a request
// sent to a TCP address. Through a socket the same 400 is the daemon's.
func TestGoTLSAnswerIsRecognisedOnlyOverTCP(t *testing.T) {
	answer := &dockerclient.StatusError{StatusCode: 400, Message: "Client sent an HTTP request to an HTTPS server.", Method: "GET", Path: "/version"}

	assert.ErrorIs(t, dockerclient.WrapConnectionError("tcp://build-host:2376", answer), dockerclient.ErrConnectionFailed,
		"over TCP it means the daemon only accepts TLS")
	assert.Same(t, answer, dockerclient.WrapConnectionError("unix:///var/run/docker.sock", answer),
		"through a socket it is the daemon's own answer")
}

// volumeAt is a config that mounts the named volume at path.
func volumeAt(name, path string) dockerclient.ContainerConfig {
	return dockerclient.ContainerConfig{Image: "mongo", HostConfig: &dockerclient.HostConfig{
		Volumes: []dockerclient.VolumeMount{{Name: name, Path: path}},
	}}
}
