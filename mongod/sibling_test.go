package mongod_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/dbtest/mongod"
)

// siblingImage is the image the helper runs in. Start pulls the same one
// for the helper's mongod, so one pull serves both.
var siblingImage = mongod.ImageDockerHub.WithVersion("8.0")

// siblingReport is what internal/siblinghelper prints.
type siblingReport struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	NetworkMode string `json:"networkMode"`
	Published   bool   `json:"published"`
	Hello       string `json:"hello"`
	Removed     bool   `json:"removed"`
}

// A test in a container on a user-defined network, with the Docker socket
// mounted, is how CI usually runs. Its start must reach mongod on that
// network with nothing published.
func TestIntegrationAStartInAContainerUsesItsNetwork(t *testing.T) {
	docker, ctx := liveDocker(t)
	socket, isUnix := strings.CutPrefix(docker.Host(), "unix://")
	require.True(t, isUnix, "this test mounts the daemon's socket into a container, so the daemon must be on a unix socket, not %s", docker.Host())
	if strings.HasSuffix(socket, "/.docker/run/docker.sock") {
		// Docker Desktop's socket on the Mac forwards to its VM, where a
		// container mounts the VM's own socket.
		socket = "/var/run/docker.sock"
	}
	requireImage(t, ctx, docker, siblingImage)
	helper := buildLinuxProgram(t, "../internal/siblinghelper")
	network := fmt.Sprintf("dbtest-sibling-%d", time.Now().UnixNano())
	dockerCLI(t, "network", "create", "--label", mongod.RequiredLabelKey+"="+mongod.RequiredLabelValue, network)
	t.Cleanup(func() { _ = exec.Command("docker", "network", "rm", network).Run() })

	out := dockerCLI(t, "run", "--rm", "--network", network,
		"--label", mongod.RequiredLabelKey+"="+mongod.RequiredLabelValue,
		"-v", socket+":/var/run/docker.sock", "-v", helper+":/siblinghelper:ro",
		"--entrypoint", "/siblinghelper", siblingImage.Reference())

	var report siblingReport
	require.NoError(t, json.Unmarshal([]byte(out), &report), "the helper prints its report as JSON")
	assert.Equal(t, "ok", report.Hello, "mongod answers a hello at Host and Port")
	assert.Equal(t, network, report.NetworkMode, "mongod joins the test container's network")
	assert.False(t, report.Published, "nothing is published on the Docker host")
	assert.Equal(t, 27017, report.Port, "mongod is reached on its own port")
	assert.Equal(t, mongod.DefaultUsername, report.Username, "other containers on that network can connect, so a password is generated")
	assert.True(t, report.Removed, "Stop leaves nothing behind")
}

// buildLinuxProgram compiles pkg as a static Linux binary, so it runs in any
// container image.
func buildLinuxProgram(t *testing.T, pkg string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), filepath.Base(pkg))
	build := exec.Command("go", "build", "-o", binary, pkg)
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux")
	build.Stderr = os.Stderr
	require.NoError(t, build.Run(), "the helper must build")
	return binary
}

// dockerCLI runs the docker CLI against the daemon the tests use and returns
// what it printed, trimmed.
func dockerCLI(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("docker", args...)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	require.NoError(t, err, "docker %s must succeed; this test needs the docker CLI", args[0])
	return strings.TrimSpace(string(out))
}
