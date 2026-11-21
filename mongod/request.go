package mongod

import (
	"maps"
	"strconv"

	"github.com/tophergopher/dbtest/dockerclient"
	"github.com/tophergopher/dbtest/internal/hostaddr"
)

// containerConfig builds the create request for how mongod will be
// reached.
func (c config) containerConfig(r reach) dockerclient.ContainerConfig {
	return dockerclient.ContainerConfig{
		Image:        c.image.Reference(),
		Cmd:          c.command(),
		Env:          c.credentialEnv(),
		Labels:       maps.Clone(c.labels),
		ExposedPorts: map[string]struct{}{mongoPort: {}},
		HostConfig:   c.hostConfig(r),
	}
}

// hostConfig says how the container is reached, and mounts the volume that
// keeps the data, if there is one.
func (c config) hostConfig(r reach) *dockerclient.HostConfig {
	hc := c.reachConfig(r)
	if c.volume != "" {
		hc.Binds = []string{c.volume + ":" + dataDir}
	}
	return hc
}

// reachConfig puts the container on the shared network, or publishes its
// port on the interface that the address to dial needs.
func (c config) reachConfig(r reach) *dockerclient.HostConfig {
	if r.shared() {
		return &dockerclient.HostConfig{NetworkMode: r.network}
	}
	publishOn := "" // empty asks the daemon to choose the port
	if c.port != 0 {
		publishOn = strconv.Itoa(c.port)
	}
	return &dockerclient.HostConfig{PortBindings: map[string][]dockerclient.PortBinding{
		mongoPort: {{HostIP: hostaddr.BindIP(r.host), HostPort: publishOn}},
	}}
}

// command returns the mongod flags. The image's entrypoint supplies the
// program, except in an image that takes no flags, which gets no command.
func (c config) command() []string {
	if !c.image.acceptsMongodArgs() {
		return nil
	}
	var cmd []string
	if c.replicaSet != "" {
		cmd = append(cmd, "--replSet", c.replicaSet)
	}
	if c.needsKeyFile() {
		cmd = append(cmd, "--keyFile", keyFilePath)
	}
	if c.tls {
		// mongod 7.0.10 and later will not use TLS without a CA file. With
		// one, mongod asks each client for a certificate, which drivers do
		// not send by default; the last flag lets them connect without one.
		cmd = append(cmd, "--tlsMode", "requireTLS", "--tlsCertificateKeyFile", tlsServerPath,
			"--tlsCAFile", tlsCAPath, "--tlsAllowConnectionsWithoutCertificates")
	}
	return append(cmd, c.mongodArgs...)
}

// rootCredentialEnv names the variables images read their root user from:
// the Docker official image the first pair, Atlas Local the second, and
// MongoDB's own builds either. Setting all four means nothing here has to
// know which image it is starting.
var rootCredentialEnv = [...]struct{ username, password string }{
	{"MONGO_INITDB_ROOT_USERNAME", "MONGO_INITDB_ROOT_PASSWORD"},
	{"MONGODB_INITDB_ROOT_USERNAME", "MONGODB_INITDB_ROOT_PASSWORD"},
}

// credentialEnv returns the environment that makes the image create the root
// user, or nil when there is none to create.
func (c config) credentialEnv() []string {
	if c.username == "" {
		return nil
	}
	var env []string
	for _, name := range rootCredentialEnv {
		env = append(env, name.username+"="+c.username, name.password+"="+c.password)
	}
	return env
}

// withCredentialsIfExposed generates a root user when the caller chose none
// and more than this process will be able to connect.
func (c config) withCredentialsIfExposed(r reach) config {
	if !c.needsGeneratedCredentials(r) {
		return c
	}
	c.username, c.password = DefaultUsername, randomHex(secretRandomBytes)
	return c
}

// checkVolumeCredentials refuses a volume where credentials would be
// generated: the volume keeps the first start's password, and the next
// start would generate another.
func (c config) checkVolumeCredentials(r reach) error {
	if c.volume != "" && c.needsGeneratedCredentials(r) {
		return ErrVolumeNeedsCredentials
	}
	return nil
}

// announceExposure logs that more than this process will be able to reach
// mongod. The same call is loopback-only on a laptop, so the change is worth
// saying.
func (c config) announceExposure(r reach) {
	if !r.exposed() {
		return
	}
	attrs := []any{"container", c.name}
	if r.shared() {
		attrs = append(attrs, "network", r.network)
	}
	if c.noAuth {
		c.logger.Warn("This mongod has no password, and "+r.where()+". "+
			"Anyone who can reach it can read and change its data. Leave out WithoutAuth unless you trust the network.",
			attrs...)
		return
	}
	c.logger.Info("This mongod asks for a username and password because "+r.where()+". Container.URI() includes them.",
		append(attrs, "username", c.username)...)
}

// needsGeneratedCredentials reports whether the caller chose neither
// credentials nor WithoutAuth, and more than this process can connect.
func (c config) needsGeneratedCredentials(r reach) bool {
	chosen := c.noAuth || c.username != ""
	return !chosen && r.exposed()
}

// needsKeyFile reports whether mongod must be given a key file: a replica
// set member that authenticates will not start without one. An image that
// runs its own replica set brings its own.
func (c config) needsKeyFile() bool {
	authenticates := c.username != ""
	return authenticates && c.replicaSet != "" && c.image.acceptsMongodArgs()
}
