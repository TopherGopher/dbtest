// Package postgres starts and stops PostgreSQL containers for tests.
//
//	c, err := postgres.Start(ctx)
//	if err != nil {
//		return err
//	}
//	defer c.Stop(context.Background())
//
//	db, err := sql.Open("pgx", c.URI()) // import _ "github.com/jackc/pgx/v5/stdlib"
//
// That runs docker.io/library/postgres:18, the official image, and waits up
// to a minute until the server answers a login with anything but "starting
// up". Each start gets a fresh, empty database, with a superuser "postgres"
// whose password is generated for it. The package imports no driver: hand
// [Container.URI] to whichever one you use. For a pgx pool and a *sql.DB
// already connected, use github.com/tophergopher/dbtest/pgtest instead.
//
// # Options
//
// [StartWithOptions] takes [Options], built by chaining With methods onto
// [DefaultOptions]. Each returns a copy, so a shared base is never changed:
//
//	base := postgres.DefaultOptions().WithImage("17")
//	c, err := postgres.StartWithOptions(ctx, base.WithLabel("team", "orders"))
//
// Three settings can also come from the environment, so CI can configure a
// whole suite. An option overrides the variable:
//
//	DBTEST_POSTGRES_IMAGE           the image, in any shape WithImage accepts
//	DBTEST_POSTGRES_START_TIMEOUT   how long to wait for the server, such as "90s"
//	DBTEST_HOST_IP                  the address the tests reach containers at
//
// A start pulls an image the daemon does not have. When Docker Hub cannot
// serve an official image, because of its rate limit or an outage, the pull
// tries Amazon ECR Public's copy. Call [PullImage] in TestMain, so the
// download happens before any test starts.
//
// # Cleaning up
//
// Stop removes the container and its data. A container that is never
// stopped is still removed if the process gets SIGINT or SIGTERM.
// [ReapRunningContainers] removes every container dbtest started in this
// process, MongoDB's included: call it after m.Run in TestMain. SIGKILL
// cannot be handled, so its containers are left running. Every container
// dbtest starts carries the label mongotest=regression, so one command
// finds them all:
//
//	docker rm -fv $(docker ps -aq --filter label=mongotest=regression)
//
// # What is not here
//
// The package has no Exec or RunScript: run SQL through your driver. To look
// inside a container while a test is paused, run
// docker exec -it <name> psql -U postgres.
package postgres
