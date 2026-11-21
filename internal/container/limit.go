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
// capacity is the start limit.
var slots = struct {
	sync.Mutex
	ch chan struct{}
}{ch: make(chan struct{}, defaultStartLimit())}

func defaultStartLimit() int { return startsPerCPU * runtime.NumCPU() }

// SetStartLimit sets how many starts may run at once in this process, and a
// limit below 1 restores the default. Starts already running when it is
// called do not count against the new limit.
func SetStartLimit(n int) {
	if n < 1 {
		n = defaultStartLimit()
	}
	slots.Lock()
	defer slots.Unlock()
	slots.ch = make(chan struct{}, n)
}

// errWaitingForSlot is the LastErr of a start whose context ended before it
// could begin.
var errWaitingForSlot = errors.New("it was still waiting for another start to finish, because as many starts as this process allows at once were running")

// takeSlot waits until fewer starts are running than the limit allows, or
// until ctx ends. It returns the slots the start counts against, and a
// function that frees its slot.
func takeSlot(ctx context.Context) (chan struct{}, func(), error) {
	slots.Lock()
	ch := slots.ch
	slots.Unlock()
	select {
	case ch <- struct{}{}:
		return ch, func() { <-ch }, nil
	case <-ctx.Done():
		return ch, nil, ctx.Err()
	}
}
