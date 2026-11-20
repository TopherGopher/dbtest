package mongod

import (
	"maps"
	"strconv"

	"github.com/tophergopher/dbtest/dockerclient"
	"github.com/tophergopher/dbtest/internal/hostaddr"
)

// containerConfig builds the create request. host is the address the caller
// will dial, which decides the interface the port is published on.
func (c config) containerConfig(host string) dockerclient.ContainerConfig {
	publishOn := "" // empty asks the daemon to choose the port
	if c.port != 0 {
		publishOn = strconv.Itoa(c.port)
	}
	return dockerclient.ContainerConfig{
		Image:        c.image.Reference(),
		Cmd:          c.command(),
		Env:          c.credentialEnv(),
		Labels:       maps.Clone(c.labels),
		ExposedPorts: map[string]struct{}{mongoPort: {}},
		HostConfig: &dockerclient.HostConfig{PortBindings: map[string][]dockerclient.PortBinding{
			mongoPort: {{HostIP: hostaddr.BindIP(host), HostPort: publishOn}},
		}},
	}
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
// and the port is about to be published where other machines can reach it.
func (c config) withCredentialsIfExposed(host string) config {
	if !c.needsGeneratedCredentials(host) {
		return c
	}
	c.username, c.password = DefaultUsername, randomHex(secretRandomBytes)
	return c
}

// announceExposure logs that other machines will be able to reach mongod.
// The same call is loopback-only on a laptop, so the change is worth saying.
func (c config) announceExposure(host string) {
	if hostaddr.BindIP(host) != hostaddr.AllInterfaces {
		return
	}
	if c.noAuth {
		c.logger.Warn("This mongod has no password, and its port is open on every network interface of the Docker host. "+
			"Anyone who can reach that host can read and change its data. Leave out WithoutAuth unless you trust the network.",
			"container", c.name)
		return
	}
	c.logger.Info("This mongod's port is open on every network interface of the Docker host, because it is reached from another machine or container. "+
		"It asks for a username and password, which Container.URI() includes.",
		"container", c.name, "username", c.username)
}

// needsGeneratedCredentials reports whether the caller chose neither
// credentials nor WithoutAuth, and the port will be on every interface.
func (c config) needsGeneratedCredentials(host string) bool {
	chosen := c.noAuth || c.username != ""
	return !chosen && hostaddr.BindIP(host) == hostaddr.AllInterfaces
}

// needsKeyFile reports whether mongod must be given a key file: a replica
// set member that authenticates will not start without one. An image that
// runs its own replica set brings its own.
func (c config) needsKeyFile() bool {
	authenticates := c.username != ""
	return authenticates && c.replicaSet != "" && c.image.acceptsMongodArgs()
}
