package mongod_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockermock"
	"github.com/tophergopher/mongotest/mongod"
)

// copied is what a start put into the container before starting it.
type copied struct {
	dir   string
	files map[string][]byte // by name relative to dir
}

// tlsMock is readyMock for a container started with TLS: the stand-in for
// mongod speaks TLS with the certificate the start copies into the
// container, as mongod would.
func tlsMock(t *testing.T) (*dockermock.Mock, *copied) {
	t.Helper()
	m, _ := readyMock(t)
	into := &copied{files: map[string][]byte{}}
	port := make(chan int, 1)
	m.CopyToContainerFunc = func(_ context.Context, _, dir string, files []dockerclient.File) error {
		into.dir = dir
		for _, f := range files {
			into.files[f.Name] = f.Content
		}
		port <- tlsListenerPort(t, into.files)
		return nil
	}
	m.ContainerInspectFunc = func(context.Context, string) (dockerclient.ContainerInspect, error) {
		p := <-port
		port <- p
		return running(p), nil
	}
	return m, into
}

// tlsListenerPort serves mongod's answer over TLS with whichever copied
// file holds a certificate and its key.
func tlsListenerPort(t *testing.T, files map[string][]byte) int {
	t.Helper()
	var cert tls.Certificate
	for _, content := range files {
		if c, err := tls.X509KeyPair(content, content); err == nil {
			cert = c
		}
	}
	require.NotEmpty(t, cert.Certificate, "one copied file holds the server's certificate and key")
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	require.NoError(t, err, "the stand-in for mongod listens")
	t.Cleanup(func() { _ = ln.Close() })
	go acceptAll(ln, answerCommand)
	return ln.Addr().(*net.TCPAddr).Port
}

// copiedFile returns the copied file a path inside the container names.
func (into *copied) file(t *testing.T, pathInContainer string) []byte {
	t.Helper()
	name, inside := strings.CutPrefix(pathInContainer, into.dir+"/")
	require.True(t, inside, "%s is under the directory the start copied to", pathInContainer)
	content, ok := into.files[name]
	require.True(t, ok, "the start copied %s into the container", pathInContainer)
	return content
}

func TestWithTLSMakesMongodRequireIt(t *testing.T) {
	m, into := tlsMock(t)

	mustStart(t, m, mongod.DefaultOptions().WithTLS())

	mode, passed := flagValue(created(t, m).Cmd, "--tlsMode")
	require.True(t, passed, "mongod is told how to treat TLS")
	assert.Equal(t, "requireTLS", mode, "and refuses a connection without it")
	keyFile, passed := flagValue(created(t, m).Cmd, "--tlsCertificateKeyFile")
	require.True(t, passed, "mongod is given a certificate")
	_, err := tls.X509KeyPair(into.file(t, keyFile), into.file(t, keyFile))
	assert.NoError(t, err, "in a file holding the certificate and its key, which mongod reads together")
}

func TestTheCertificateIsInPlaceBeforeMongodStarts(t *testing.T) {
	m, _ := tlsMock(t)

	mustStart(t, m, mongod.DefaultOptions().WithTLS())

	methods := m.Methods()
	copiedAt := slices.Index(methods, "CopyToContainer")
	require.GreaterOrEqual(t, copiedAt, 0, "the certificate is copied into the container")
	assert.Less(t, copiedAt, slices.Index(methods, "ContainerStart"), "before mongod starts and reads it")
}

func TestTheServerCertificateIsTrustedThroughTLSConfig(t *testing.T) {
	m, into := tlsMock(t)
	c := mustStart(t, m, mongod.DefaultOptions().WithTLS())
	keyFile, _ := flagValue(created(t, m).Cmd, "--tlsCertificateKeyFile")
	leaf := firstCertificate(t, into.file(t, keyFile))
	roots := c.TLSConfig().RootCAs

	for _, name := range []string{c.Host(), "localhost", "127.0.0.1"} {
		_, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: name})
		assert.NoError(t, err, "a client that trusts TLSConfig accepts the server as %s", name)
	}
	_, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: "example.com"})
	assert.Error(t, err, "but not as a name the certificate was not made for")
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

func TestTheURIAsksForTLS(t *testing.T) {
	m, _ := tlsMock(t)
	c := mustStart(t, m, mongod.DefaultOptions().WithTLS())

	uri, err := url.Parse(c.URI())

	require.NoError(t, err, "the URI parses")
	assert.Equal(t, "true", uri.Query().Get("tls"), "a driver given the URI connects with TLS")
	assert.Equal(t, "true", uri.Query().Get("directConnection"), "and still to this one server")
}

func TestAContainerWithoutTLSHasNoTLSConfig(t *testing.T) {
	m, _ := readyMock(t)
	c := mustStart(t, m, mongod.DefaultOptions())

	uri, err := url.Parse(c.URI())

	require.NoError(t, err, "the URI parses")
	assert.Empty(t, uri.Query().Get("tls"), "a plain container's URI does not ask for TLS")
	assert.Nil(t, c.TLSConfig(), "and there is nothing to configure a client with")
	assert.Empty(t, m.CallsTo("CopyToContainer"), "and no certificate is made")
}

func TestEachTLSConfigIsTheCallersOwn(t *testing.T) {
	m, _ := tlsMock(t)
	c := mustStart(t, m, mongod.DefaultOptions().WithTLS())

	c.TLSConfig().ServerName = "changed"

	assert.Empty(t, c.TLSConfig().ServerName, "changing one config does not change the next")
}

func TestTheShellUsesTLSWhenMongodRequiresIt(t *testing.T) {
	t.Run("with TLS", func(t *testing.T) {
		m, into := tlsMock(t)
		c := mustStart(t, m, mongod.DefaultOptions().WithTLS())
		commands := shell(m, dockerclient.ExecResult{Stdout: "1"}, nil)

		_, err := c.RunScript(context.Background(), "1")

		require.NoError(t, err, "the script runs")
		assert.Contains(t, (*commands)[0], "--tls", "the shell connects with TLS")
		caFile, _ := flagValue((*commands)[0], "--tlsCAFile")
		assert.NotEmpty(t, firstCertificate(t, into.file(t, caFile)), "trusting a CA the start copied into the container")
	})
	t.Run("without TLS", func(t *testing.T) {
		m, _ := readyMock(t)
		c := mustStart(t, m, mongod.DefaultOptions())
		commands := shell(m, dockerclient.ExecResult{Stdout: "1"}, nil)

		_, err := c.RunScript(context.Background(), "1")

		require.NoError(t, err, "the script runs")
		assert.NotContains(t, (*commands)[0], "--tls", "the shell asks for TLS only when mongod requires it")
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
	for name, opts := range map[string]mongod.Options{
		"standalone":                             mongod.DefaultOptions().WithTLS(),
		"replica set with credentials":           mongod.DefaultOptions().WithTLS().WithReplicaSet("rs0").WithAuth("admin", "s3cret"),
		"legacy shell, replica set, credentials": mongod.DefaultOptions().WithTLS().WithMongoImage(legacy).WithReplicaSet("rs0").WithAuth("admin", "s3cret"),
	} {
		t.Run(name, func(t *testing.T) {
			docker, ctx := liveDocker(t)
			key, value := runLabel(t)
			requireImage(t, ctx, docker, legacy)
			c, err := mongod.StartWithOptions(ctx, opts.WithDocker(docker).WithLabel(key, value))
			require.NoError(t, err, "mongod integration: cannot start a container with TLS")
			t.Cleanup(func() { _ = c.Stop(context.Background()) })
			address := net.JoinHostPort(c.Host(), strconv.Itoa(c.Port()))

			conn, err := tls.Dial("tcp", address, c.TLSConfig())
			require.NoError(t, err, "a client that trusts TLSConfig completes a handshake with mongod")
			_ = conn.Close()
			assert.Error(t, askHello(address), "a client without TLS cannot use it")
			writable, err := c.RunScript(ctx, "db.hello().isWritablePrimary")
			require.NoError(t, err, "the shell inside the container connects with TLS")
			assert.Equal(t, "true", writable, "and the server accepts writes")
		})
	}
}
