package hostaddr

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"

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
		"docker":             {mountinfo: mountinfoOf(selfID), want: selfID, found: true},
		"a data root moved":  {mountinfo: "114 94 254:0 /srv/docker/containers/" + selfID + "/hostname /etc/hostname rw - ext4 /dev/vda rw\n", want: selfID, found: true},
		"not in a container": {mountinfo: "28 27 0:51 / / rw,relatime - ext4 /dev/vda rw\n"},
		"podman":             {mountinfo: "114 94 0:40 /overlay-containers/" + selfID + "/userdata/hostname /etc/hostname rw - tmpfs tmpfs rw\n"},
		"mounted elsewhere":  {mountinfo: "114 94 254:0 /var/lib/docker/containers/" + selfID + "/hostname /mnt/hostname rw - ext4 /dev/vda rw\n"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			id, found := containerIDFromMountinfo(strings.NewReader(tc.mountinfo))

			assert.Equal(t, tc.found, found, "only Docker's hostname mount names this process's container")
			assert.Equal(t, tc.want, id, "the id is the directory the hostname file is in")
		})
	}
}

func FuzzContainerIDFromMountinfo(f *testing.F) {
	f.Add(mountinfoOf(selfID))
	f.Add("114 94 254:0 /containers//hostname /etc/hostname\n")
	f.Add("")
	f.Fuzz(func(t *testing.T, mountinfo string) {
		id, found := containerIDFromMountinfo(strings.NewReader(mountinfo))
		if found && !isContainerID(id) {
			t.Fatalf("found %q, which is not a container id", id)
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
		mode        string
		want        string
		userDefined bool
	}{
		"a user-defined network": {mode: "ci-net", want: "ci-net", userDefined: true},
		"the default bridge":     {mode: "bridge", want: "bridge"},
		"no mode, so the bridge": {mode: "", want: "bridge"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fake, id, address := fakeDaemon(t, tc.mode)

			network, err := finderFor(mountinfoOf(id), address).find(context.Background(), fake)

			require.NoError(t, err, "a container whose address is this process's shares its network")
			assert.Equal(t, tc.want, network.Name, "a new container joins the network this process is on")
			assert.Equal(t, tc.userDefined, network.UserDefined, "only the default bridge lets every container connect")
		})
	}
}

func TestTheFirstUserDefinedNetworkIsChosen(t *testing.T) {
	networks := map[string]dockerclient.NetworkEndpoint{
		"bridge": {IPAddress: "172.17.0.2"}, "zeta": {IPAddress: "172.19.0.2"}, "alpha": {IPAddress: "172.18.0.2"},
	}

	network, err := chooseNetwork(networks, []net.IP{net.ParseIP("172.17.0.2"), net.ParseIP("172.18.0.2"), net.ParseIP("172.19.0.2")})

	require.NoError(t, err, "a container on three networks shares them all")
	assert.Equal(t, SharedNetwork{Name: "alpha", UserDefined: true}, network, "the user-defined network that sorts first wins over the bridge")
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
