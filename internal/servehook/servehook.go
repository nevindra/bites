// Package servehook is the one door stdhttp.Handler uses to start an App before it returns
// (ADR 0019). bites sets Start when it is imported. It is internal so that no application can
// start an App by a way other than Listen, Serve and stdhttp.Handler.
package servehook

import "net"

// Start does now what Serve does first: it freezes app's routes and builds its server, so a route
// registered after it panics. It returns the function that serves ln until Shutdown or Close,
// which the caller runs on a goroutine of its own. It also runs the app's services' Start
// functions (ADR 0079) and returns the error of a failed one, with the listener closed and no
// serve function. app is a *bites.App; the type is hidden here
// because this package cannot import it.
var Start func(app any, ln net.Listener) (serve func() error, err error)
