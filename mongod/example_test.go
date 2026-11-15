package mongod_test

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/dockermock"
	"github.com/tophergopher/mongotest/mongod"
)

// stage stands in for Docker so the examples run anywhere. It is a fake
// daemon that says it is at 127.0.0.1 and publishes every container on a
// port where a stand-in for mongod answers. A real program passes no WithDocker,
// and Start finds the daemon itself.
type stage struct {
	*dockermock.Fake
	port int
}

func (s stage) Host() string { return "tcp://127.0.0.1:2375" }

func (s stage) ContainerInspect(ctx context.Context, id string) (dockerclient.ContainerInspect, error) {
	info, err := s.Fake.ContainerInspect(ctx, id)
	if err != nil {
		return info, err
	}
	info.NetworkSettings.Ports = map[string][]dockerclient.PortBinding{
		"27017/tcp": {{HostIP: "127.0.0.1", HostPort: strconv.Itoa(s.port)}},
	}
	return info, nil
}

// exampleDocker returns the stage, and a function that closes it.
func exampleDocker() (dockerclient.Client, func()) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	go acceptAll(listener, answerCommand)

	fake := dockermock.NewFake(dockermock.WithImages("docker.io/library/mongo:8.0", "docker.io/library/mongo:7.0", "mirror.corp.example/mongo:8.0"))
	fake.Processes = func(c dockermock.ContainerState) [][]string {
		if len(c.Config.Env) > 0 {
			return [][]string{{"1", "mongod --auth --bind_ip_all"}}
		}
		return [][]string{{"1", "mongod --bind_ip_all"}}
	}
	return stage{Fake: fake, port: listener.Addr().(*net.TCPAddr).Port}, func() { _ = listener.Close() }
}

// Start needs no options. This example is compiled but not run, because it
// talks to a real Docker daemon.
func ExampleStart() {
	ctx := context.Background()

	c, err := mongod.Start(ctx)
	if err != nil {
		fmt.Println("cannot start mongod:", err)
		return
	}
	defer c.Stop(context.Background())

	// Hand c.URI() to a MongoDB driver.
	fmt.Println(c.URI())
}

// Chain the options a start needs onto DefaultOptions.
func ExampleStartWithOptions() {
	docker, closeDocker := exampleDocker()
	defer closeDocker()
	ctx := context.Background()

	opts := mongod.DefaultOptions().WithDocker(docker).WithImage("7.0").WithReplicaSet("rs0")
	c, err := mongod.StartWithOptions(ctx, opts)
	if err != nil {
		fmt.Println("cannot start mongod:", err)
		return
	}
	defer c.Stop(context.Background())

	fmt.Println(c.Image(), c.ReplicaSet())
	// Output: docker.io/library/mongo:7.0 rs0
}

// Fetch the image once, in TestMain, so that tests starting in parallel do
// not each find it missing. An image the daemon already has is left alone.
func ExamplePullImage() {
	docker, closeDocker := exampleDocker()
	defer closeDocker()
	ctx := context.Background()
	opts := mongod.DefaultOptions().WithDocker(docker).WithImage("6.0")

	if err := mongod.PullImage(ctx, opts); err != nil {
		fmt.Println("cannot pull the image:", err)
		return
	}

	// A start with the same options now finds the image there.
	c, err := mongod.StartWithOptions(ctx, opts)
	if err != nil {
		fmt.Println("cannot start mongod:", err)
		return
	}
	defer c.Stop(context.Background())

	fmt.Println(c.Image())
	// Output: docker.io/library/mongo:6.0
}

// A With method returns a copy, so tests can add to a shared base without
// changing it for each other.
func ExampleOptions() {
	docker, closeDocker := exampleDocker()
	defer closeDocker()
	ctx := context.Background()
	base := mongod.DefaultOptions().WithDocker(docker).WithImage("7.0")

	withSet, err := mongod.StartWithOptions(ctx, base.WithReplicaSet("rs0"))
	if err != nil {
		fmt.Println("cannot start mongod:", err)
		return
	}
	defer withSet.Stop(context.Background())

	plain, err := mongod.StartWithOptions(ctx, base)
	if err != nil {
		fmt.Println("cannot start mongod:", err)
		return
	}
	defer plain.Stop(context.Background())

	fmt.Printf("%s %q\n", withSet.Image(), withSet.ReplicaSet())
	fmt.Printf("%s %q\n", plain.Image(), plain.ReplicaSet())
	// Output:
	// docker.io/library/mongo:7.0 "rs0"
	// docker.io/library/mongo:7.0 ""
}

// A version on its own keeps the default image, and a repository on its own
// keeps the default version, which is how to pull through a mirror.
func ExampleOptions_WithImage() {
	docker, closeDocker := exampleDocker()
	defer closeDocker()
	ctx := context.Background()

	older, err := mongod.StartWithOptions(ctx, mongod.DefaultOptions().WithDocker(docker).WithImage("7.0"))
	if err != nil {
		fmt.Println("cannot start mongod:", err)
		return
	}
	defer older.Stop(context.Background())

	mirrored, err := mongod.StartWithOptions(ctx, mongod.DefaultOptions().WithDocker(docker).WithImage("mirror.corp.example/mongo"))
	if err != nil {
		fmt.Println("cannot start mongod:", err)
		return
	}
	defer mirrored.Stop(context.Background())

	fmt.Println(older.Image())
	fmt.Println(mirrored.Image())
	// Output:
	// docker.io/library/mongo:7.0
	// mirror.corp.example/mongo:8.0
}

// MONGOTEST_IMAGE moves a whole suite to another image without editing a
// test, which is how CI gets off Docker Hub's pull limits.
func ExampleStartWithOptions_environment() {
	docker, closeDocker := exampleDocker()
	defer closeDocker()
	ctx := context.Background()
	os.Setenv(mongod.EnvImage, "mongo:7.0")
	defer os.Unsetenv(mongod.EnvImage)

	c, err := mongod.StartWithOptions(ctx, mongod.DefaultOptions().WithDocker(docker))
	if err != nil {
		fmt.Println("cannot start mongod:", err)
		return
	}
	defer c.Stop(context.Background())

	fmt.Println(c.Image())
	// Output: docker.io/library/mongo:7.0
}

func ExampleOptions_WithAuth() {
	docker, closeDocker := exampleDocker()
	defer closeDocker()
	ctx := context.Background()

	c, err := mongod.StartWithOptions(ctx, mongod.DefaultOptions().WithDocker(docker).WithAuth("admin", "p@ssword"))
	if err != nil {
		fmt.Println("cannot start mongod:", err)
		return
	}
	defer c.Stop(context.Background())

	// c.URI() carries the credentials, encoded, so it is all a driver needs.
	// Printing the container itself hides the password.
	fmt.Println(c.Username())
	fmt.Println("password printed:", strings.Contains(fmt.Sprint(c), "p@ssword"))
	// Output:
	// admin
	// password printed: false
}

// The URI is all a driver needs. It asks for a direct connection, because a
// replica set member advertises an address only its container can resolve.
func ExampleContainer_URI() {
	docker, closeDocker := exampleDocker()
	defer closeDocker()
	ctx := context.Background()

	c, err := mongod.StartWithOptions(ctx, mongod.DefaultOptions().WithDocker(docker).WithAuth("admin", "p@ssword"))
	if err != nil {
		fmt.Println("cannot start mongod:", err)
		return
	}
	defer c.Stop(context.Background())

	uri, err := url.Parse(c.URI())
	if err != nil {
		fmt.Println("the URI does not parse:", err)
		return
	}
	password, _ := uri.User.Password()
	fmt.Println(uri.Scheme, uri.User.Username(), password, uri.Query().Get("directConnection"))
	// Output: mongodb admin p@ssword true
}

// A lone word is a version if it starts with a digit or is "latest", and a
// repository otherwise. A name with no registry is Docker Hub's, and is
// spelled out in full.
func ExampleParseMongoImage() {
	for _, ref := range []string{"8.0", "latest", "mongo", "localhost:5000/team/mongo:8"} {
		image, err := mongod.ParseMongoImage(ref)
		if err != nil {
			fmt.Println("cannot parse:", err)
			return
		}
		fmt.Printf("registry=%q repository=%q version=%q\n", image.Registry, image.Repository, image.Version)
	}
	// Output:
	// registry="" repository="" version="8.0"
	// registry="" repository="" version="latest"
	// registry="docker.io" repository="library/mongo" version=""
	// registry="localhost:5000" repository="team/mongo" version="8"
}

// Every error matches a sentinel, so a caller never reads a message.
func ExampleStartWithOptions_errors() {
	ctx := context.Background()

	_, err := mongod.StartWithOptions(ctx, mongod.DefaultOptions().WithMongoImage(mongod.ImageCommunity.WithVersion("8")))
	fmt.Println("no such version:", errors.Is(err, mongod.ErrNoSuchVersion))

	_, err = mongod.StartWithOptions(ctx, mongod.DefaultOptions().WithMongoImage(mongod.ImageAtlasLocal).WithMongodArgs("--quiet"))
	var unsupported *mongod.UnsupportedForImageError
	if errors.As(err, &unsupported) {
		fmt.Println("remove:", unsupported.Option)
	}
	// Output:
	// no such version: true
	// remove: WithMongodArgs
}

// Calling this after m.Run in a TestMain removes any container a test
// forgot to stop:
//
//	func TestMain(m *testing.M) {
//		code := m.Run()
//		if err := mongod.ReapRunningContainers(context.Background()); err != nil {
//			log.Print(err)
//		}
//		os.Exit(code)
//	}
func ExampleReapRunningContainers() {
	docker, closeDocker := exampleDocker()
	defer closeDocker()
	ctx := context.Background()

	if _, err := mongod.StartWithOptions(ctx, mongod.DefaultOptions().WithDocker(docker)); err != nil {
		fmt.Println("cannot start mongod:", err)
		return
	}
	// Nothing stops the container.

	fmt.Println("reaped:", mongod.ReapRunningContainers(ctx) == nil)
	// Output: reaped: true
}

// busy is a daemon that refuses to remove any container.
type busy struct{ dockerclient.Client }

func (busy) ContainerRemove(context.Context, string, dockerclient.RemoveOptions) error {
	return errors.New("device or resource busy")
}

// When a container cannot be removed, the error names it, so that it can be
// removed by hand.
func ExampleReapRunningContainers_errors() {
	docker, closeDocker := exampleDocker()
	defer closeDocker()
	ctx := context.Background()

	opts := mongod.DefaultOptions().WithDocker(busy{docker}).WithName("orders-db")
	if _, err := mongod.StartWithOptions(ctx, opts); err != nil {
		fmt.Println("cannot start mongod:", err)
		return
	}

	err := mongod.ReapRunningContainers(ctx)
	var re *mongod.ReapError
	if errors.As(err, &re) {
		for _, failed := range re.Failures {
			fmt.Println("still running:", failed.Name)
		}
	}
	// Output: still running: orders-db
}

// RunScript runs JavaScript in the container's MongoDB shell and returns what
// it printed. Here a stand-in daemon plays the shell.
func ExampleContainer_RunScript() {
	docker, closeDocker := exampleDocker()
	defer closeDocker()
	docker.(stage).ExecFunc = func(_ string, cmd []string) dockerclient.ExecResult {
		return dockerclient.ExecResult{Stdout: "1\n"}
	}
	ctx := context.Background()

	c, err := mongod.StartWithOptions(ctx, mongod.DefaultOptions().WithDocker(docker))
	if err != nil {
		fmt.Println("start:", err)
		return
	}
	defer c.Stop(ctx)

	ok, err := c.RunScript(ctx, "db.runCommand({ping: 1}).ok")
	if err != nil {
		// A *ScriptError's fields hold all the shell wrote; the message
		// quotes the start of it.
		fmt.Println(err)
		return
	}
	fmt.Println("ping ok:", ok)
	// Output: ping ok: 1
}

// tlsStage is the stage for a container started with TLS: once the start
// copies its certificate in, a stand-in for mongod answers over TLS with it.
type tlsStage struct {
	stage
	listener net.Listener
}

func (s *tlsStage) CopyToContainer(ctx context.Context, id, dir string, files []dockerclient.File) error {
	for _, f := range files {
		cert, err := tls.X509KeyPair(f.Content, f.Content)
		if err != nil {
			continue
		}
		listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
		if err != nil {
			return err
		}
		go acceptAll(listener, answerCommand)
		s.listener, s.port = listener, listener.Addr().(*net.TCPAddr).Port
	}
	return s.Fake.CopyToContainer(ctx, id, dir, files)
}

// With WithTLS, mongod accepts only TLS. A driver needs the URI, which asks
// for TLS, and TLSConfig, which trusts the certificate made for the
// container: with the official driver, options.Client().ApplyURI(c.URI()).
// SetTLSConfig(c.TLSConfig()).
func ExampleOptions_WithTLS() {
	plain, closeDocker := exampleDocker()
	defer closeDocker()
	docker := &tlsStage{stage: plain.(stage)}
	ctx := context.Background()

	c, err := mongod.StartWithOptions(ctx, mongod.DefaultOptions().WithDocker(docker).WithTLS())
	if err != nil {
		fmt.Println("cannot start mongod:", err)
		return
	}
	defer c.Stop(ctx)
	defer docker.listener.Close()

	conn, err := tls.Dial("tcp", net.JoinHostPort(c.Host(), strconv.Itoa(c.Port())), c.TLSConfig())
	if err != nil {
		fmt.Println("the server is not trusted:", err)
		return
	}
	defer conn.Close()
	uri, _ := url.Parse(c.URI())
	fmt.Println("tls:", uri.Query().Get("tls"))
	// Output: tls: true
}
