package container

// Starting returns how many starts in this process hold a turn now.
func Starting() int {
	slots.Lock()
	defer slots.Unlock()
	return len(slots.ch)
}

// StartingWhenItEnded returns how many containers were starting when the
// start that returned err ended.
func StartingWhenItEnded(err *NotReadyError) int { return err.starting }
