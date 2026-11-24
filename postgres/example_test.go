package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"testing"

	"github.com/tophergopher/dbtest/dockerclient"
	"github.com/tophergopher/dbtest/dockermock"
	"github.com/tophergopher/dbtest/postgres"
)

// stage stands in for Docker so the examples run anywhere. It is a fake
// daemon that publishes every container on a port where a stand-in for
// PostgreSQL answers. A real program passes no WithDocker, and Start finds
// the daemon itself.
type stage struct {
	*dockermock.Fake
	port int
}

func (s stage) ContainerInspect(ctx context.Context, id string) (dockerclient.ContainerInspect, error) {
	info, err := s.Fake.ContainerInspect(ctx, id)
	if err != nil {
		return info, err
	}
	info.NetworkSettings.Ports = map[string][]dockerclient.PortBinding{
		"5432/tcp": {{HostIP: "127.0.0.1", HostPort: strconv.Itoa(s.port)}},
	}
	return info, nil
}

// exampleDocker returns the stage, and a function that closes it.
func exampleDocker() (dockerclient.Client, func()) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go askPassword(conn)
		}
	}()
	fake := dockermock.NewFake(dockermock.WithImages("docker.io/library/postgres:17"))
	return stage{Fake: fake, port: listener.Addr().(*net.TCPAddr).Port}, func() { _ = listener.Close() }
}

// Start needs no options. This example is compiled but not run, because it
// talks to a real Docker daemon.
func ExampleStart() /* --8<-- [start:start] */ {
	ctx := context.Background()

	c, err := postgres.Start(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer c.Stop(ctx)

	// Give c.URI() to a driver, such as pgx through database/sql:
	//
	//	db, err := sql.Open("pgx", c.URI())
	//
	// or pgx's own pool:
	//
	//	pool, err := pgxpool.New(ctx, c.URI())
	//
	// It holds the password, so log the address instead.
	log.Printf("PostgreSQL is listening on %s:%d", c.Host(), c.Port())
} // --8<-- [end:start]

// Chain the options a start needs onto DefaultOptions. A lone version runs
// the official image at that tag.
func ExampleStartWithOptions() {
	docker, closeDocker := exampleDocker()
	defer closeDocker()
	ctx := context.Background()

	opts := postgres.DefaultOptions().WithDocker(docker).WithImage("17")
	c, err := postgres.StartWithOptions(ctx, opts)
	if err != nil {
		fmt.Println("cannot start PostgreSQL:", err)
		return
	}
	defer c.Stop(ctx)

	fmt.Println(c.Image())
	fmt.Println(c.Username(), c.Database())
	// Output:
	// docker.io/library/postgres:17
	// postgres postgres
}

// A mistake in the options is refused before anything is created, with an
// error to match with errors.Is.
func ExampleOptions_WithImage_mistake() {
	_, err := postgres.StartWithOptions(context.Background(), postgres.DefaultOptions().WithImage(""))
	fmt.Println(errors.Is(err, postgres.ErrEmptyImage))
	fmt.Println(errors.Is(err, dockerclient.ErrInvalidArgument))
	// Output:
	// true
	// true
}

// Pull the image in TestMain, so the download happens once, before any test
// starts, and remove whatever the tests left running afterwards. This
// example is compiled but not run, because it talks to a real Docker daemon.
func ExamplePullImage() {
	var m *testing.M // TestMain's argument
	// --8<-- [start:test-main]
	ctx := context.Background()
	if err := postgres.PullImage(ctx, postgres.DefaultOptions()); err != nil {
		log.Fatal(err)
	}
	code := m.Run()
	if err := postgres.ReapRunningContainers(ctx); err != nil {
		log.Print(err)
	}
	os.Exit(code)
	// --8<-- [end:test-main]
}
