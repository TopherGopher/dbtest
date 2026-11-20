package mongod_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/dbtest/dockerclient"
	"github.com/tophergopher/dbtest/dockermock"
	"github.com/tophergopher/dbtest/mongod"
)

// tlsStartTimeout bounds a mocked start with TLS, so a readiness check that
// cannot speak TLS fails the test quickly instead of waiting a minute.
const tlsStartTimeout = 5 * time.Second

// copied is what a start put into the container with CopyToContainer.
type copied struct {
	mu    sync.Mutex
	dir   string
	files map[string][]byte // by name relative to dir
}

// record keeps a copy's files, and returns the one holding a certificate
// and its key, if any.
func (into *copied) record(dir string, files []dockerclient.File) (tls.Certificate, bool) {
	into.mu.Lock()
	defer into.mu.Unlock()
	into.dir = dir
	var pair tls.Certificate
	found := false
	for _, f := range files {
		into.files[f.Name] = f.Content
		if c, err := tls.X509KeyPair(f.Content, f.Content); err == nil {
			pair, found = c, true
		}
	}
	return pair, found
}

// file returns the copied file a path inside the container names.
func (into *copied) file(t *testing.T, pathInContainer string) []byte {
	t.Helper()
	into.mu.Lock()
	defer into.mu.Unlock()
	name, inside := strings.CutPrefix(pathInContainer, into.dir+"/")
	require.True(t, inside, "%s is under the directory the start copied to", pathInContainer)
	content, ok := into.files[name]
	require.True(t, ok, "the start copied %s into the container", pathInContainer)
	return content
}

// recordingCopies makes a mocked daemon keep what is copied into the
// container, and answer each copy with serve's result.
func recordingCopies(m *dockermock.Mock, serve func(tls.Certificate)) *copied {
	into := &copied{files: map[string][]byte{}}
	m.CopyToContainerFunc = func(_ context.Context, _, dir string, files []dockerclient.File) error {
		if pair, ok := into.record(dir, files); ok && serve != nil {
			serve(pair)
		}
		return nil
	}
	return into
}

// tlsMock is readyMock for a container started with TLS: once the start
// copies its certificate in, the stand-in for mongod answers over TLS with
// it, as mongod would.
func tlsMock(t *testing.T) (*dockermock.Mock, *copied) {
	t.Helper()
	return tlsMockServing(t, enforcingMongod)
}

// tlsMockServing is tlsMock with the given stand-in for mongod.
func tlsMockServing(t *testing.T, standIn func(net.Conn)) (*dockermock.Mock, *copied) {
	t.Helper()
	m, _ := readyMock(t)
	var mu sync.Mutex
	port := 0
	into := recordingCopies(m, func(pair tls.Certificate) {
		mu.Lock()
		defer mu.Unlock()
		port = tlsListenerPort(t, pair, standIn)
	})
	m.ContainerInspectFunc = func(context.Context, string) (dockerclient.ContainerInspect, error) {
		mu.Lock()
		defer mu.Unlock()
		if port == 0 {
			return dockerclient.ContainerInspect{}, errors.New("test: inspected before the certificate was copied in")
		}
		return running(port), nil
	}
	return m, into
}

// tlsListenerPort serves mongod's answer over TLS with a certificate.
func tlsListenerPort(t *testing.T, pair tls.Certificate, standIn func(net.Conn)) int {
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{pair}})
	if err != nil {
		t.Errorf("the stand-in for mongod cannot listen: %v", err)
		return 0
	}
	t.Cleanup(func() { _ = ln.Close() })
	go acceptAll(ln, standIn)
	return ln.Addr().(*net.TCPAddr).Port
}

// startTLS starts a container with TLS against a mocked daemon.
func startTLS(t *testing.T, m *dockermock.Mock, opts mongod.Options) *mongod.Container {
	t.Helper()
	return mustStart(t, m, opts.WithTLS().WithStartTimeout(tlsStartTimeout))
}

// firstCertificate parses the first certificate in a PEM file.
func firstCertificate(t *testing.T, pemFile []byte) *x509.Certificate {
	t.Helper()
	for block, rest := pem.Decode(pemFile); block != nil; block, rest = pem.Decode(rest) {
		if block.Type == "CERTIFICATE" {
			cert, err := x509.ParseCertificate(block.Bytes)
			require.NoError(t, err, "the certificate parses")
			return cert
		}
	}
	require.Fail(t, "the file holds no certificate")
	return nil
}

// signedBy reports whether the server certificate in one file is signed by
// the CA in another, for a name.
func signedBy(t *testing.T, serverPEM, caPEM []byte, name string) error {
	t.Helper()
	roots := x509.NewCertPool()
	roots.AddCert(firstCertificate(t, caPEM))
	_, err := firstCertificate(t, serverPEM).Verify(x509.VerifyOptions{Roots: roots, DNSName: name})
	return err
}

func TestStartWaitsForMongodOverTLS(t *testing.T) {
	m, _ := tlsMock(t)

	_, err := start(t, m, mongod.DefaultOptions().WithTLS().WithStartTimeout(tlsStartTimeout))

	assert.NoError(t, err, "a mongod that speaks only TLS is ready once it answers over TLS")
}

// Something on the port with a certificate this start did not make is not
// this container's mongod, however well it answers.
func TestAServerWithAnotherCertificateIsNotTheContainersMongod(t *testing.T) {
	m, _ := readyMock(t)
	stranger := httptest.NewTLSServer(nil)
	t.Cleanup(stranger.Close)
	port := stranger.Listener.Addr().(*net.TCPAddr).Port
	m.ContainerInspectFunc = func(context.Context, string) (dockerclient.ContainerInspect, error) {
		return running(port), nil
	}

	_, err := start(t, m, mongod.DefaultOptions().WithTLS().WithStartTimeout(300*time.Millisecond))

	require.ErrorIs(t, err, mongod.ErrNotReady, "the start does not accept it")
	assert.ErrorIs(t, err, mongod.ErrNotMongod, "because what answered is not this container's mongod")
}

// The credentials check goes over TLS too: a mongod that answers without
// credentials is refused, and one that enforces them is ready.
func TestTheCredentialsCheckWorksOverTLS(t *testing.T) {
	enforcing, _ := tlsMockServing(t, enforcingMongod)
	startTLS(t, enforcing, mongod.DefaultOptions().WithAuth("admin", "secret"))

	open, _ := tlsMockServing(t, openMongod)
	_, err := start(t, open, mongod.DefaultOptions().WithTLS().WithAuth("admin", "secret").WithStartTimeout(300*time.Millisecond))
	assert.ErrorIs(t, err, mongod.ErrAuthNotEnforced, "an open mongod behind TLS is refused")
}

func TestWithTLSMakesMongodRequireIt(t *testing.T) {
	m, into := tlsMock(t)

	startTLS(t, m, mongod.DefaultOptions())

	cmd := created(t, m).Cmd
	mode, _ := flagValue(cmd, "--tlsMode")
	assert.Equal(t, "requireTLS", mode, "mongod refuses a connection without TLS")
	keyFile, passed := flagValue(cmd, "--tlsCertificateKeyFile")
	require.True(t, passed, "mongod is given a certificate")
	_, err := tls.X509KeyPair(into.file(t, keyFile), into.file(t, keyFile))
	assert.NoError(t, err, "certificate and key in one file")
	// Whether mongod accepts these flags is for the integration test to show.
	caFile, passed := flagValue(cmd, "--tlsCAFile")
	require.True(t, passed, "mongod 7.0.10 and later need a CA file")
	assert.NoError(t, signedBy(t, into.file(t, keyFile), into.file(t, caFile), "localhost"), "naming the CA that signed its certificate")
}

func TestTheCertificateIsInPlaceBeforeMongodStarts(t *testing.T) {
	m, _ := tlsMock(t)

	startTLS(t, m, mongod.DefaultOptions())

	methods := m.Methods()
	copiedAt := indexOf(methods, "CopyToContainer")
	require.GreaterOrEqual(t, copiedAt, 0, "the certificate is copied into the container")
	assert.Less(t, copiedAt, indexOf(methods, "ContainerStart"), "before mongod starts and reads it")
}

func indexOf(s []string, v string) int {
	for i, e := range s {
		if e == v {
			return i
		}
	}
	return -1
}

func TestAFailedCertificateCopyRemovesTheContainer(t *testing.T) {
	m, _ := readyMock(t)
	refused := errors.New("the daemon refused the archive")
	m.CopyToContainerFunc = func(context.Context, string, string, []dockerclient.File) error { return refused }

	_, err := start(t, m, mongod.DefaultOptions().WithTLS())

	var startErr *mongod.StartError
	require.ErrorAs(t, err, &startErr, "the failure is reported as a step of the start")
	assert.Equal(t, mongod.OpTLSCertificate, startErr.Op, "naming the step")
	assert.ErrorIs(t, err, refused, "and wrapping the daemon's error")
	assert.Empty(t, m.CallsTo("ContainerStart"), "a mongod without its certificate is not started")
	assert.Len(t, m.CallsTo("ContainerRemove"), 1, "and the container is removed")
}

// A remote daemon's containers are dialled by its hostname, and one reached
// through a gateway by an address other than loopback. The certificate has
// to name whichever it is, or a client that verifies it refuses the server.
// Nothing listens there, so the start runs out of time after the copy.
func TestTheServerCertificateNamesTheAddressACallerDials(t *testing.T) {
	for _, host := range []string{"docker.build.example", siblingGateway, "fd00::1"} {
		t.Run(host, func(t *testing.T) {
			m := &dockermock.Mock{
				ContainerCreateFunc: func(context.Context, string, dockerclient.ContainerConfig) (string, []string, error) {
					return containerID, nil, nil
				},
				ContainerInspectFunc: func(context.Context, string) (dockerclient.ContainerInspect, error) {
					return running(1), nil
				},
			}
			into := recordingCopies(m, nil)

			_, err := start(t, m, mongod.DefaultOptions().WithTLS().WithHostIP(host).WithStartTimeout(20*time.Millisecond))

			require.ErrorIs(t, err, mongod.ErrNotReady, "nothing answers at %s", host)
			cmd := created(t, m).Cmd
			keyFile, _ := flagValue(cmd, "--tlsCertificateKeyFile")
			caFile, _ := flagValue(cmd, "--tlsCAFile")
			for _, name := range []string{host, "localhost", "127.0.0.1", "::1"} {
				assert.NoError(t, signedBy(t, into.file(t, keyFile), into.file(t, caFile), name),
					"a client that trusts the CA accepts the server as %s", name)
			}
			assert.Error(t, signedBy(t, into.file(t, keyFile), into.file(t, caFile), "example.com"),
				"but not as a name the certificate was not made for")
		})
	}
}

func TestTLSConfigTrustsTheContainersCertificate(t *testing.T) {
	m, into := tlsMock(t)
	c := startTLS(t, m, mongod.DefaultOptions())
	keyFile, _ := flagValue(created(t, m).Cmd, "--tlsCertificateKeyFile")

	_, err := firstCertificate(t, into.file(t, keyFile)).Verify(x509.VerifyOptions{Roots: c.TLSConfig().RootCAs, DNSName: c.Host()})

	assert.NoError(t, err, "a client given TLSConfig accepts the server at the address it dials")
}

func TestTheURIAsksForTLS(t *testing.T) {
	m, _ := tlsMock(t)
	c := startTLS(t, m, mongod.DefaultOptions())

	uri, err := url.Parse(c.URI())

	require.NoError(t, err, "the URI parses")
	assert.Equal(t, "true", uri.Query().Get("tls"), "a driver given the URI connects with TLS")
	assert.Equal(t, "true", uri.Query().Get("directConnection"), "and still to this one server")
}

func TestAContainerWithoutTLSHasNoTLSConfig(t *testing.T) {
	m, _ := readyMock(t)
	into := recordingCopies(m, nil)
	c := mustStart(t, m, mongod.DefaultOptions())

	uri, err := url.Parse(c.URI())

	require.NoError(t, err, "the URI parses")
	assert.Empty(t, uri.Query().Get("tls"), "a plain container's URI does not ask for TLS")
	assert.Nil(t, c.TLSConfig(), "and there is nothing to configure a client with")
	for name, content := range into.files {
		_, err := tls.X509KeyPair(content, content)
		assert.Error(t, err, "and no certificate is copied in, but %s holds one", name)
	}
}

func TestEachTLSConfigIsTheCallersOwn(t *testing.T) {
	m, _ := tlsMock(t)
	c := startTLS(t, m, mongod.DefaultOptions())
	stranger := httptest.NewTLSServer(nil)
	t.Cleanup(stranger.Close)

	changed := c.TLSConfig()
	changed.ServerName = "changed"
	changed.RootCAs.AddCert(stranger.Certificate())

	fresh := c.TLSConfig()
	assert.Empty(t, fresh.ServerName, "changing one config does not change the next")
	_, err := stranger.Certificate().Verify(x509.VerifyOptions{Roots: fresh.RootCAs})
	assert.Error(t, err, "nor does trusting another CA in it")
}

func TestTheShellUsesTLSWhenMongodRequiresIt(t *testing.T) {
	t.Run("with TLS", func(t *testing.T) {
		m, into := tlsMock(t)
		c := startTLS(t, m, mongod.DefaultOptions())
		keyFile, _ := flagValue(created(t, m).Cmd, "--tlsCertificateKeyFile")
		commands := shell(m, dockerclient.ExecResult{Stdout: "1"}, nil)

		_, err := c.RunScript(context.Background(), "1")

		require.NoError(t, err, "the script runs")
		require.NotEmpty(t, *commands, "in a shell")
		for _, cmd := range *commands {
			assert.Contains(t, cmd, "--tls", "the shell connects with TLS")
			caFile, _ := flagValue(cmd, "--tlsCAFile")
			assert.NoError(t, signedBy(t, into.file(t, keyFile), into.file(t, caFile), "127.0.0.1"),
				"trusting the CA, at the dialled address")
		}
	})
	t.Run("without TLS", func(t *testing.T) {
		m, _ := readyMock(t)
		c := mustStart(t, m, mongod.DefaultOptions())
		commands := shell(m, dockerclient.ExecResult{Stdout: "1"}, nil)

		_, err := c.RunScript(context.Background(), "1")

		require.NoError(t, err, "the script runs")
		for _, cmd := range *commands {
			assert.NotContains(t, cmd, "--tls", "the shell asks for TLS only when mongod requires it")
		}
	})
}

func TestAtlasLocalCannotBeGivenACertificate(t *testing.T) {
	m, _ := readyMock(t)

	_, err := start(t, m, mongod.DefaultOptions().WithMongoImage(mongod.ImageAtlasLocal).WithTLS())

	require.ErrorIs(t, err, mongod.ErrUnsupportedForImage, "the image takes no mongod flags, so it cannot be told to use TLS")
	var unsupported *mongod.UnsupportedForImageError
	require.ErrorAs(t, err, &unsupported, "the error names the option")
	assert.Equal(t, "WithTLS", unsupported.Option, "to remove")
	assert.Empty(t, m.CallsTo("ContainerCreate"), "and nothing is created")
}

// TLS has to work for real: the certificate has to satisfy mongod, the
// entrypoint has to create the user with it, and the shell inside has to
// trust it. The legacy shell takes the same flags.
func TestIntegrationWithTLS(t *testing.T) {
	legacy := mongod.ImagePublicECR.WithVersion("4.4")
	docker, ctx := liveDocker(t)
	requireImage(t, ctx, docker, legacy)
	for name, opts := range map[string]mongod.Options{
		"standalone":                             mongod.DefaultOptions().WithTLS(),
		"replica set with credentials":           mongod.DefaultOptions().WithTLS().WithReplicaSet("rs0").WithAuth("admin", "s3cret"),
		"legacy shell, replica set, credentials": mongod.DefaultOptions().WithTLS().WithMongoImage(legacy).WithReplicaSet("rs0").WithAuth("admin", "s3cret"),
	} {
		t.Run(name, func(t *testing.T) {
			docker, ctx := liveDocker(t)
			key, value := runLabel(t)
			c, err := mongod.StartWithOptions(ctx, opts.WithDocker(docker).WithLabel(key, value))
			require.NoError(t, err, "mongod integration: cannot start a container with TLS")
			t.Cleanup(func() { _ = c.Stop(context.Background()) })
			address := net.JoinHostPort(c.Host(), strconv.Itoa(c.Port()))

			conn, err := tls.Dial("tcp", address, c.TLSConfig())
			require.NoError(t, err, "a client that trusts TLSConfig completes a handshake with mongod")
			_ = conn.Close()
			assert.Error(t, askHello(address), "a connection without TLS is rejected")
			writable, err := c.RunScript(ctx, "db.hello().isWritablePrimary")
			require.NoError(t, err, "the shell inside the container connects with TLS")
			assert.Equal(t, "true", writable, "and the server accepts writes")
		})
	}
}
