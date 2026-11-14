package mongod_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tophergopher/mongotest/dockerclient"
	"github.com/tophergopher/mongotest/mongod"
)

func TestTheImageAStartRuns(t *testing.T) {
	cases := []struct {
		name string
		env  string
		opts []mongod.Option
		want string
	}{
		{name: "no option", want: "mongo:8.0"},
		{name: "a version", opts: []mongod.Option{mongod.WithImage("7.0")}, want: "mongo:7.0"},
		{name: "a repository gets its own default version",
			opts: []mongod.Option{mongod.WithImage("mongodb/mongodb-community-server")},
			want: "mongodb/mongodb-community-server:8.0-ubi9"},
		{name: "a full reference",
			opts: []mongod.Option{mongod.WithImage("localhost:5000/team/mongo:7")},
			want: "localhost:5000/team/mongo:7"},
		{name: "a known image", opts: []mongod.Option{mongod.WithMongoImage(mongod.ImageAtlasLocal)},
			want: "mongodb/mongodb-atlas-local:8.0"},
		{name: "a known image from a mirror",
			opts: []mongod.Option{mongod.WithMongoImage(mongod.ImageDockerHub.WithRegistry("mirror.corp.example"))},
			want: "mirror.corp.example/mongo:8.0"},
		{name: "a later version changes only the version",
			opts: []mongod.Option{mongod.WithMongoImage(mongod.ImagePublicECR), mongod.WithImage("7.0")},
			want: "public.ecr.aws/docker/library/mongo:7.0"},
		{name: "a later image replaces the registry too",
			opts: []mongod.Option{mongod.WithMongoImage(mongod.ImagePublicECR), mongod.WithMongoImage(mongod.ImageDockerHub)},
			want: "mongo:8.0"},
		{name: "a later repository does not keep an earlier registry",
			opts: []mongod.Option{mongod.WithImage("public.ecr.aws/docker/library/mongo:7.0"), mongod.WithImage("mongo")},
			want: "mongo:7.0"},

		{name: "the environment alone", env: "public.ecr.aws/docker/library/mongo:7.0",
			want: "public.ecr.aws/docker/library/mongo:7.0"},
		{name: "the environment with a version from an option", env: "public.ecr.aws/docker/library/mongo",
			opts: []mongod.Option{mongod.WithImage("7.0")}, want: "public.ecr.aws/docker/library/mongo:7.0"},
		{name: "an option naming an image ignores the environment", env: "public.ecr.aws/docker/library/mongo:7.0",
			opts: []mongod.Option{mongod.WithMongoImage(mongod.ImageCommunity)},
			want: "mongodb/mongodb-community-server:8.0-ubi9"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env != "" {
				t.Setenv(mongod.EnvImage, tc.env)
			}

			assert.Equal(t, tc.want, requestFor(t, tc.opts...).Image, "the image the daemon is asked for")
		})
	}
}

func TestWithPortPublishesThere(t *testing.T) {
	request := requestFor(t, mongod.WithPort(27018))

	binding := request.HostConfig.PortBindings["27017/tcp"][0]
	assert.Equal(t, 27018, mustAtoi(t, binding.HostPort), "the daemon is told to publish on the port that was asked for")
}

func TestWithNameNamesTheContainer(t *testing.T) {
	m, _ := readyMock(t)

	c := mustStart(t, m, mongod.WithName("orders-db"))

	assert.Equal(t, "orders-db", createdName(t, m), "the daemon is given the name")
	assert.Equal(t, "orders-db", c.Name(), "and the container reports it")
}

func TestWithLabelAddsToTheRequiredLabel(t *testing.T) {
	request := requestFor(t, mongod.WithLabel("suite", "checkout"), mongod.WithLabel(mongod.RequiredLabelKey, "mine"))

	assert.Equal(t, "checkout", request.Labels["suite"], "the caller's label is set")
	assert.Equal(t, mongod.RequiredLabelValue, request.Labels[mongod.RequiredLabelKey], "and cannot replace the one stray containers are found by")
}

func TestWithMongodArgsReachMongod(t *testing.T) {
	request := requestFor(t, mongod.WithMongodArgs("--quiet"), mongod.WithMongodArgs("--setParameter", "enableTestCommands=1"))

	assert.Equal(t, []string{"--quiet", "--setParameter", "enableTestCommands=1"}, request.Cmd,
		"every flag is passed on in the order given, because a flag's value is the argument after it")
}

func TestTheStartTimeout(t *testing.T) {
	waits := func(t *testing.T, opts ...mongod.Option) time.Duration {
		m, _ := readyMock(t)
		m.ContainerTopFunc = func(context.Context, string) (dockerclient.Top, error) { return processes("runc init"), nil }
		_, err := start(t, m, opts...)
		var notReady *mongod.NotReadyError
		require.ErrorAs(t, err, &notReady, "a mongod that never appears times out")
		return notReady.Timeout
	}

	t.Run("from the option", func(t *testing.T) {
		assert.Equal(t, 40*time.Millisecond, waits(t, mongod.WithStartTimeout(40*time.Millisecond)), "the option sets how long Start waits")
	})
	t.Run("from the environment", func(t *testing.T) {
		t.Setenv(mongod.EnvStartTimeout, "30ms")
		assert.Equal(t, 30*time.Millisecond, waits(t), "CI can set it for a whole suite")
	})
	t.Run("the option beats the environment", func(t *testing.T) {
		t.Setenv(mongod.EnvStartTimeout, "10m")
		assert.Equal(t, 40*time.Millisecond, waits(t, mongod.WithStartTimeout(40*time.Millisecond)), "one test can still override the suite")
	})
}

func TestLaterOptionsOverrideEarlierOnes(t *testing.T) {
	m, _ := readyMock(t)

	c := mustStart(t, m, mongod.WithName("first"), mongod.WithName("second"))

	assert.Equal(t, "second", c.Name(), "options apply in the order given")
}

// Each of these fails before anything is created, so the error can name the
// option to change.
func TestOptionsThatCannotWorkAreRefused(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		opts []mongod.Option
		want error
	}{
		{name: "a port out of range", opts: []mongod.Option{mongod.WithPort(70000)}, want: dockerclient.ErrInvalidArgument},
		{name: "a negative port", opts: []mongod.Option{mongod.WithPort(-1)}, want: dockerclient.ErrInvalidArgument},
		{name: "a negative timeout", opts: []mongod.Option{mongod.WithStartTimeout(-time.Second)}, want: dockerclient.ErrInvalidArgument},
		{name: "an empty image", opts: []mongod.Option{mongod.WithImage("")}, want: mongod.ErrEmptyImage},
		{name: "an image docker would refuse", opts: []mongod.Option{mongod.WithImage("Mongo:8")}, want: dockerclient.ErrInvalidArgument},
		{name: "an empty host", opts: []mongod.Option{mongod.WithHostIP("")}, want: mongod.ErrEmptyHostIP},
		{name: "a bare version for the community image",
			opts: []mongod.Option{mongod.WithMongoImage(mongod.ImageCommunity.WithVersion("8"))}, want: mongod.ErrNoSuchVersion},
		{name: "a replica set for Atlas Local, which runs its own",
			opts: []mongod.Option{mongod.WithMongoImage(mongod.ImageAtlasLocal), mongod.WithReplicaSet("rs0")}, want: mongod.ErrUnsupportedForImage},
		{name: "an image in the environment that docker would refuse",
			env: map[string]string{mongod.EnvImage: "Bad Ref!!"}, want: dockerclient.ErrInvalidArgument},
		{name: "a timeout in the environment that is not a duration",
			env: map[string]string{mongod.EnvStartTimeout: "soon"}, want: dockerclient.ErrInvalidArgument},
		{name: "a timeout of zero in the environment",
			env: map[string]string{mongod.EnvStartTimeout: "0s"}, want: dockerclient.ErrInvalidArgument},
		{name: "a negative timeout in the environment",
			env: map[string]string{mongod.EnvStartTimeout: "-5s"}, want: dockerclient.ErrInvalidArgument},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for name, value := range tc.env {
				t.Setenv(name, value)
			}
			m, _ := readyMock(t)

			_, err := start(t, m, tc.opts...)

			require.ErrorIs(t, err, tc.want, "the mistake is reported")
			assert.Empty(t, m.CallsTo("ContainerCreate"), "before anything is created")
		})
	}
}

func TestAtlasLocalTakesNoMongodFlags(t *testing.T) {
	m, _ := readyMock(t)

	_, err := start(t, m, mongod.WithMongoImage(mongod.ImageAtlasLocal), mongod.WithMongodArgs("--quiet"))

	var unsupported *mongod.UnsupportedForImageError
	require.ErrorAs(t, err, &unsupported, "flags would replace the program the image runs")
	assert.Equal(t, "WithMongodArgs", unsupported.Option, "the error names the option to remove")
}

func TestAKnownImageIsRecognisedBehindAProxyPath(t *testing.T) {
	m, _ := readyMock(t)
	behindProxy := mongod.WithImage("artifactory.corp.example/docker-remote/mongodb/mongodb-atlas-local:8.0")

	_, err := start(t, m, behindProxy, mongod.WithReplicaSet("rs0"))
	assert.ErrorIs(t, err, mongod.ErrUnsupportedForImage, "Atlas Local runs its own replica set whatever path it is pulled through")

	lookalike := mongod.WithImage("registry.corp.example/team/not-mongodb-atlas-local:8.0")
	_, err = start(t, m, lookalike, mongod.WithReplicaSet("rs0"))
	assert.NoError(t, err, "a repository that only ends in the same letters is a different image")
}

func TestANilLoggerIsIgnored(t *testing.T) {
	m, _ := readyMock(t)

	c, err := start(t, m, mongod.WithLogger(nil))

	require.NoError(t, err, "a logger that was never set must not break a start")
	assert.NoError(t, c.Stop(context.Background()), "or a stop")
}

// An option mistake is mongod's to report: "docker: invalid credentials"
// reads like a failed registry login.
func TestOptionMistakesAreReportedAsMongodsOwn(t *testing.T) {
	mistakes := map[string]mongod.Option{
		"half-set credentials": mongod.WithAuth("admin", ""),
		"a port out of range":  mongod.WithPort(70000),
		"an empty image":       mongod.WithImage(""),
		"a negative timeout":   mongod.WithStartTimeout(-time.Second),
	}
	for name, mistake := range mistakes {
		t.Run(name, func(t *testing.T) {
			m, _ := readyMock(t)

			_, err := start(t, m, mistake)

			require.ErrorIs(t, err, dockerclient.ErrInvalidArgument, "it is still an invalid argument")
			var fromDocker *dockerclient.InvalidArgumentError
			assert.NotErrorAs(t, err, &fromDocker, "but not one the Docker client reports")
		})
	}
}
