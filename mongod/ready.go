package mongod

import "crypto/tls"

// readyProbe asks a started container's mongod whether it is ready: one
// attempt, which internal/container repeats until the start timeout.
type readyProbe struct {
	name string
	id   string
	// host and port are the address a caller will connect to.
	host string
	port int
	// requireAuth says the container was created with credentials, so the
	// mongod that serves must enforce them.
	requireAuth bool
	// tlsConfig is set when mongod requires TLS, and trusts its certificate.
	tlsConfig *tls.Config
}
