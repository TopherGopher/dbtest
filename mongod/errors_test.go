package mongod_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/tophergopher/mongotest/mongod"
)

// A message is for a person, so its wording is free to change. What must not
// change is that it names the thing the reader has to go and look at.
func TestEveryErrorNamesWhatItIsAbout(t *testing.T) {
	notReady := mongod.NotReadyError{
		Name: "orders-db", ID: containerID, Host: "172.17.0.1", Port: 32768,
		HostSource: "WithHostIP", Timeout: time.Minute, Cause: context.DeadlineExceeded,
	}
	stuck, ended := notReady, notReady
	stuck.LastErr = mongod.ErrHelloRefused
	ended.ContextEnded = true
	cases := []struct {
		name     string
		err      error
		mentions []string
	}{
		{"a step of the start", &mongod.StartError{Op: mongod.OpCreate, Name: "orders-db", Image: "mongo:8.0", Err: errors.New("no space left")},
			[]string{"orders-db", "mongo:8.0", "no space left"}},
		{"no published port", &mongod.UnpublishedPortError{Port: "27017/tcp", Name: "orders-db", ID: containerID},
			[]string{"orders-db", "27017/tcp"}},
		{"not ready in time", &notReady, []string{"orders-db", "172.17.0.1:32768"}},
		{"not ready, with what the wait last saw", &stuck, []string{"orders-db", "172.17.0.1:32768", mongod.ErrHelloRefused.Error()}},
		{"not ready because the caller's context ended", &ended, []string{"orders-db", "172.17.0.1:32768"}},
		{"not ready before the container had an id or a port",
			&mongod.NotReadyError{Name: "orders-db", Timeout: time.Minute, Cause: context.DeadlineExceeded}, []string{"orders-db"}},
		{"an address that cannot be worked out",
			&mongod.UnresolvedHostError{DockerHost: "unix:///var/run/docker.sock", Evidence: "/.dockerenv exists", Err: errors.New("no default route")},
			[]string{"unix:///var/run/docker.sock", "/.dockerenv exists", "no default route"}},
		{"a version the image does not publish", &mongod.NoSuchVersionError{Image: mongod.ImageCommunity.WithVersion("8")},
			[]string{mongod.ImageCommunity.Repository}},
		{"an option the image cannot honour",
			&mongod.UnsupportedForImageError{Image: mongod.ImageAtlasLocal, Option: "WithReplicaSet", Reason: "it runs its own"},
			[]string{mongod.ImageAtlasLocal.Repository, "WithReplicaSet", "it runs its own"}},
		{"credentials that are not enforced", &mongod.AuthNotEnforcedError{Name: "orders-db", ID: containerID}, []string{"orders-db"}},
		{"a container that exited", &mongod.ContainerExitedError{Name: "orders-db", ID: containerID, Status: "exited", ExitCode: 14, HasExitCode: true},
			[]string{"orders-db", "14"}},
		{"a container that was removed", &mongod.ContainerExitedError{Name: "orders-db", ID: containerID, Status: "removed"},
			[]string{"orders-db"}},
		{"a container that exited and said why", &mongod.ContainerExitedError{Name: "orders-db", ID: containerID, Status: "exited",
			ExitCode: 2, HasExitCode: true, Output: "unrecognised option '--x'\n"},
			[]string{"orders-db", "unrecognised option '--x'"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			message := tc.err.Error()

			for _, mention := range tc.mentions {
				assert.Contains(t, message, mention, "the message names what the reader has to look at")
			}
		})
	}
}

// When a start runs out of time, the advice suits what went wrong: the
// address is in doubt only when nothing, or something other than mongod,
// answered; and a mongod that ignores its credentials needs no longer wait.
func TestTheAdviceForAStartThatRanOutOfTimeSuitsTheFailure(t *testing.T) {
	inDoubt := func(lastErr error) bool { return mongod.AddressInDoubt(&mongod.NotReadyError{LastErr: lastErr}) }
	assert.True(t, inDoubt(mongod.ErrNoReply), "nothing answered: maybe the address")
	assert.True(t, inDoubt(mongod.ErrNotMongod), "not mongod: maybe the address")
	assert.False(t, inDoubt(mongod.ErrHelloRefused), "mongod answered: the address is right")
	assert.NotEmpty(t, mongod.NotReadyAdvice(mongod.ErrHelloRefused), "a longer wait may help")
	assert.Empty(t, mongod.NotReadyAdvice(&mongod.AuthNotEnforcedError{Name: "orders-db"}), "no advice to wait for an open mongod")
}
