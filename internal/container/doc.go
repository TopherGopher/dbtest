// Package container creates, stops and removes a container this module
// starts, whatever image it runs. It is internal: an image's package, such as
// mongod, keeps a [Container] in an unexported field and offers its own
// methods.
//
//	c := container.New(name, docker, logger) // removed if the process is signalled
//	if err := c.Create(ctx, request); err != nil {
//		c.Forget()
//		return err
//	}
//	defer c.Stop(ctx)
//
// Stop is safe to call more than once, from several goroutines at the same
// time, and while Create is still running: it waits for Create, so a
// container created as the process is signalled is not left behind.
package container
