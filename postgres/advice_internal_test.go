package postgres

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The sentence a timed-out start ends with must suit what went wrong: a
// wrong address and a slow server look alike, so advice about the address
// is given only when nothing, or something else, answered there. Each case
// is compared with the package's own advice, not with wording.
func TestTheAdviceForATimeoutSuitsTheFailure(t *testing.T) {
	noReply := fmt.Errorf("%w: connection refused", errNoReply)
	detected := config{hostSource: "loopback, because the daemon is on this machine"}
	chosen := config{hostSource: "WithHostIP"}

	assert.Equal(t, crowdedAdvice, detected.notReadyAdvice(noReply, true), "other starts at once may be the cause")
	assert.Equal(t, allowLonger, detected.notReadyAdvice(&startingUpError{code: sqlstateStartingUp}, false),
		"a server still starting was found, so the address is right")
	assert.Equal(t, allowLonger, detected.notReadyAdvice(nil, false), "nothing points at the address")

	for name, err := range map[string]error{"nothing answered": noReply, "something else answered": errNotPostgres} {
		t.Run(name, func(t *testing.T) {
			fromDetection := detected.notReadyAdvice(err, false)
			assert.True(t, strings.HasPrefix(fromDetection, checkImageAdvice), "a detected address may be right, and the image wrong")
			assert.Contains(t, fromDetection, EnvHostIP, "or the address may be wrong")
			fromCaller := chosen.notReadyAdvice(err, false)
			assert.False(t, strings.HasPrefix(fromCaller, checkImageAdvice), "an address the caller chose is the first suspect")
			assert.Contains(t, fromCaller, EnvHostIP, "and the caller is told how to correct it")
		})
	}
}
