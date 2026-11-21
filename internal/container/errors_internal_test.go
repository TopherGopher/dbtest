package container

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// The count is only in the message, so the message is where a caller sees
// it.
func TestTheMessageSaysHowManyContainersWereStarting(t *testing.T) {
	crowded := &NotReadyError{Name: "orders", Timeout: time.Second, starting: 7}
	alone := &NotReadyError{Name: "orders", Timeout: time.Second, starting: 1}

	assert.Contains(t, crowded.Error(), "7", "the message gives the count")
	assert.NotEqual(t, alone.Error(), crowded.Error(), "and a lone start's message differs")
}
