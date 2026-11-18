// Package container stops and removes a container this module created,
// whatever image it runs. It is internal: an image's package, such as mongod,
// keeps a [Container] in an unexported field and offers its own methods.
//
//	c := container.New(name, docker, logger) // registers with the reaper
//	err := c.Create(ctx, request)
//	defer c.Stop(ctx)
//
// Stop is safe to call twice, at once from several goroutines, and while the
// container is still being created: it waits for the daemon's answer, so a
// container created as the process is signalled is not left behind.
package container
