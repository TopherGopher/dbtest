package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/dbtest/dockerclient"
	"github.com/tophergopher/dbtest/postgres"
)

func TestTheImageAStartRuns(t *testing.T) {
	cases := []struct {
		name    string
		opts    postgres.Options
		fromEnv string
		want    string
	}{
		{name: "the default", opts: postgres.DefaultOptions(), want: "docker.io/library/postgres:18"},
		{name: "a lone version is the official image at that tag", opts: postgres.DefaultOptions().WithImage("17"),
			want: "docker.io/library/postgres:17"},
		{name: "a name is used as written", opts: postgres.DefaultOptions().WithImage("postgres:17-alpine"),
			want: "postgres:17-alpine"},
		{name: "an extension image keeps its own tags", opts: postgres.DefaultOptions().WithImage("pgvector/pgvector:pg18"),
			want: "pgvector/pgvector:pg18"},
		{name: "nothing carries over from an earlier call", opts: postgres.DefaultOptions().WithImage("17").WithImage("postgis/postgis"),
			want: "postgis/postgis"},
		{name: "the variable when no option names an image", opts: postgres.DefaultOptions(), fromEnv: "public.ecr.aws/docker/library/postgres:18",
			want: "public.ecr.aws/docker/library/postgres:18"},
		{name: "a lone version from the variable", opts: postgres.DefaultOptions(), fromEnv: "16", want: "docker.io/library/postgres:16"},
		{name: "an option wins outright over the variable", opts: postgres.DefaultOptions().WithImage("17"), fromEnv: "mirror.corp.example/postgres",
			want: "docker.io/library/postgres:17"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(postgres.EnvImage, tc.fromEnv)
			m, _ := readyMock(t)
			c := mustStart(t, m, tc.opts)
			_, request := created(t, m)
			assert.Equal(t, tc.want, request.Image, "the container is created from this image")
			assert.Equal(t, tc.want, c.Image(), "and reports it")
		})
	}
}

func TestTheStartTimeout(t *testing.T) {
	cases := []struct {
		name    string
		opts    postgres.Options
		fromEnv string
		want    time.Duration
	}{
		{name: "the default", opts: postgres.DefaultOptions(), want: postgres.DefaultStartTimeout},
		{name: "the variable", opts: postgres.DefaultOptions(), fromEnv: "150ms", want: 150 * time.Millisecond},
		{name: "an option over the variable", opts: postgres.DefaultOptions().WithStartTimeout(120 * time.Millisecond), fromEnv: "1h",
			want: 120 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(postgres.EnvStartTimeout, tc.fromEnv)
			m, _ := servingMock(t, hangUp)
			// The caller's deadline ends a wait of a minute early; the error
			// still reports the start timeout.
			_, err := startWithin(t, m, tc.opts, 300*time.Millisecond)
			var notReady *postgres.NotReadyError
			require.ErrorAs(t, err, &notReady, "nothing answers, so the start ends not ready")
			assert.Equal(t, tc.want, notReady.Timeout, "after the timeout that applies")
		})
	}
}

func TestOptionsThatCannotWorkAreRefused(t *testing.T) {
	cases := []struct {
		name    string
		opts    postgres.Options
		env     map[string]string
		matches error
	}{
		{name: "an empty image", opts: postgres.DefaultOptions().WithImage(" "), matches: postgres.ErrEmptyImage},
		{name: "an image that is not a reference", opts: postgres.DefaultOptions().WithImage("Postgres:17")},
		{name: "an image with a scheme", opts: postgres.DefaultOptions().WithImage("https://docker.io/postgres")},
		{name: "a version docker cannot use as a tag", opts: postgres.DefaultOptions().WithImage("17+1")},
		{name: "an empty host address", opts: postgres.DefaultOptions().WithHostIP(""), matches: postgres.ErrEmptyHostIP},
		{name: "a port out of range", opts: postgres.DefaultOptions().WithPort(70000)},
		{name: "a negative start timeout", opts: postgres.DefaultOptions().WithStartTimeout(-time.Second)},
		{name: "a negative pull timeout", opts: postgres.DefaultOptions().WithPullTimeout(-time.Second)},
		{name: "an image variable that is not a reference", opts: postgres.DefaultOptions(), env: map[string]string{postgres.EnvImage: "not an image"}},
		{name: "a start timeout variable that is not a duration", opts: postgres.DefaultOptions(), env: map[string]string{postgres.EnvStartTimeout: "soon"}},
		{name: "a start timeout variable that is not positive", opts: postgres.DefaultOptions(), env: map[string]string{postgres.EnvStartTimeout: "0s"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for name, value := range tc.env {
				t.Setenv(name, value)
			}
			m, _ := readyMock(t)
			_, err := start(t, m, tc.opts)
			assert.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "a mistake in the options is the caller's to correct")
			if tc.matches != nil {
				assert.ErrorIs(t, err, tc.matches, "and has its own sentinel")
			}
			assert.Empty(t, m.CallsTo("ContainerCreate"), "nothing is created for options that cannot work")
		})
	}
}

func TestPullImageRefusesTheSameMistakes(t *testing.T) {
	m, _ := readyMock(t)
	err := postgres.PullImage(t.Context(), postgres.DefaultOptions().WithDocker(m).WithImage(""))
	assert.ErrorIs(t, err, postgres.ErrEmptyImage, "PullImage checks the options as a start does")
	assert.Empty(t, m.CallsTo("ImagePull"), "and pulls nothing")
}

func TestPullImagePullsOnlyAMissingImage(t *testing.T) {
	m, _ := readyMock(t)
	require.NoError(t, postgres.PullImage(t.Context(), postgres.DefaultOptions().WithDocker(m)), "an image the daemon has needs nothing")
	assert.Empty(t, m.CallsTo("ImagePull"), "so nothing is pulled")

	m.ImageInspectFunc = func(_ context.Context, ref string) (dockerclient.ImageInspect, error) {
		return dockerclient.ImageInspect{}, dockerclient.ErrNotFound
	}
	require.NoError(t, postgres.PullImage(t.Context(), postgres.DefaultOptions().WithDocker(m)), "a missing image is pulled")
	require.Len(t, m.CallsTo("ImagePull"), 1, "once")
	assert.Equal(t, "docker.io/library/postgres:18", m.CallsTo("ImagePull")[0].Args[0], "the image a start would run")
}

func TestWithNameNamesTheContainer(t *testing.T) {
	m, _ := readyMock(t)
	c := mustStart(t, m, postgres.DefaultOptions().WithName("orders-db"))
	name, _ := created(t, m)
	assert.Equal(t, "orders-db", name, "the daemon is asked for that name")
	assert.Equal(t, "orders-db", c.Name(), "and the container reports it")
}

func TestWithLabelAddsToTheRequiredLabel(t *testing.T) {
	m, _ := readyMock(t)
	mustStart(t, m, postgres.DefaultOptions().WithLabel("team", "orders"))
	_, request := created(t, m)
	assert.Equal(t, "orders", request.Labels["team"], "the caller's label is set")
	assert.Equal(t, "regression", request.Labels["mongotest"], "and the label the cleanup command finds is kept")
}

func TestAWithMethodLeavesTheOptionsItWasCalledOnAlone(t *testing.T) {
	base := postgres.DefaultOptions().WithLabel("team", "orders")
	_ = base.WithLabel("team", "billing")
	m, _ := readyMock(t)
	mustStart(t, m, base)
	_, request := created(t, m)
	assert.Equal(t, "orders", request.Labels["team"], "a base shared between tests is never changed by one of them")
}

func TestANilLoggerIsIgnored(t *testing.T) {
	logger := &levelLogger{}
	m, _ := readyMock(t)
	mustStart(t, m, postgres.DefaultOptions().WithLogger(logger).WithLogger(nil))
	assert.NotEmpty(t, logger.at("info"), "the logger given first still gets the start's lines")
}
