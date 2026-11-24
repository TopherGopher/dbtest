package hostaddr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/dbtest/dockerclient"
	"github.com/tophergopher/dbtest/dockermock"
)

// selfID is the container id these tests give this process.
const selfID = "ebfb67d24553813b4d0d0d04935324b7b2abf09d16da2722cead41dadc6ea57c"

// mountinfoOf returns the line Docker writes for a container's hostname file,
// as /proc/self/mountinfo shows it inside that container.
func mountinfoOf(id string) string {
	return "28 27 0:51 / / rw,relatime - overlay overlay rw\n" +
		"114 94 254:0 /var/lib/docker/containers/" + id + "/hostname /etc/hostname rw,relatime - ext4 /dev/vda rw\n" +
		"115 94 254:0 /var/lib/docker/containers/" + id + "/hosts /etc/hosts rw,relatime - ext4 /dev/vda rw\n"
}

func TestTheContainerIDIsReadFromTheHostnameMount(t *testing.T) {
	cases := map[string]struct {
		mountinfo string
		want      string
		found     bool
	}{
		"docker":            {mountinfo: mountinfoOf(selfID), want: selfID, found: true},
		"a data root moved": {mountinfo: "114 94 254:0 /srv/docker/containers/" + selfID + "/hostname /etc/hostname rw - ext4 /dev/vda rw\n", want: selfID, found: true},
		// When /var/lib/docker is its own mount, the path starts there.
		"a data root mounted alone": {mountinfo: "114 94 254:0 /containers/" + selfID + "/hostname /etc/hostname rw - ext4 /dev/vdb rw\n", want: selfID, found: true},
		"not in a container":        {mountinfo: "28 27 0:51 / / rw,relatime - ext4 /dev/vda rw\n"},
		"podman":                    {mountinfo: "114 94 0:40 /overlay-containers/" + selfID + "/userdata/hostname /etc/hostname rw - tmpfs tmpfs rw\n"},
		"mounted elsewhere":         {mountinfo: "114 94 254:0 /var/lib/docker/containers/" + selfID + "/hostname /mnt/hostname rw - ext4 /dev/vda rw\n"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			id, err := containerIDFromMountinfo(strings.NewReader(tc.mountinfo))

			assert.Equal(t, tc.found, err == nil, "only Docker's hostname mount names this process's container")
			assert.Equal(t, tc.want, id, "the id is the directory the hostname file is in")
			if !tc.found {
				assert.ErrorIs(t, err, ErrNoSharedNetwork, "no id means no shared network")
			}
		})
	}
}

func TestAMountinfoThatCannotBeReadIsNotTakenAsNoContainer(t *testing.T) {
	readFailed := errors.New("read failed")
	broken := io.MultiReader(strings.NewReader("28 27 0:51 / / rw - overlay overlay rw\n"), iotest.ErrReader(readFailed))

	_, err := containerIDFromMountinfo(broken)

	assert.ErrorIs(t, err, ErrNoSharedNetwork, "an unreadable mountinfo still means no shared network")
	assert.ErrorIs(t, err, readFailed, "and the debug line says why it could not be read")
}

// hostnameLine is the mountinfo line Docker writes for a container's
// hostname file.
func hostnameLine(id string) string {
	return "114 94 254:0 /var/lib/docker/containers/" + id + "/hostname /etc/hostname rw - ext4 /dev/vda rw\n"
}

// namedBy reports whether mountinfo has a hostname line for id.
func namedBy(mountinfo, id string) bool {
	return strings.Contains(mountinfo, "/containers/"+id+"/hostname /etc/hostname")
}

func FuzzContainerIDFromMountinfo(f *testing.F) {
	f.Add("28 27 0:51 / / rw - overlay overlay rw", selfID)
	f.Add("114 94 254:0 /containers//hostname /etc/hostname", "abc")
	f.Add("", "")
	f.Fuzz(func(t *testing.T, before, id string) {
		if got, err := containerIDFromMountinfo(strings.NewReader(before)); err == nil && !namedBy(before, got) {
			t.Fatalf("found %q, which no hostname line names", got)
		}
		// A line longer than the scanner takes is reported as unreadable.
		if !isContainerID(id) || len(before) > 60_000 {
			return
		}
		mountinfo := before + "\n" + hostnameLine(id)
		got, err := containerIDFromMountinfo(strings.NewReader(mountinfo))
		if err != nil {
			t.Fatalf("the hostname line for %q was not found: %v", id, err)
		}
		// An earlier hostname line wins.
		if got != id && !namedBy(before, got) {
			t.Fatalf("found %q, want %q", got, id)
		}
	})
}

// fakeDaemon returns a Fake holding this process's container, created with
// network mode mode and started, and the address the Fake gave it there.
func fakeDaemon(t *testing.T, mode string) (*dockermock.Fake, string, string) {
	t.Helper()
	ctx := context.Background()
	fake := dockermock.NewFake()
	require.NoError(t, fake.ImagePull(ctx, "golang:1.27"), "the test container needs an image")
	id, _, err := fake.ContainerCreate(ctx, "ci-job", dockerclient.ContainerConfig{
		Image: "golang:1.27", HostConfig: &dockerclient.HostConfig{NetworkMode: mode},
	})
	require.NoError(t, err, "the test container must be created")
	require.NoError(t, fake.ContainerStart(ctx, id), "the test container must be running")
	info, err := fake.ContainerInspect(ctx, id)
	require.NoError(t, err, "the test container must be inspectable")
	var address string
	for _, endpoint := range info.NetworkSettings.Networks {
		address = endpoint.IPAddress
	}
	return fake, id, address
}

// finderFor is a finder that reads mountinfo as given and sees addresses as
// this process's own.
func finderFor(mountinfo string, addresses ...string) networkFinder {
	return networkFinder{
		openMountinfo: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(mountinfo)), nil },
		localAddrs: func() ([]net.Addr, error) {
			var addrs []net.Addr
			for _, address := range addresses {
				addrs = append(addrs, &net.IPNet{IP: net.ParseIP(address), Mask: net.CIDRMask(16, 32)})
			}
			return addrs, nil
		},
	}
}

func TestAContainerOnANetworkSharesIt(t *testing.T) {
	cases := map[string]struct {
		mode string
		want string
	}{
		"a user-defined network": {mode: "ci-net", want: "ci-net"},
		"the default bridge":     {mode: "bridge", want: "bridge"},
		"no mode, so the bridge": {mode: "", want: "bridge"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fake, id, address := fakeDaemon(t, tc.mode)

			network, err := finderFor(mountinfoOf(id), address).find(context.Background(), fake)

			require.NoError(t, err, "a container whose address is this process's shares its network")
			assert.Equal(t, tc.want, network, "a new container joins the network this process is on")
		})
	}
}

func TestTheFirstUserDefinedNetworkIsChosen(t *testing.T) {
	// Every user-defined name sorts after "bridge", and there are enough
	// that a choice left to map order would rarely pick ci-net.
	names := []string{"bridge", "zeta", "kappa", "omega", "delta", "ci-net", "sigma", "theta"}
	networks := map[string]dockerclient.NetworkEndpoint{}
	var local []net.IP
	for i, name := range names {
		address := fmt.Sprintf("172.%d.0.2", 17+i)
		networks[name] = dockerclient.NetworkEndpoint{IPAddress: address}
		local = append(local, net.ParseIP(address))
	}

	network, err := chooseNetwork(networks, local)

	require.NoError(t, err, "a container on several networks shares them all")
	assert.Equal(t, "ci-net", network, "the user-defined network that sorts first wins over the bridge")
}

func TestNoNetworkIsSharedWhenTheContainerCannotBeConfirmed(t *testing.T) {
	fake, id, address := fakeDaemon(t, "ci-net")
	cases := map[string]struct {
		finder networkFinder
		docker dockerclient.Client
	}{
		"not in a container": {finder: finderFor("28 27 0:51 / / rw - ext4 /dev/vda rw\n", address), docker: fake},
		"mountinfo unreadable": {finder: networkFinder{
			openMountinfo: func() (io.ReadCloser, error) { return nil, errors.New("permission denied") },
			localAddrs:    finderFor("", address).localAddrs,
		}, docker: fake},
		// Another daemon, such as Docker-in-Docker, does not know the id.
		"the daemon does not know it": {finder: finderFor(mountinfoOf(selfID), address), docker: fake},
		// Or it knows a container of that id that is not this one.
		"its address is not ours": {finder: finderFor(mountinfoOf(id), "10.9.8.7"), docker: fake},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := tc.finder.find(context.Background(), tc.docker)

			assert.ErrorIs(t, err, ErrNoSharedNetwork, "anything that cannot be confirmed means no shared network")
		})
	}
}

func TestHostNetworkingSharesNoNetwork(t *testing.T) {
	for _, mode := range []string{"host", "none", "container:" + selfID} {
		t.Run(mode, func(t *testing.T) {
			fake, id, _ := fakeDaemon(t, mode)

			_, err := finderFor(mountinfoOf(id), "127.0.0.1", "10.0.0.5").find(context.Background(), fake)

			assert.ErrorIs(t, err, ErrNoSharedNetwork, "a container without a network of its own has nothing to share")
		})
	}
}
