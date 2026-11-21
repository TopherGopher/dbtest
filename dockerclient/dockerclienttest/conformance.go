package dockerclienttest

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"

	"github.com/tophergopher/dbtest/dockerclient"
)

// Factory returns a client whose daemon starts empty and keeps state for the
// duration of one test.
type Factory func(t *testing.T) dockerclient.Client

// TestImage is the image the conformance suite pulls and runs. Nothing is
// downloaded: the daemon behind the client is a double.
const TestImage = "mongo:8"

// What the daemon behind a conformance client must report for TestImage.
// The suite cannot set either through the client, so the double is told:
// a dockermock.Fake takes them in its ImageUser and Health fields.
const (
	// TestImageUser is the user TestImage declares, as MongoDB's own images
	// declare one by name.
	TestImageUser = "mongodb"
	// TestImageHealth is the health a running container of TestImage
	// reports, as one from an image with a health check does.
	TestImageHealth = "healthy"
	// TestContainerLog is what a container of TestImage writes to stdout,
	// where mongod writes its log. A dockermock.Fake takes it in its Log
	// field.
	TestContainerLog = "Waiting for connections\n"
	// TestContainerErrLog is what a container of TestImage writes to
	// stderr. A dockermock.Fake takes it in its ErrLog field.
	TestContainerErrLog = "about to fork child process\n"
)

// Conformance runs every behaviour assertion against one implementation.
func Conformance(t *testing.T, newClient Factory) {
	t.Run("rejects bad input before reaching the daemon", func(t *testing.T) {
		conformanceValidation(t, newClient(t))
	})
	t.Run("reports a missing object as ErrNotFound", func(t *testing.T) {
		conformanceNotFound(t, newClient(t))
	})
	t.Run("runs a container lifecycle", func(t *testing.T) {
		conformanceLifecycle(t, newClient(t))
	})
	t.Run("copies files into a container", func(t *testing.T) {
		conformanceCopy(t, newClient(t))
	})
	t.Run("runs a command and reports its output", func(t *testing.T) {
		conformanceExec(t, newClient(t))
	})
	t.Run("reports the user an image declares", func(t *testing.T) {
		conformanceImageUser(t, newClient(t))
	})
	t.Run("reports the health check of a running container", func(t *testing.T) {
		conformanceHealth(t, newClient(t))
	})
	t.Run("reads what a container wrote", func(t *testing.T) {
		conformanceLogs(t, newClient(t))
	})
	t.Run("puts a container on the network it names", func(t *testing.T) {
		conformanceNetwork(t, newClient(t))
	})
	t.Run("mounts the named volume it is given", func(t *testing.T) {
		conformanceVolume(t, newClient(t))
	})
}

// TestVolume is the named volume the conformance suite mounts. A double
// keeps nothing in it; a real daemon would create it and keep it.
const TestVolume = "dbtest-conformance"

// conformanceVolume checks that a create that mounts a named volume is
// accepted, which is how mongod keeps its data between containers.
func conformanceVolume(t *testing.T, c dockerclient.Client) {
	ctx := context.Background()
	if err := c.ImagePull(ctx, TestImage); err != nil {
		t.Fatalf("pulling %s: %v", TestImage, err)
	}
	_, _, err := c.ContainerCreate(ctx, "", dockerclient.ContainerConfig{
		Image:      TestImage,
		HostConfig: &dockerclient.HostConfig{Volumes: []dockerclient.VolumeMount{{Name: TestVolume, Path: "/data/db"}}},
	})
	if err != nil {
		t.Errorf("a create that mounts the volume %s must be accepted, got %v", TestVolume, err)
	}
}

// TestNetwork is the network the conformance suite puts a container on. A
// double accepts any name; a real daemon would need it created first.
const TestNetwork = "dbtest-conformance"

// conformanceNetwork checks that NetworkMode reaches the daemon and that
// inspect reports the container's address on that network, which is how a
// container started next to the caller is reached with no published port.
func conformanceNetwork(t *testing.T, c dockerclient.Client) {
	ctx := context.Background()
	if err := c.ImagePull(ctx, TestImage); err != nil {
		t.Fatalf("pulling %s: %v", TestImage, err)
	}
	id, _, err := c.ContainerCreate(ctx, "", dockerclient.ContainerConfig{
		Image:      TestImage,
		HostConfig: &dockerclient.HostConfig{NetworkMode: TestNetwork},
	})
	if err != nil {
		t.Fatalf("creating a container on %s: %v", TestNetwork, err)
	}
	if err := c.ContainerStart(ctx, id); err != nil {
		t.Fatalf("starting %s: %v", id, err)
	}
	info, err := c.ContainerInspect(ctx, id)
	if err != nil {
		t.Fatalf("inspecting %s: %v", id, err)
	}
	endpoint, ok := info.NetworkSettings.Networks[TestNetwork]
	if !ok {
		t.Fatalf("a container created with NetworkMode %q must be on that network, got networks %+v. "+
			"Send HostConfig.NetworkMode on create and read NetworkSettings.Networks on inspect",
			TestNetwork, info.NetworkSettings.Networks)
	}
	if net.ParseIP(endpoint.IPAddress) == nil {
		t.Errorf("a running container must report its address on %s, got %q", TestNetwork, endpoint.IPAddress)
	}
	// A daemon lists an exposed port that is not published with no
	// bindings, so only a binding counts.
	for port, bindings := range info.NetworkSettings.Ports {
		if len(bindings) != 0 {
			t.Errorf("a container that publishes nothing must report no host port, got %s on %+v", port, bindings)
		}
	}
}

// conformanceLogs checks ContainerLogsTo returns what the container wrote,
// which is how a failed start says why mongod stopped.
func conformanceLogs(t *testing.T, c dockerclient.Client) {
	id := createRunning(t, c)
	var stdout, stderr bytes.Buffer
	if err := c.ContainerLogsTo(context.Background(), id, &stdout, &stderr); err != nil {
		t.Fatalf("reading the log of %s: %v", id, err)
	}
	if stdout.String() != TestContainerLog {
		t.Errorf("the log must hold what the container wrote to stdout: want %q, got %q. Without it a failed start "+
			"cannot say why mongod stopped. Split the daemon's log stream into stdout and stderr; a dockermock.Fake "+
			"needs its Log set to TestContainerLog", TestContainerLog, stdout.String())
	}
	if stderr.String() != TestContainerErrLog {
		t.Errorf("the log must hold what the container wrote to stderr, apart from stdout: want %q, got %q. "+
			"A dockermock.Fake needs its ErrLog set to TestContainerErrLog", TestContainerErrLog, stderr.String())
	}
}

// conformanceImageUser checks ImageInspect passes on the image's user, which
// decides who owns the key file a replica set with credentials needs.
func conformanceImageUser(t *testing.T, c dockerclient.Client) {
	ctx := context.Background()
	if err := c.ImagePull(ctx, TestImage); err != nil {
		t.Fatalf("pulling %s: %v", TestImage, err)
	}
	image, err := c.ImageInspect(ctx, TestImage)
	if err != nil {
		t.Fatalf("inspecting %s: %v", TestImage, err)
	}
	if image.Config.User != TestImageUser {
		t.Errorf("ImageInspect reported the image's user as %q, want %q. Without it a replica set with credentials "+
			"cannot start on MongoDB's own images: its key file gets the wrong owner and mongod refuses it. "+
			"Fill ImageInspect.Config.User from the daemon's answer", image.Config.User, TestImageUser)
	}
}

// conformanceHealth checks ContainerInspect passes on the health check's
// status, which is the only sign that some images have finished starting.
func conformanceHealth(t *testing.T, c dockerclient.Client) {
	ctx := context.Background()
	id := createRunning(t, c)
	info, err := c.ContainerInspect(ctx, id)
	if err != nil {
		t.Fatalf("inspecting %s: %v", id, err)
	}
	if info.State.Health.Status != TestImageHealth {
		t.Errorf("ContainerInspect reported the container's health as %q, want %q. Without it a start returns "+
			"before an image that declares a health check, such as Atlas Local, is ready. "+
			"Fill ContainerInspect.State.Health from the daemon's answer", info.State.Health.Status, TestImageHealth)
	}
}

func conformanceValidation(t *testing.T, c dockerclient.Client) {
	ctx := context.Background()
	checks := map[string]func() error{
		"an uppercase image reference": func() error { return c.ImagePull(ctx, "Mongo:8") },
		"an empty container id":        func() error { return c.ContainerStart(ctx, "") },
		"a container id with a slash": func() error {
			return c.ContainerRemove(ctx, "a/b", dockerclient.RemoveOptions{})
		},
		"a create with no image": func() error {
			_, _, err := c.ContainerCreate(ctx, "", dockerclient.ContainerConfig{})
			return err
		},
		"an empty exec id": func() error {
			return c.ExecStartTo(ctx, "", nil, nil)
		},
		"reading the log of an empty container id": func() error {
			return c.ContainerLogsTo(ctx, "", nil, nil)
		},
		"an exec id with a slash": func() error {
			_, err := c.ExecInspect(ctx, "a/b")
			return err
		},
		"a port binding with a bad host ip": func() error {
			_, _, err := c.ContainerCreate(ctx, "", dockerclient.ContainerConfig{
				Image: TestImage,
				HostConfig: &dockerclient.HostConfig{PortBindings: map[string][]dockerclient.PortBinding{
					"27017/tcp": {{HostIP: "not-an-ip"}},
				}},
			})
			return err
		},
		"a volume named like a host directory": func() error {
			_, _, err := c.ContainerCreate(ctx, "", dockerclient.ContainerConfig{
				Image:      TestImage,
				HostConfig: &dockerclient.HostConfig{Volumes: []dockerclient.VolumeMount{{Name: "/srv/data", Path: "/data/db"}}},
			})
			return err
		},
		"a copy with no files": func() error {
			return c.CopyToContainer(ctx, "abc", "/tmp", nil)
		},
		"an exec with no command": func() error {
			_, err := c.Exec(ctx, "abc")
			return err
		},
	}
	for name, check := range checks {
		err := check()
		if !errors.Is(err, dockerclient.ErrInvalidArgument) {
			t.Errorf("%s must be rejected as an invalid argument, got %v", name, err)
		}
	}
}

func conformanceNotFound(t *testing.T, c dockerclient.Client) {
	ctx := context.Background()
	checks := map[string]func() error{
		"inspecting an unknown container": func() error {
			_, err := c.ContainerInspect(ctx, "missing")
			return err
		},
		"starting an unknown container": func() error { return c.ContainerStart(ctx, "missing") },
		"removing an unknown container": func() error {
			return c.ContainerRemove(ctx, "missing", dockerclient.RemoveOptions{Force: true})
		},
		"reading the log of an unknown container": func() error {
			return c.ContainerLogsTo(ctx, "missing", nil, nil)
		},
		"inspecting an image that was never pulled": func() error {
			_, err := c.ImageInspect(ctx, "never-pulled:1")
			return err
		},
		"creating from an image that was never pulled": func() error {
			_, _, err := c.ContainerCreate(ctx, "", dockerclient.ContainerConfig{Image: "never-pulled:1"})
			return err
		},
	}
	for name, check := range checks {
		err := check()
		if !dockerclient.IsNotFound(err) {
			t.Errorf("%s must report ErrNotFound, got %v", name, err)
		}
	}
}

// createRunning pulls the test image and starts a container from it.
func createRunning(t *testing.T, c dockerclient.Client) string {
	t.Helper()
	ctx := context.Background()
	if err := c.ImagePull(ctx, TestImage); err != nil {
		t.Fatalf("pulling %s: %v", TestImage, err)
	}
	id, _, err := c.ContainerCreate(ctx, "", dockerclient.ContainerConfig{
		Image:        TestImage,
		Labels:       map[string]string{"mongotest": "conformance"},
		ExposedPorts: map[string]struct{}{"27017/tcp": {}},
		HostConfig: &dockerclient.HostConfig{PortBindings: map[string][]dockerclient.PortBinding{
			"27017/tcp": {{HostIP: "127.0.0.1", HostPort: ""}},
		}},
	})
	if err != nil {
		t.Fatalf("creating a container: %v", err)
	}
	if err := c.ContainerStart(ctx, id); err != nil {
		t.Fatalf("starting %s: %v", id, err)
	}
	return id
}

func conformanceLifecycle(t *testing.T, c dockerclient.Client) {
	ctx := context.Background()
	id := createRunning(t, c)

	if err := c.ContainerStart(ctx, id); err != nil {
		t.Errorf("starting an already running container must succeed, got %v", err)
	}

	info, err := c.ContainerInspect(ctx, id)
	if err != nil {
		t.Fatalf("inspecting %s: %v", id, err)
	}
	if !info.State.Running {
		t.Errorf("a started container must report Running, got state %+v", info.State)
	}
	if info.HostPort("27017/tcp") == "" {
		t.Errorf("an empty HostPort must be filled in by the daemon, got ports %+v", info.NetworkSettings.Ports)
	}
	if info.HostPort("80/tcp") != "" {
		t.Errorf("HostPort must be empty for a port that was never published, got %q", info.HostPort("80/tcp"))
	}

	top, err := c.ContainerTop(ctx, id)
	if err != nil {
		t.Fatalf("listing processes in %s: %v", id, err)
	}
	if len(top.Processes) == 0 {
		t.Error("a running container must report at least one process")
	}

	if err := c.ContainerRemove(ctx, id, dockerclient.RemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
		t.Fatalf("removing %s: %v", id, err)
	}
	if _, err := c.ContainerInspect(ctx, id); !dockerclient.IsNotFound(err) {
		t.Errorf("inspecting a removed container must report ErrNotFound, got %v", err)
	}
	if err := c.ContainerRemove(ctx, id, dockerclient.RemoveOptions{Force: true}); !dockerclient.IsNotFound(err) {
		t.Errorf("removing a container twice must report ErrNotFound, got %v", err)
	}
}

func conformanceCopy(t *testing.T, c dockerclient.Client) {
	ctx := context.Background()
	id := createRunning(t, c)
	files := []dockerclient.File{
		{Name: "mongo-tls/server.pem", Mode: 0o644, Content: []byte("certificate and key")},
		{Name: "mongo-tls/ca.pem", Mode: 0o644, Content: []byte("ca")},
	}
	// One condition per assertion, each checking the sentinel rather than
	// merely that something failed. An earlier version passed id and destDir
	// the wrong way round and asserted only err != nil, so it passed for the
	// wrong reason and hid two missing checks in the Fake.
	if err := c.CopyToContainer(ctx, id, "/etc", nil); !errors.Is(err, dockerclient.ErrNoFiles) {
		t.Errorf("a copy with no files must be ErrNoFiles, got %v", err)
	}
	if err := c.CopyToContainer(ctx, id, "", files); !errors.Is(err, dockerclient.ErrInvalidArgument) {
		t.Errorf("a copy with no destination must be ErrInvalidArgument, got %v", err)
	}
	if err := c.CopyToContainer(ctx, id, "relative", files); !errors.Is(err, dockerclient.ErrInvalidArgument) {
		t.Errorf("a relative destination must be rejected, got %v", err)
	}
	if err := c.CopyToContainer(ctx, id, "/etc/../root", files); !errors.Is(err, dockerclient.ErrInvalidArgument) {
		t.Errorf("a destination that walks outside itself must be rejected, got %v", err)
	}
	if err := c.CopyArchiveToContainer(ctx, id, "relative", bytes.NewReader(nil)); !errors.Is(err, dockerclient.ErrInvalidArgument) {
		t.Errorf("CopyArchiveToContainer must apply the same destination rule, got %v", err)
	}
	if err := c.CopyToContainer(ctx, id, "/etc", files); err != nil {
		t.Fatalf("copying files into %s: %v", id, err)
	}
	if err := c.CopyArchiveToContainer(ctx, id, "/tmp", bytes.NewReader(nil)); err != nil {
		t.Errorf("an empty archive is not an error, got %v", err)
	}
}

func conformanceExec(t *testing.T, c dockerclient.Client) {
	ctx := context.Background()
	id := createRunning(t, c)

	res, err := c.Exec(ctx, id, "echo", "hello")
	if err != nil {
		t.Fatalf("running a command in %s: %v", id, err)
	}
	if res.ExitCode != 0 {
		t.Errorf("a successful command must report exit code 0, got %+v", res)
	}

	execID, err := c.ExecCreate(ctx, id, dockerclient.ExecConfig{Cmd: []string{"echo", "streamed"}})
	if err != nil {
		t.Fatalf("creating an exec in %s: %v", id, err)
	}
	var stdout, stderr bytes.Buffer
	if err := c.ExecStartTo(ctx, execID, &stdout, &stderr); err != nil {
		t.Fatalf("starting exec %s: %v", execID, err)
	}
	ins, err := c.ExecInspect(ctx, execID)
	if err != nil {
		t.Fatalf("inspecting exec %s: %v", execID, err)
	}
	if ins.Running {
		t.Error("an exec must not report Running once its output stream has closed")
	}
	if _, err := c.ExecInspect(ctx, "missing"); !dockerclient.IsNotFound(err) {
		t.Errorf("inspecting an unknown exec must report ErrNotFound, got %v", err)
	}
}
