// Command reaphelper is a test helper, never part of the library: the
// reaper's integration test builds it and runs it as a subprocess, and no
// program that uses dbtest ever starts it or sees what it prints.
//
// It starts a MongoDB container and waits to be signalled, so the test can
// check that a signalled process removes its container and still dies of
// the signal, which only a parent process can observe.
//
// It prints "reaphelper: container <id> <name>" once the container is up,
// for the test to read.
package main

import (
	"context"
	"fmt"
	"os"
	"runtime/coverage"
	"time"

	"github.com/tophergopher/dbtest/internal/reaper"
	"github.com/tophergopher/dbtest/mongod"
)

func main() {
	// The tests can build this program with coverage, to count the
	// reaper's signal path, which runs only here. The program dies of the
	// signal, and Go writes coverage only on a normal exit, so write it
	// first. Without -cover the call fails, which is fine.
	reaper.BeforeReRaise = func() { _ = coverage.WriteCountersDir(os.Getenv("GOCOVERDIR")) }

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// The test labels the container so it can find one that leaks.
	c, err := mongod.StartWithOptions(ctx, mongod.DefaultOptions().WithLabel("mongotest.reaphelper", os.Getenv("REAPHELPER_LABEL")))
	if err != nil {
		fmt.Fprintln(os.Stderr, "reaphelper: cannot start a container:", err)
		os.Exit(2)
	}
	fmt.Printf("reaphelper: container %s %s\n", c.ID(), c.Name())

	// Getting past this means no signal arrived, which is a failure.
	time.Sleep(5 * time.Minute)
	fmt.Fprintln(os.Stderr, "reaphelper: no signal arrived")
	os.Exit(3)
}
