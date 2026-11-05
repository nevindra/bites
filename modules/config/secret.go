package config

import (
	"fmt"
	"log/slog"
)

const redacted = "[redacted]"

// Secret is a setting whose text must never be printed: a password, a key, a token. Reveal is
// the only way to the text. Printing the value, with Println, %v, %+v or %#v, logging it with
// log/slog, or encoding the whole configuration as JSON or text, writes "[redacted]".
//
// A Secret has no flag, because the command line is visible in ps and /proc. It is set by the
// environment (APP_DB_URL), by a file whose path is in the variable with the suffix _FILE
// (APP_DB_URL_FILE, read through SecretFiles) or by a configuration file. The zero Secret is
// empty, which is a value like any other string.
type Secret string

// Reveal returns the secret's text. It is the only way to it.
func (s Secret) Reveal() string { return string(s) }

// String returns "[redacted]".
func (s Secret) String() string { return redacted }

// GoString returns "[redacted]".
func (s Secret) GoString() string { return redacted }

// Format writes "[redacted]" for every verb.
func (s Secret) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(redacted)) }

// MarshalText returns "[redacted]".
func (s Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// MarshalJSON returns the JSON string "[redacted]".
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }

// LogValue returns "[redacted]" as a log/slog value.
func (s Secret) LogValue() slog.Value { return slog.StringValue(redacted) }
