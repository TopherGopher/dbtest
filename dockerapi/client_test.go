package dockerapi

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tophergopher/dbtest/dockermock"
)

func TestNewRequiresValidHost(t *testing.T) {
	fixes := map[string]bool{}
	for _, host := range []string{"bogus", "npipe:////./pipe/docker_engine"} {
		_, err := New(WithHost(host))
		require.ErrorIs(t, err, ErrInvalidArgument, "%s must be rejected as an invalid argument", host)
		var invalid *InvalidArgumentError
		require.ErrorAs(t, err, &invalid, "%s: the error says what was refused", host)
		assert.Equal(t, host, invalid.Value, "the error quotes the host it refused")
		assert.NotEmpty(t, invalid.Fix, "%s: the error says what to use instead", host)
		fixes[invalid.Fix] = true
	}
	assert.Len(t, fixes, 2, "a named pipe gets its own fix, not the one for a host with no scheme")
}

func TestNewUsesDiscoveryWhenNoHostOption(t *testing.T) {
	fd := newDaemon(t)
	t.Setenv("DOCKER_HOST", fd.Host())
	t.Setenv("DOCKER_CONTEXT", "")
	c, err := FromEnv()
	require.NoError(t, err, "FromEnv must succeed when DOCKER_HOST points at a listening socket")
	assert.Equal(t, fd.Host(), c.Host(), "the client must report the DOCKER_HOST it discovered")
}

// createBody is what the fake create handler saw.
type createBody struct {
	Image string `json:"Image"`
}

func roundTrip(t *testing.T, fd *dockermock.Daemon, opts ...Option) {
	t.Helper()
	fd.ServeVersion("1.54", "1.40")
	fd.Handle("POST", "/containers/create", func(w http.ResponseWriter, r *http.Request) {
		if r.Host != DummyHost && !strings.HasPrefix(fd.Host(), "tcp://") {
			dockermock.Error(w, 400, "unexpected Host header "+r.Host)
			return
		}
		dockermock.JSON(w, 201, createResponse{ID: "abc123", Warnings: []string{}})
	})
	c, err := New(append([]Option{WithHost(fd.Host()), WithUserAgent("ua-test/1")}, opts...)...)
	require.NoError(t, err, "constructing a client against the fake daemon must succeed")

	resp, err := c.do(context.Background(), http.MethodPost, "/containers/create",
		url.Values{"name": {"mongotest 1"}}, createBody{Image: "mongo:8"})
	require.NoError(t, err, "the round trip to the fake daemon must succeed")
	defer resp.Body.Close()
	require.Equal(t, 201, resp.StatusCode, "the fake answers create with 201")

	var out createResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out), "the create response must decode")
	assert.Equal(t, "abc123", out.ID, "the id from the fake must round-trip")

	reqs := fd.Requests()
	require.Len(t, reqs, 2, "exactly two requests are expected: /version negotiation, then the create")
	assert.Equal(t, "/version", reqs[0].RawPath, "the first request must be the unversioned negotiation call")
	r := reqs[1]
	assert.Equal(t, "POST", r.Method, "create is a POST")
	assert.Equal(t, "/containers/create", r.Path, "path without the version prefix")
	assert.Equal(t, "/v1.44/containers/create", r.RawPath, "the negotiated version must prefix the path")
	assert.Equal(t, "mongotest 1", r.Query.Get("name"), "query values must be URL-encoded and decoded intact")
	assert.Equal(t, "application/json", r.Header.Get("Content-Type"), "JSON bodies must be labelled")
	assert.Equal(t, "ua-test/1", r.Header.Get("User-Agent"), "WithUserAgent must be honoured")
	var sent createBody
	require.NoError(t, json.Unmarshal(r.Body, &sent), "the request body must be JSON")
	assert.Equal(t, "mongo:8", sent.Image, "the body must carry the encoded struct")
}

func TestRoundTripUnixSocket(t *testing.T) {
	fd := newDaemon(t)
	roundTrip(t, fd)
	got := fd.Requests()[1].Header.Get("Host")
	assert.True(t, got == "" || got == DummyHost, "unix socket requests must use the placeholder host, got %q", got)
}

func TestRoundTripTCP(t *testing.T) {
	roundTrip(t, newDaemon(t, dockermock.OverTCP()))
}

func TestDoWithoutBodyHasNoContentType(t *testing.T) {
	fd := newDaemon(t)
	fd.ServeVersion("1.44", "")
	c, err := New(WithHost(fd.Host()))
	require.NoError(t, err, "client construction")
	resp, err := c.do(context.Background(), http.MethodGet, "/version", nil, nil)
	require.NoError(t, err, "GET /version through do must succeed")
	resp.Body.Close()
	first := fd.Requests()[0]
	assert.Empty(t, first.Header.Get("Content-Type"), "a body-less request must not claim a content type")
	assert.True(t, strings.HasPrefix(first.Header.Get("User-Agent"), "dbtest"), "the default user agent identifies dbtest, got %q", first.Header.Get("User-Agent"))
}

func TestWithHTTPClientIsUsedAsIs(t *testing.T) {
	fd := newDaemon(t, dockermock.OverTCP())
	fd.Handle("GET", "/_ping", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	fd.ServeVersion("1.54", "1.40")
	var seen bool
	hc := &http.Client{Transport: mockRoundTripper(func(r *http.Request) (*http.Response, error) {
		seen = true
		return http.DefaultTransport.RoundTrip(r)
	})}
	c, err := New(WithHost(fd.Host()), WithHTTPClient(hc))
	require.NoError(t, err, "client construction with a custom http.Client")
	resp, err := c.do(context.Background(), http.MethodGet, "/_ping", nil, nil)
	require.NoError(t, err, "ping through the custom client")
	resp.Body.Close()
	assert.True(t, seen, "WithHTTPClient must be the transport actually used for requests")
}

func TestWithLoggerReceivesRequestRecords(t *testing.T) {
	fd := newDaemon(t)
	fd.ServeVersion("1.54", "1.40")
	fd.Handle("GET", "/_ping", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	rec := &recordLogger{}
	c, err := New(WithHost(fd.Host()), WithLogger(rec))
	require.NoError(t, err, "client construction with a logger")
	resp, err := c.do(context.Background(), http.MethodGet, "/_ping", nil, nil)
	require.NoError(t, err, "ping")
	resp.Body.Close()
	require.GreaterOrEqual(t, len(rec.entries), 2, "one debug record per request is expected (version, ping)")
	last := rec.entries[len(rec.entries)-1]
	assert.Equal(t, "docker request", last.msg, "request records use a fixed message so log filters can match it")
	assert.Equal(t, "/v1.44/_ping", last.kv["path"], "the record must carry the versioned path")
	assert.Equal(t, 200, last.kv["status"], "the record must carry the status code")
}

// debugRecords returns a logger at Debug that writes JSON, and a function
// that decodes what it has written so far, one map per record.
func debugRecords(t *testing.T) (*slog.Logger, func() []map[string]any) {
	t.Helper()
	var out strings.Builder
	logger := slog.New(slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return logger, func() []map[string]any {
		var records []map[string]any
		for line := range strings.Lines(out.String()) {
			var record map[string]any
			require.NoError(t, json.Unmarshal([]byte(line), &record), "each record is one JSON object")
			records = append(records, record)
		}
		return records
	}
}

// An slog logger at Debug, as the dockerapi page shows, gets a record for
// each request.
func TestAnSlogLoggerAtDebugReceivesRequestRecords(t *testing.T) {
	fd := newDaemon(t)
	fd.ServeVersion("1.54", "1.40")
	fd.Handle("GET", "/_ping", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	debug, records := debugRecords(t)
	c, err := New(WithHost(fd.Host()), WithLogger(debug))
	require.NoError(t, err, "client construction with a logger")
	resp, err := c.do(context.Background(), http.MethodGet, "/_ping", nil, nil)
	require.NoError(t, err, "ping")
	resp.Body.Close()

	pinged := false
	for _, record := range records() {
		path, _ := record["path"].(string)
		pinged = pinged || strings.HasSuffix(path, "/_ping")
	}
	assert.True(t, pinged, "the ping has its record")
}

// A request that never reaches the daemon is logged too, with the error.
func TestAnSlogLoggerAtDebugReceivesFailedRequestRecords(t *testing.T) {
	gone := dockermock.NewDaemon()
	gone.Close()
	debug, records := debugRecords(t)
	c, err := New(WithHost(gone.Host()), WithLogger(debug))
	require.NoError(t, err, "client construction with a logger")
	_, err = c.do(context.Background(), http.MethodGet, "/_ping", nil, nil)
	require.Error(t, err, "nothing listens at a closed daemon's address")

	logged := records()
	require.NotEmpty(t, logged, "the failed request has a record")
	assert.Contains(t, logged[0], "error", "the record says what went wrong")
	assert.Contains(t, logged[0], "path", "the record says which request it was")
}

// ignoreLogger drops every record but is not an *slog.Logger, so the client
// cannot know that and builds each record for it.
type ignoreLogger struct{}

func (ignoreLogger) Debug(string, ...any) {}
func (ignoreLogger) Info(string, ...any)  {}
func (ignoreLogger) Warn(string, ...any)  {}
func (ignoreLogger) Error(string, ...any) {}

// A logger that drops Debug records, such as the default or an slog logger
// at Info, must not cost every request the allocations of building one.
func TestADroppedRequestRecordIsNotBuilt(t *testing.T) {
	allocs := func(l Logger) float64 {
		c := newMockClient(t, mockStatus(http.StatusOK), WithLogger(l))
		ping := func() {
			resp, err := c.do(context.Background(), http.MethodGet, "/_ping", nil, nil)
			require.NoError(t, err, "ping")
			resp.Body.Close()
		}
		ping() // negotiates, which only the first request does
		return testing.AllocsPerRun(200, ping)
	}
	built := allocs(ignoreLogger{})
	assert.Less(t, allocs(NopLogger()), built, "the default logger")
	assert.Less(t, allocs(slog.New(slog.NewTextHandler(io.Discard, nil))), built, "an slog logger at Info")
}

// writeTLSContext makes a docker context named remote, current through
// DOCKER_CONTEXT, that reaches host with ca.pem as its TLS file.
func writeTLSContext(t *testing.T, host string, caPEM []byte) {
	t.Helper()
	for _, name := range []string{"DOCKER_HOST", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"} {
		t.Setenv(name, "")
	}
	configDir := t.TempDir()
	t.Setenv("DOCKER_CONFIG", configDir)
	t.Setenv("DOCKER_CONTEXT", "remote")
	sum := sha256.Sum256([]byte("remote"))
	id := hex.EncodeToString(sum[:])
	meta := filepath.Join(configDir, "contexts", "meta", id)
	require.NoError(t, os.MkdirAll(meta, 0o755), "creating the context")
	require.NoError(t, os.WriteFile(filepath.Join(meta, "meta.json"),
		[]byte(`{"Name":"remote","Endpoints":{"docker":{"Host":"`+host+`"}}}`), 0o644), "writing the context")
	tlsDir := filepath.Join(configDir, "contexts", "tls", id, "docker")
	require.NoError(t, os.MkdirAll(tlsDir, 0o700), "creating the context's TLS directory")
	require.NoError(t, os.WriteFile(filepath.Join(tlsDir, "ca.pem"), caPEM, 0o600), "writing ca.pem")
}

// A daemon found through a docker context with TLS files is reached over
// TLS with them, with no TLS variables set.
func TestAContextsTLSFilesAreUsed(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"ApiVersion":"1.44"}`)
	}))
	defer srv.Close()

	t.Run("a context whose ca.pem signed the daemon", func(t *testing.T) {
		writeTLSContext(t, "tcp://"+strings.TrimPrefix(srv.URL, "https://"),
			pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
		c, err := New()
		require.NoError(t, err, "a client for the context")

		resp, err := c.do(context.Background(), http.MethodGet, "/version", nil, nil)

		require.NoError(t, err, "the daemon is trusted through the context's ca.pem")
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode, "the request went over TLS")
	})
	t.Run("a context whose ca.pem holds no certificate", func(t *testing.T) {
		writeTLSContext(t, "tcp://"+strings.TrimPrefix(srv.URL, "https://"), []byte("not a certificate"))

		_, err := New()

		assert.ErrorIs(t, err, ErrInvalidArgument, "a TLS file that cannot be used is a configuration problem to fix")
	})
}

// TLS: a DOCKER_CERT_PATH directory built from an httptest TLS server.
func TestTLSFromEnv(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil {
			w.WriteHeader(400)
			return
		}
		_, _ = io.WriteString(w, `{"ApiVersion":"1.44"}`)
	}))
	defer srv.Close()

	certDir := t.TempDir()
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	require.NoError(t, os.WriteFile(filepath.Join(certDir, "ca.pem"), certPEM, 0o644), "writing ca.pem fixture")
	addr := strings.TrimPrefix(srv.URL, "https://")

	t.Run("tcp host with DOCKER_TLS_VERIFY and ca.pem only", func(t *testing.T) {
		t.Setenv("DOCKER_TLS_VERIFY", "1")
		t.Setenv("DOCKER_CERT_PATH", certDir)
		c, err := New(WithHost("tcp://" + addr))
		require.NoError(t, err, "client construction with TLS from the environment")
		resp, err := c.do(context.Background(), http.MethodGet, "/version", nil, nil)
		require.NoError(t, err, "a TLS round trip trusting ca.pem must succeed")
		defer resp.Body.Close()
		assert.Equal(t, 200, resp.StatusCode, "the TLS server answers 200 when the handshake completed")
	})

	t.Run("client certificate pair is loaded when present", func(t *testing.T) {
		key := srv.TLS.Certificates[0]
		keyDER, err := x509.MarshalPKCS8PrivateKey(key.PrivateKey)
		require.NoError(t, err, "marshalling the fixture key")
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "ca.pem"), certPEM, 0o644), "ca.pem fixture")
		require.NoError(t, os.WriteFile(filepath.Join(dir, "cert.pem"), certPEM, 0o644), "cert.pem fixture")
		require.NoError(t, os.WriteFile(filepath.Join(dir, "key.pem"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600), "key.pem fixture")
		cfg, err := tlsConfigFromEnv(env(map[string]string{"DOCKER_TLS_VERIFY": "1", "DOCKER_CERT_PATH": dir}))
		require.NoError(t, err, "a complete cert directory must load")
		require.NotNil(t, cfg, "a tls.Config is expected when DOCKER_TLS_VERIFY is set")
		assert.Len(t, cfg.Certificates, 1, "cert.pem and key.pem must be loaded as one client certificate")
		assert.NotNil(t, cfg.RootCAs, "ca.pem must populate the root pool")
	})

	t.Run("DOCKER_TLS_VERIFY alone takes the files from the Docker config directory", func(t *testing.T) {
		t.Setenv("DOCKER_CONFIG", certDir)
		cfg, err := tlsConfigFromEnv(env(map[string]string{"DOCKER_TLS_VERIFY": "1"}))
		require.NoError(t, err, "ca.pem in DOCKER_CONFIG must load")
		require.NotNil(t, cfg, "DOCKER_TLS_VERIFY turns TLS on")
		assert.NotNil(t, cfg.RootCAs, "the CA in the config directory is trusted")
	})

	t.Run("no DOCKER_TLS_VERIFY means no tls", func(t *testing.T) {
		cfg, err := tlsConfigFromEnv(env(map[string]string{"DOCKER_CERT_PATH": certDir}))
		require.NoError(t, err, "an unset DOCKER_TLS_VERIFY is not an error")
		assert.Nil(t, cfg, "without DOCKER_TLS_VERIFY no tls.Config may be built even if certificates exist")
	})

	t.Run("missing ca.pem is an error naming the path", func(t *testing.T) {
		empty := t.TempDir()
		_, err := tlsConfigFromEnv(env(map[string]string{"DOCKER_TLS_VERIFY": "1", "DOCKER_CERT_PATH": empty}))
		require.ErrorIs(t, err, ErrInvalidArgument, "a missing ca.pem is a configuration problem")
		assert.Contains(t, err.Error(), filepath.Join(empty, "ca.pem"), "the error must name the file it looked for")
		assert.Contains(t, err.Error(), "DOCKER_CERT_PATH", "the error must name the variable to fix")
	})

	t.Run("WithTLSConfig overrides the environment", func(t *testing.T) {
		t.Setenv("DOCKER_TLS_VERIFY", "1")
		t.Setenv("DOCKER_CERT_PATH", t.TempDir()) // would fail if consulted
		pool := x509.NewCertPool()
		pool.AddCert(srv.Certificate())
		c, err := New(WithHost("tcp://"+addr), WithTLSConfig(&tls.Config{RootCAs: pool}))
		require.NoError(t, err, "an explicit tls.Config must win over an unusable DOCKER_CERT_PATH")
		resp, err := c.do(context.Background(), http.MethodGet, "/version", nil, nil)
		require.NoError(t, err, "round trip with the explicit config")
		resp.Body.Close()
	})

	t.Run("https scheme implies tls with system roots when nothing else is set", func(t *testing.T) {
		t.Setenv("DOCKER_TLS_VERIFY", "")
		c, err := New(WithHost("https://" + addr))
		require.NoError(t, err, "https hosts construct without extra configuration")
		assert.True(t, strings.HasPrefix(c.baseURL, "https://"), "https:// must select a TLS base URL, got %q", c.baseURL)
	})
}
