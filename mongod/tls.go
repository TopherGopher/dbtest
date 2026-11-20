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
	"net"
	"time"

	"github.com/tophergopher/dbtest/dockerclient"
)

// Where the TLS files go inside the container. They are copied in before
// mongod starts: a remote daemon cannot see this machine's files, so a bind
// mount would not work.
const (
	tlsCopyDir    = "/etc"
	tlsDirName    = "mongo-tls"
	tlsCAPath     = tlsCopyDir + "/" + tlsDirName + "/ca.pem"
	tlsServerPath = tlsCopyDir + "/" + tlsDirName + "/server.pem"
)

// certificateLifetime is how long the certificates are valid: longer than
// any test run, short enough that a leaked one soon stops working.
const certificateLifetime = 24 * time.Hour

// certificates are a container's own CA and a server certificate signed by
// that CA.
type certificates struct {
	caPEM []byte
	// serverPEM is the certificate followed by its key, in the one file
	// --tlsCertificateKeyFile expects.
	serverPEM []byte
	roots     *x509.CertPool
}

// newCertificates makes a CA and a server certificate for mongod, in
// memory. The server certificate is valid for localhost and the loopback
// addresses, which the shell inside the container dials, and for host, which
// a caller dials. The CA's key is dropped once it has signed, so nothing can
// sign another certificate the caller would trust.
func newCertificates(host string) (certificates, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return certificates{}, err
	}
	// Valid from an hour ago, so a container whose clock is behind this
	// machine's still accepts it.
	now := time.Now()
	caTemplate := &x509.Certificate{
		Subject:   pkix.Name{CommonName: "mongotest CA"},
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
		Subject:   pkix.Name{CommonName: "localhost"},
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
		roots:     roots,
	}, nil
}

// tlsConfig returns a client configuration that trusts only the CA. Each
// call returns a new one, so changing it affects nothing else.
func (c certificates) tlsConfig() *tls.Config {
	return &tls.Config{RootCAs: c.roots.Clone()}
}

// copyInto puts the CA and the server certificate into a container that is
// created but not started. The daemon writes them as root, so they are made
// readable by everyone for mongod to read them whatever user it runs as.
// Unlike a replica set key file, mongod accepts a certificate key others can
// read.
func (c certificates) copyInto(ctx context.Context, docker dockerclient.Client, id string) error {
	return docker.CopyToContainer(ctx, id, tlsCopyDir, []dockerclient.File{
		{Name: tlsDirName + "/ca.pem", Mode: 0o644, Content: c.caPEM},
		{Name: tlsDirName + "/server.pem", Mode: 0o644, Content: c.serverPEM},
	})
}

// TLSConfig returns the client configuration that trusts the container's
// certificate, or nil when the container was started without
// [Options.WithTLS]. Give it to the driver after [Container.URI]: applying a
// URI that says tls=true afterwards replaces it, in the official drivers.
//
//	options.Client().ApplyURI(c.URI()).SetTLSConfig(c.TLSConfig())
//
// Each call returns a new one, so changing it affects nothing else.
func (c *Container) TLSConfig() *tls.Config {
	if c.certs == nil {
		return nil
	}
	return c.certs.tlsConfig()
}

// probeTLSConfig is the configuration Start's readiness check dials with:
// TLSConfig, naming the address it dials. It is nil without TLS.
func (c *Container) probeTLSConfig() *tls.Config {
	config := c.TLSConfig()
	if config != nil {
		config.ServerName = c.host
	}
	return config
}

// certificatesFor makes the certificates when WithTLS was given, and returns
// nil otherwise.
func (c config) certificatesFor(host string) (*certificates, error) {
	if !c.tls {
		return nil, nil
	}
	certs, err := newCertificates(host)
	if err != nil {
		return nil, &StartError{Op: OpTLSCertificate, Name: c.name, Image: c.image.Reference(), Err: err}
	}
	return &certs, nil
}
