// Package testlog is the one door bitetest uses to replace the logger an App was not given
// (ADR 0027). bites sets Set when it is imported; bitetest calls it from Start. It is internal
// so that no application can swap an App's logger after New.
package testlog

import "log/slog"

// Set gives app the logger l unless app was given one with WithLogger. It reports whether it
// did. app is a *bites.App; the type is hidden here because this package cannot import it.
var Set func(app any, l *slog.Logger) bool
