package bites

import (
	"fmt"
	"strings"

	"github.com/nevindra/bites/internal/engine"
)

// Encoder is one content coding the engine can compress an answer with (ADR 0061): gzip from the
// core (GzipEncoder), zstd and brotli from the modules github.com/nevindra/bites/modules/zstd
// and github.com/nevindra/bites/modules/brotli. An application rarely writes one.
//
// Coding is the token Accept-Encoding and Content-Encoding name it by ("gzip", "zstd", "br"), a
// lowercase token read once when New runs. Encode appends the compressed form of src to dst and
// returns it; it is called from many connections at once, and must keep neither src nor dst past
// its return, because both are the engine's pooled buffers (ADR 0008): the contract io.Writer
// states for its argument. A panic in Encode is recovered and the answer sent uncompressed.
type Encoder interface {
	Coding() string
	Encode(dst, src []byte) []byte
}

// GzipEncoder is the core's gzip Encoder: compress/gzip at BestSpeed, its writers pooled (ADR 0031).
func GzipEncoder() Encoder { return engine.Gzip() }

// Compression configures response compression with the encoders the app offers (ADR 0031,
// ADR 0061). The engine compresses a finished body of at least MinSize with the encoder the
// request's Accept-Encoding weighs highest; on a tie it takes the one listed first here. A
// middleware sees the uncompressed answer.
type Compression struct {
	// MinSize is the smallest body that is compressed, in bytes. Zero is the default of 1 KiB; a
	// value below 1 KiB makes New panic.
	MinSize int

	// Encoders are the codings offered, in the server's order of preference: list the one that
	// makes the smallest answer first, such as brotli.Encoder(), zstd.Encoder(),
	// bites.GzipEncoder(). Empty is gzip alone. Each coding is listed once.
	Encoders []Encoder
}

// MinCompressSize is the floor of Compression.MinSize and Gzip.MinSize, and their default.
const MinCompressSize = engine.CompressFloor

// WithCompression turns response compression on with the encoders c lists (ADR 0061). It is off
// without this option or WithGzip, which is this option with gzip alone. New panics when it is
// given twice or beside WithGzip, for a MinSize below 1 KiB, a nil Encoder, a Coding that is not
// a lowercase token (or is "identity" or "*"), and a coding listed twice.
func WithCompression(c Compression) Option {
	return func(a *App) {
		where := callerWhere()
		if a.compressBy != "" {
			a.refuseCompressTwice("WithCompression", where, "give one Compression that lists every Encoder, in the order the server prefers")
		}
		fail := func(format string, args ...any) {
			panic(&RegistrationError{msg: "WithCompression: " + fmt.Sprintf(format, args...) + "\n\tregistered at " + where})
		}
		if c.MinSize != 0 && c.MinSize < engine.CompressFloor {
			fail("Compression.MinSize is %d, which is below the floor of %d bytes: under about 1.2 KiB an answer fits one TCP segment, so compressing it saves bytes and no packets; give %d or more, or zero for the default of 1 KiB", c.MinSize, engine.CompressFloor, engine.CompressFloor)
		}
		encs := append([]Encoder(nil), c.Encoders...)
		if len(encs) == 0 {
			encs = []Encoder{GzipEncoder()}
		}
		seen := map[string]int{}
		for i, e := range encs {
			if e == nil {
				fail("Encoders[%d] is nil", i)
			}
			cd := e.Coding()
			if !isToken(cd) || strings.ToLower(cd) != cd || cd == "identity" || cd == "*" {
				fail("Encoders[%d] has the coding %q, which is not a lowercase content coding token such as \"br\", \"zstd\" or \"gzip\"", i, cd)
			}
			if j, ok := seen[cd]; ok {
				fail("Encoders[%d] has the coding %q, as Encoders[%d] has: list each coding once, in the order the server prefers", i, cd, j)
			}
			seen[cd] = i
		}
		a.compressBy, a.compressWhere = "WithCompression", where
		a.compress = Compression{MinSize: c.MinSize, Encoders: encs}
		a.custom["compression"] = true
	}
}

// refuseCompressTwice is the refusal of a second compression option, naming both places.
func (a *App) refuseCompressTwice(fn, where, advice string) {
	what := "given twice"
	if a.compressBy != fn {
		what = "given after " + a.compressBy
	}
	panic(&RegistrationError{msg: fn + ": " + what + " (first at " + a.compressWhere + ", again at " + where + "): " + advice})
}

// compressConfig is what the engine is given: no encoder when compression is off.
func (a *App) compressConfig() engine.CompressConfig {
	if a.compressBy == "" {
		return engine.CompressConfig{}
	}
	cfg := engine.CompressConfig{MinSize: a.compress.MinSize}
	for _, e := range a.compress.Encoders {
		cfg.Encoders = append(cfg.Encoders, e)
	}
	return cfg
}

// codings is the start-up log's list of the codings offered, in the server's order.
func (a *App) codings() string {
	var b strings.Builder
	for i, e := range a.compress.Encoders {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(e.Coding())
	}
	return b.String()
}
