// Command siblinghelper is a test helper, never part of the library: the
// mongod package's integration test runs it inside a container that has the
// Docker socket mounted, the way CI runs tests.
//
// It starts MongoDB with mongod.Start, sends a hello to Container.Host and
// Container.Port, asks the daemon how the container was set up, stops it,
// and prints what it found as one JSON object for the test to read.
package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/tophergopher/dbtest/dockerapi"
	"github.com/tophergopher/dbtest/mongod"
)

// Report is what the helper prints.
type Report struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	NetworkMode string `json:"networkMode"`
	Published   bool   `json:"published"`
	Hello       string `json:"hello"`
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	report, err := run(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "siblinghelper:", err)
		os.Exit(1)
	}
	_ = json.NewEncoder(os.Stdout).Encode(report)
}

// run starts mongod with the defaults. The logger only lets a failed test
// show why a start took the path it did.
func run(ctx context.Context) (Report, error) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	c, err := mongod.StartWithOptions(ctx, mongod.DefaultOptions().WithLogger(logger))
	if err != nil {
		return Report{}, err
	}
	defer func() { _ = c.Stop(context.Background()) }()

	report := Report{Host: c.Host(), Port: c.Port(), Username: c.Username(), Hello: "ok"}
	if err := hello(net.JoinHostPort(c.Host(), strconv.Itoa(c.Port()))); err != nil {
		report.Hello = err.Error()
	}
	docker, err := dockerapi.FromEnv()
	if err != nil {
		return Report{}, err
	}
	info, err := docker.ContainerInspect(ctx, c.ID())
	if err != nil {
		return Report{}, err
	}
	report.Published = info.HostPort("27017/tcp") != ""
	for name := range info.NetworkSettings.Networks {
		report.NetworkMode = name
	}
	return report, nil
}

// hello sends a MongoDB hello and checks that the reply is a MongoDB
// message, which nothing but mongod sends.
func hello(address string) error {
	conn, err := net.DialTimeout("tcp", address, 10*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write(helloMessage()); err != nil {
		return err
	}
	header := make([]byte, 16)
	if _, err := io.ReadFull(conn, header); err != nil {
		return err
	}
	const opMsg = 2013
	if binary.LittleEndian.Uint32(header[12:]) != opMsg {
		return errors.New("the reply is not a MongoDB message")
	}
	return nil
}

// helloMessage is an OP_MSG holding {hello: 1, $db: "admin"}.
func helloMessage() []byte {
	var elements []byte
	elements = append(elements, 0x10)
	elements = append(elements, "hello\x00"...)
	elements = binary.LittleEndian.AppendUint32(elements, 1)
	elements = append(elements, 0x02)
	elements = append(elements, "$db\x00"...)
	elements = binary.LittleEndian.AppendUint32(elements, uint32(len("admin")+1))
	elements = append(elements, "admin\x00"...)
	document := binary.LittleEndian.AppendUint32(nil, uint32(4+len(elements)+1))
	document = append(append(document, elements...), 0)

	body := append(binary.LittleEndian.AppendUint32(nil, 0), 0) // no flags, one document
	body = append(body, document...)
	const opMsg = 2013
	message := binary.LittleEndian.AppendUint32(nil, uint32(16+len(body)))
	message = binary.LittleEndian.AppendUint32(message, 1) // request id
	message = binary.LittleEndian.AppendUint32(message, 0) // not a reply
	message = binary.LittleEndian.AppendUint32(message, opMsg)
	return append(message, body...)
}
