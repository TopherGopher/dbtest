package dockerclient_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/tophergopher/dbtest/dockerclient"
)

// A pull that finds nothing, or a create whose name is taken, has one likely
// cause, and the generic advice for its status code names others that
// cannot apply, such as an object removed already.
func TestAHintSuitsTheRequestThatFailed(t *testing.T) {
	notFound := &dockerclient.StatusError{StatusCode: 404, Method: "GET", Path: "/containers/c1/json"}
	pullNotFound := &dockerclient.StatusError{StatusCode: 404, Method: "POST", Path: "/images/create"}
	assert.NotEqual(t, notFound.Hint(), pullNotFound.Hint(), "a pull that finds nothing gets advice about the image's name and tags")
	assert.NotEmpty(t, pullNotFound.Hint(), "and it still gets advice")

	conflict := &dockerclient.StatusError{StatusCode: 409, Method: "POST", Path: "/containers/c1/start"}
	nameTaken := &dockerclient.StatusError{StatusCode: 409, Method: "POST", Path: "/containers/create"}
	assert.NotEqual(t, conflict.Hint(), nameTaken.Hint(), "a create whose name is taken gets advice about that container")
	assert.NotEmpty(t, nameTaken.Hint(), "and it still gets advice")
}
