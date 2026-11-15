package mongod_test

import (
	"context"
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

// Transactions and change streams need a replica set. The start initiates
// it and returns once it accepts writes.
func ExampleOptions_WithReplicaSet() {
	docker, closeDocker := exampleDocker()
	defer closeDocker()
	ctx := context.Background()

	c, err := mongod.StartWithOptions(ctx, mongod.DefaultOptions().WithDocker(docker).WithReplicaSet("rs0"))
	if err != nil {
		fmt.Println("cannot start mongod:", err)
		return
	}
	defer c.Stop(context.Background())

	fmt.Println(c.ReplicaSet())
	// Output: rs0
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
