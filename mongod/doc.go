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
// That runs docker.io/library/mongo:8.0, the official image, and waits up to
// a minute for mongod. The package imports no MongoDB driver: hand
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
//	DBTEST_HOST_IP            the address containers are reachable at
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
// When Docker Hub's rate limit refuses a pull, or Docker Hub is still down
// after those retries, the pull tries a mirror that serves the same image:
// Amazon ECR Public for a Docker official image such as mongo, and
// quay.io/mongodb for an image under mongodb/, such as
// mongodb/mongodb-community-server. Other images have no mirror, and an image
// named on another registry is pulled from there alone. A pull stops at
// Docker Hub, without trying the mirror, when the image is missing or private
// there. The mirrors cannot be turned off.
//
// An image pulled from a mirror keeps the mirror's name, such as
// public.ecr.aws/docker/library/mongo:8.0. The container runs under that
// name, and [Container.Image] returns it. The mirrors serve the same image
// as Docker Hub, so the tag is enough.
//
// After a rate limit refuses a pull from Docker Hub or a mirror, that
// registry is skipped for five minutes; a pull with nowhere else to go fails
// at once until then. If Docker Hub and the mirror both fail, the error says
// why each one failed. It matches
// [github.com/tophergopher/dbtest/dockerclient.ErrRateLimited] if either was
// a rate limit.
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
// if it declares one. That check can pass while the temporary mongod still
// answers. So once it passes, the start waits a few seconds and asks again
// on the same connection. If that connection has dropped, the temporary
// mongod has gone, and the start keeps waiting. With [Options.WithReplicaSet] it also initiates the
// set and waits until it accepts writes.
//
// # Many containers at once
//
// At most 8 containers per CPU start at once in a process, and further
// starts wait their turn. A start counts against the limit until it
// returns, so the limit caps how many containers are starting, not how many
// are running. The wait counts against the context passed to the start, not
// against the start timeout. [SetMaxConcurrentStarts] changes the limit.
//
// # Where to connect
//
// mongod is not always reachable at loopback. A start dials the first of
// these that applies: mongod's address on the network of this process's
// container, with nothing published; or a port the daemon publishes, on a
// remote daemon's host, on loopback for a local daemon, or at the default
// gateway. [Options.WithHostIP] lists the rules, and overrides them when the
// detection is wrong.
//
// # Who can connect
//
// A container published on 127.0.0.1 runs mongod without authentication,
// because only this machine can reach it. When its port is published on
// every interface, or it joins a network shared with this process's
// container, others can reach it, so the start creates a root user,
// "mongotest", with a random password. [Container.URI] includes
// the credentials when there are any, so a test that takes its URI from the
// container runs unchanged either way.
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
// removed if the process gets SIGINT or SIGTERM, and the process then exits
// as the signal would make it, whatever handlers the program installed. A
// program with its own shutdown calls [DisableSignalHandler].
// [ReapRunningContainers]
// removes this process's containers on demand: call it after m.Run in
// TestMain. SIGKILL cannot be handled, so its containers are left running.
// Each container carries the label [RequiredLabelKey]=[RequiredLabelValue],
// so it can be found and removed with its volumes:
//
//	docker rm -fv $(docker ps -aq --filter label=mongotest=regression)
package mongod
