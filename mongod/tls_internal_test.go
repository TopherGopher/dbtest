package mongod

import (
	"crypto/x509"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A remote daemon's containers are dialled by its hostname, and one reached
// through a gateway by an address other than loopback. The certificate has
// to name whichever it is, or a client that verifies it refuses the server.
// A mocked start cannot dial either, so this asks for the certificate itself.
func TestTheServerCertificateNamesTheAddressACallerDials(t *testing.T) {
	for _, host := range []string{"docker.build.example", "172.17.0.1", "fd00::1"} {
		t.Run(host, func(t *testing.T) {
			certs, err := newCertificates(host)
			require.NoError(t, err, "certificates are made")
			leaf, err := x509.ParseCertificate(certs.server.Certificate[0])
			require.NoError(t, err, "the server certificate parses")
			roots := certs.tlsConfig().RootCAs

			for _, name := range []string{host, "localhost", "127.0.0.1", "::1"} {
				_, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: name})
				assert.NoError(t, err, "a client that trusts TLSConfig accepts the server as %s", name)
			}
		})
	}
}
