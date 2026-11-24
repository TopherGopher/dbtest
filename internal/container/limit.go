package container

import (
	"context"
	"errors"
	"runtime"
	"sync"
)

// startsPerCPU sets the default limit. Starting containers is bound by CPU.
// The number is low enough that replica set starts, which each run a shell
// that needs memory, also fit.
const startsPerCPU = 8

// slots holds one value for each start under way in this process. Its
// capacity is the limit.
var slots = struct {
	sync.Mutex
	ch chan struct{}
}{ch: make(chan struct{}, defaultMaxConcurrentStarts())}

func defaultMaxConcurrentStarts() int { return startsPerCPU * runtime.NumCPU() }

// SetMaxConcurrentStarts sets how many containers may be starting at once in
// this process, and returns the previous limit. A value below 1 restores the
// default. Starts already running or waiting keep the old limit.
func SetMaxConcurrentStarts(n int) int {
	if n < 1 {
		n = defaultMaxConcurrentStarts()
	}
	slots.Lock()
	defer slots.Unlock()
	previous := cap(slots.ch)
	slots.ch = make(chan struct{}, n)
	return previous
}

// errWaitingForTurn is the LastErr of a start whose context ended before it
// could begin.
var errWaitingForTurn = errors.New("it was still waiting for a turn, because as many containers as the limit allows were starting")

// takeSlot waits until fewer starts are running than the limit allows, or
// until ctx ends. It returns the slots the start counts against, and a
// function that frees its slot.
func takeSlot(ctx context.Context) (chan struct{}, func(), error) {
	slots.Lock()
	ch := slots.ch
	slots.Unlock()
	// select picks at random when both cases are ready, so a context that
	// has already ended is checked first, and starts nothing.
	if err := ctx.Err(); err != nil {
		return ch, nil, err
	}
	select {
	case ch <- struct{}{}:
		return ch, func() { <-ch }, nil
	case <-ctx.Done():
		return ch, nil, ctx.Err()
	}
}

// crowded reports whether other starts and this one fill the limit. A lone
// start at a limit of one is not crowded: nothing else slowed it.
func crowded(slots chan struct{}) bool {
	n := len(slots)
	return n > 1 && n == cap(slots)
}
