package mongod

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"time"

	"github.com/tophergopher/mongotest/dockerclient"
)

// Where the TLS files go inside the container. They are copied in before
// mongod starts, because a bind mount does not reach a remote daemon.
const (
	tlsCopyDir    = "/etc"
	tlsDirName    = "mongo-tls"
	tlsCAPath     = tlsCopyDir + "/" + tlsDirName + "/ca.pem"
	tlsServerPath = tlsCopyDir + "/" + tlsDirName + "/server.pem"
)

// certificateLifetime is how long the certificates are valid: longer than
// any test run, short enough that a leaked one soon stops working.
const certificateLifetime = 24 * time.Hour

// certificates are a container's own CA and a server certificate it signed.
type certificates struct {
	caPEM     []byte
	serverPEM []byte // the certificate, then its key, as mongod reads them
	server    tls.Certificate
	roots     *x509.CertPool
}

// newCertificates makes a CA and a server certificate for mongod, in
// memory. The server certificate names the loopback addresses the shell
// inside the container uses, and host, the address a caller dials.
func newCertificates(host string) (certificates, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return certificates{}, err
	}
	now := time.Now()
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "mongotest CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(certificateLifetime),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return certificates{}, err
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return certificates{}, err
	}
	return serverCertificate(ca, caKey, host)
}

// serverCertificate makes the server certificate, signed by the CA.
func serverCertificate(ca *x509.Certificate, caKey *ecdsa.PrivateKey, host string) (certificates, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return certificates{}, err
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: ca.NotBefore, NotAfter: ca.NotAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	if ip := net.ParseIP(host); ip != nil {
		template.IPAddresses = append(template.IPAddresses, ip)
	} else {
		template.DNSNames = append(template.DNSNames, host)
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		return certificates{}, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return certificates{}, err
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	return certificates{
		caPEM:     pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw}),
		serverPEM: append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})...),
		server:    tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key},
		roots:     roots,
	}, nil
}

// tlsConfig returns a client configuration that trusts only the CA. Each
// call returns a new one, so a caller can change it freely.
func (c certificates) tlsConfig() *tls.Config {
	return &tls.Config{RootCAs: c.roots.Clone(), MinVersion: tls.VersionTLS12}
}

// copyInto puts the CA and the server certificate into a container that is
// created but not started. mongod does not check who can read them.
func (c certificates) copyInto(ctx context.Context, docker dockerclient.Client, id string) error {
	return docker.CopyToContainer(ctx, id, tlsCopyDir, []dockerclient.File{
		{Name: tlsDirName + "/ca.pem", Mode: 0o644, Content: c.caPEM},
		{Name: tlsDirName + "/server.pem", Mode: 0o644, Content: c.serverPEM},
	})
}

// TLSConfig returns a client configuration that trusts the container's
// certificate, or nil when the container was started without WithTLS. Pass
// it to the driver along with URI. Each call returns a new one.
func (c *Container) TLSConfig() *tls.Config {
	if c.certs == nil {
		return nil
	}
	return c.certs.tlsConfig()
}

// certificatesFor makes the certificates for a start with WithTLS, or none.
func (c config) certificatesFor(host string) (*certificates, error) {
	if !c.tls {
		return nil, nil
	}
	certs, err := newCertificates(host)
	if err != nil {
		return nil, fmt.Errorf("mongod: cannot make the TLS certificates: %w", err)
	}
	return &certs, nil
}
