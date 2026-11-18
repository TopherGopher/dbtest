// Package mongod starts and stops MongoDB containers for tests.
//
//	c, err := mongod.Start(ctx)
//	if err != nil {
//		return err
//	}
//	defer c.Stop(context.Background())
//
//	client, err := mongo.Connect(options.Client().ApplyURI(c.URI()))
//
// That runs docker.io/library/mongo:8.0, the official image, on a host port
// the daemon chooses and waits up to a minute for mongod. The package imports no MongoDB driver: hand
// [Container.URI] to whichever one you use, and with [Options.WithTLS] also
// [Container.TLSConfig].
//
// # Options
//
// [StartWithOptions] takes [Options], built by chaining With methods onto
// [DefaultOptions]. Each returns a copy, so a shared base is never changed:
//
//	base := mongod.DefaultOptions().WithImage("7.0")
//	c, err := mongod.StartWithOptions(ctx, base.WithReplicaSet("rs0"))
//
// Three settings can also come from the environment, so CI can configure a
// whole suite. An option overrides the variable:
//
//	MONGOTEST_IMAGE           the image, in any shape WithImage accepts
//	MONGOTEST_START_TIMEOUT   how long to wait for mongod, such as "90s"
//	MONGOTEST_HOST_IP         the address containers are reachable at
//
// # Images
//
// The known images, and what a start runs for each when no version is named:
//
//	ImageDockerHub    docker.io/library/mongo:8.0                            the default
//	ImagePublicECR    public.ecr.aws/docker/library/mongo:8.0                no Docker Hub rate limit
//	ImageCommunity    docker.io/mongodb/mongodb-community-server:8.0-ubi9    no bare version tags
//	ImageEnterprise   docker.io/mongodb/mongodb-enterprise-server:8.0-ubi9   needs a licence
//	ImageAtlasLocal   docker.io/mongodb/mongodb-atlas-local:8.0              Atlas Search; own replica set
//
// Every image is named in full, registry included, so nothing depends on a
// daemon's default registry. A short name such as "mongo:7" is accepted
// wherever an image is, and is taken to be Docker Hub's.
//
// A version and an image can be named apart, in either order. A version
// stays until another is named, so both of these run public ECR's mongo:7.0:
//
//	mongod.DefaultOptions().WithImage("7.0").WithMongoImage(mongod.ImagePublicECR)
//	mongod.DefaultOptions().WithMongoImage(mongod.ImagePublicECR).WithImage("7.0")
//
// A start pulls an image the daemon does not have. Calls to Start and
// [PullImage] in one process that need the same image share one pull. A pull
// that fails because a registry is briefly down is tried again for about a
// minute.
//
// When Docker Hub's rate limit refuses a pull, a Docker official image such
// as mongo is pulled from Amazon ECR Public instead. An image under
// mongodb/, such as mongodb/mongodb-community-server, is pulled from
// quay.io/mongodb. For the next five minutes, pulls of that image in this
// process go straight to the mirror. The container runs the image under the
// mirror's name, which [Container.Image] returns. Name an image by digest to
// be sure a mirror serves exactly that content. There is no option to turn
// this off; an image on any other registry is pulled from there alone.
//
// A Docker Hub image with no mirror is tried again for about a minute. If
// the limit still refuses it, the error matches
// [github.com/tophergopher/mongotest/dockerclient.ErrRateLimited], and later
// pulls of that image in this process fail at once for five minutes. So does
// an image whose mirror fails too, and that error names both failures.
//
// Call PullImage in TestMain, so the download happens before any test
// starts. A pull may take up to ten minutes, which
// [Options.WithPullTimeout] changes, and does not count against the wait
// for mongod.
//
// Two mistakes are refused up front, because the containers they produce
// fail without naming the cause: a bare version such as "8.0" for
// ImageCommunity or ImageEnterprise ([ErrNoSuchVersion]), and WithReplicaSet
// or WithMongodArgs for Atlas Local ([ErrUnsupportedForImage]). An image this
// package does not know is treated like the official one.
//
// # When a start returns
//
// A start returns once mongod has answered a command on the address in the
// URI, so the first command a caller sends works. An image that creates a
// user or a replica set first runs a temporary mongod, then replaces it;
// the start waits for the final one, and for the image's own health check
// if it declares one. With [Options.WithReplicaSet] it also initiates the
// set and waits until it accepts writes.
//
// # Where to connect
//
// The published port is on the machine the daemon runs on, which is not
// always this one. A start works the address out: a remote daemon's host,
// loopback for a local one, or the gateway when this process is itself in a
// container. [Options.WithHostIP] lists the order, and overrides it for a
// network the detection gets wrong.
//
// # Who can connect
//
// A local container is published on 127.0.0.1 only, and mongod runs without
// authentication. Any other address needs the port published on every
// interface, so the start creates a root user, "mongotest", with a random
// password. [Container.URI] includes the credentials when there are any, so
// a test that takes its URI from the container runs unchanged in both places.
//
//	mongod.DefaultOptions().WithAuth("admin", "secret")   // always these
//	mongod.DefaultOptions().WithoutAuth()                 // never any
//
// Creating the user makes a start a few seconds slower. Anyone who can use
// the Docker daemon can read the credentials, and traffic is not encrypted.
//
// # Cleaning up
//
// Stop removes the container. A container that is never stopped is still
// removed if the process gets SIGINT or SIGTERM. [ReapRunningContainers]
// removes this process's containers on demand: call it after m.Run in
// TestMain. SIGKILL cannot be handled, so containers outlive it. Each is
// labelled so it can be found, and removed with its volumes:
//
//	docker rm -fv $(docker ps -aq --filter label=mongotest=regression)
package mongod
