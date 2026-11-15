package mongod

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/tophergopher/mongotest/dockerclient"
)

// These read text this package does not write: a process listing from the
// daemon, DOCKER_HOST from the environment, and the user an image declares.
// They are inside the package because none of it is reachable with arbitrary
// bytes through Start.

func FuzzMongodFlags(f *testing.F) {
	for _, seed := range []string{
		"mongod --bind_ip_all",
		"mongod --bind_ip=127.0.0.1 -f /etc/mongod.conf --auth",
		"/usr/bin/mongod --bind_ip localhost,/tmp/mongod.sock --fork",
		"bash /usr/local/bin/docker-entrypoint.sh mongod",
		"mongod --bind_ip",
		"",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, command string) {
		parsed, isMongod := mongodFlags(command)
		_ = parsed.listensBeyondLoopback()
		_ = parsed.enforcesAuth()

		for name := range parsed {
			assert.False(t, strings.HasPrefix(name, "-"), "a flag is kept by its name, without its dashes: %q", name)
		}
		assert.Equal(t, isMongod, parsed != nil, "and only a mongod has flags to read")
	})
}

func FuzzCommands(f *testing.F) {
	f.Add("PID\tCMD", "1\tmongod --bind_ip_all")
	f.Add("COMMAND\tPID\tUSER", "mongod --bind_ip_all\t1\tmongodb")
	f.Add("", "mongod")
	f.Add("PID\tCMD\tUSER", "1")
	f.Fuzz(func(t *testing.T, titles, row string) {
		cells := strings.Split(row, "\t")
		top := dockerclient.Top{Titles: strings.Split(titles, "\t"), Processes: [][]string{cells}}

		for _, command := range commands(top) {
			assert.Contains(t, cells, command, "a command is one of the cells the daemon listed, never anything else")
		}
	})
}

func FuzzRemoteHost(f *testing.F) {
	for _, seed := range []string{
		"tcp://build-host:2376",
		"ssh://deploy@build-host",
		"http://build-host:2375/v1",
		"tcp://[fd00::1]:2376",
		"tcp://0.0.0.0:2375",
		"unix:///var/run/docker.sock",
		"mock",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, dockerHost string) {
		scheme, hostPart := splitDockerHost(dockerHost)
		host := remoteHost(hostPart)

		assert.Equal(t, strings.ToLower(scheme), scheme, "a scheme is compared in lower case")
		assert.NotEmpty(t, host, "and there is always a host to dial, this machine if the address names none")
	})
}

func FuzzKeyFileOwner(f *testing.F) {
	for _, seed := range []string{"", "1001:0", "mongod", "mongodb:mongodb", "-5", "0"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, imageUser string) {
		assert.GreaterOrEqual(t, keyFileOwner(imageUser), 0, "a file cannot be owned by a negative user")
	})
}
