// Package container starts, stops and removes a container this module
// runs, whatever image it holds. It is internal: an image's package, such as
// mongod, keeps a [Container] in an unexported field and offers its own
// methods.
//
//	c := container.New(name, docker, logger) // removed if the process is signalled
//	port, err := c.Start(ctx, container.Spec{
//		Request: request, Port: "5432/tcp", Host: "127.0.0.1",
//		StartTimeout: time.Minute, Ready: ready,
//	})
//	if err != nil {
//		return err // the container was removed
//	}
//	defer c.Stop(ctx)
//
// The image's package fills in a [Spec]: how to pull its image, what to do
// before the container starts, how to tell it is ready, and what to advise
// when it is not ready in time.
//
// Stop is safe to call more than once and from several goroutines. Called
// while Start is still creating the container, it waits for the daemon's
// answer and removes what was created. This is what removes a container
// when the process is signalled.
package container
