package bites

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
)

// RegistrationError is the panic value of a route that cannot work (ADR 0007). Its text is the
// route, the reason, and where the route was registered:
//
//	GET /users/{id}: the pattern has {id}, but GetUser.Path has no field for it
//		registered at main.go:14
//
// It is an error so that a test can recover it and read the text with errors.As; it is not
// recovered by the framework, because a program with a refused route is meant to stop (ADR 0028).
type RegistrationError struct{ msg string }

func (e *RegistrationError) Error() string { return e.msg }

// refusal is a reason a route was refused, before the route and where are known. Building it
// with refusalf keeps the compile functions free of the label.
type refusal struct{ reason string }

func (r *refusal) Error() string { return r.reason }

func refusalf(format string, args ...any) error {
	return &refusal{reason: fmt.Sprintf(format, args...)}
}

// registrationFailure builds the panic value: label ("GET /users/{id}"), reason, where.
func registrationFailure(label string, reason error, where string) *RegistrationError {
	return &RegistrationError{msg: label + ": " + reason.Error() + "\n\tregistered at " + where}
}

// pkgDir is this package's source directory, read once when the package is initialised; it is
// how callerWhere tells the framework's own frames from the application's.
var pkgDir = func() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Dir(file)
}()

// callerWhere is "file.go:line" of the first frame outside this package's own non-test files:
// the application's call to Get or Group, however many generic wrappers sit between.
func callerWhere() string {
	var pcs [16]uintptr
	n := runtime.Callers(2, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	for {
		f, more := frames.Next()
		if f.File != "" && !(filepath.Dir(f.File) == pkgDir && !strings.HasSuffix(f.File, "_test.go")) {
			return fmt.Sprintf("%s:%d", filepath.Base(f.File), f.Line)
		}
		if !more {
			return "unknown"
		}
	}
}
