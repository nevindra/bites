package bites

import (
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/nevindra/bites/internal/engine"
	"github.com/nevindra/bites/internal/jsoncodec"
	"github.com/nevindra/bites/internal/wire"
)

// problemType is the content type of every error answer the framework writes (ADR 0012).
const problemType = "application/problem+json"

// maxErrors is the most errors one 400 lists (ADR 0012, ADR 0024).
const maxErrors = 20

// StatusError is an error that carries its own HTTP status (ADR 0049). A handler returns one,
// directly or wrapped with %w, and the answer has that status and this message:
//
//	return User{}, fmt.Errorf("load user: %w", bites.NotFound("no such user"))
//
// The outer text ("load user") never reaches the client; only Message does. Any other error is a
// 500 with no message. The framework maps no driver's errors: turning pgx.ErrNoRows into a 404
// is a line of the application's, in a handler or in WithErrorMapper.
type StatusError struct {
	// Status is the HTTP status of the answer, from 400 to 599; any other is a 500, logged.
	Status int
	// Message is the detail of the answer, and the only text of the error the client sees.
	Message string
	members map[string]any
	headers []headerPair
}

func (e *StatusError) Error() string {
	if e.Message == "" {
		return strconv.Itoa(e.Status) + " " + engine.StatusText(e.Status)
	}
	return e.Message
}

// With returns a copy that also carries a problem+json member, such as a machine-readable code
// (ADR 0049). The members title, status and detail belong to the framework and are ignored.
func (e *StatusError) With(name string, value any) *StatusError {
	c := *e
	c.members = make(map[string]any, len(e.members)+1)
	for k, v := range e.members {
		c.members[k] = v
	}
	c.members[name] = value
	return &c
}

// WithHeader returns a copy whose answer also carries the response header name: value, after
// any it already carries. It is how a Provider, which answers only with an error, sends the
// WWW-Authenticate challenge of a 401 or the Retry-After of a 429 (ADR 0054, ADR 0055). The
// framework's own headers (Content-Type, Content-Length, Date, Connection, Transfer-Encoding) are
// the engine's: a header of that name, or a name that is not an HTTP token, is left out and
// logged. A CR, LF or NUL in the value is replaced by a space, as in every header.
func (e *StatusError) WithHeader(name, value string) *StatusError {
	c := *e
	c.headers = append(e.headers[:len(e.headers):len(e.headers)], headerPair{name, value})
	return &c
}

// HasStatus is implemented by an application's own error type to say which HTTP answer it is,
// so a handler returns it, wrapped or not, without converting it (ADR 0049):
//
//	func (e *NotFoundError) HTTPStatus() (int, string) { return 404, e.What + " does not exist" }
//
// It is found through the same walk of the error chain as a StatusError, and the nearer of the
// two wins. The status should be 400 or above (any other is answered as a 500 and logged, as for
// NewError), and the message is the whole of what the client reads: the text of the errors that
// wrap it never reaches the client. An empty message is a body with a title and a status only. The
// answer carries no extra members and no headers; an error that needs them is converted with
// NewError, With and WithHeader in the handler.
type HasStatus interface {
	error
	HTTPStatus() (status int, message string)
}

// findStatus looks for the status an error stands for: the nearest *StatusError or HasStatus in
// its chain, through Unwrap and through the branches of a joined error, in the order errors.As
// would visit them. It returns nil when the chain has none. A HasStatus costs one StatusError on
// the way out; a success pays nothing.
func findStatus(err error) *StatusError {
	for err != nil {
		switch e := err.(type) {
		case *StatusError:
			return e
		case HasStatus:
			st, msg := e.HTTPStatus()
			return &StatusError{Status: st, Message: msg}
		}
		switch u := err.(type) {
		case interface{ Unwrap() error }:
			err = u.Unwrap()
		case interface{ Unwrap() []error }:
			for _, c := range u.Unwrap() {
				if se := findStatus(c); se != nil {
					return se
				}
			}
			return nil
		default:
			return nil
		}
	}
	return nil
}

// WithErrorMapper gives the app a function that turns an error of a type the application does not
// own, and so cannot give an HTTPStatus method, into the answer it stands for (ADR 0049):
//
//	bites.WithErrorMapper(func(err error) *bites.StatusError {
//		var pg *pgconn.PgError
//		switch {
//		case errors.Is(err, pgx.ErrNoRows):
//			return bites.NotFound("no such record")
//		case errors.As(err, &pg) && pg.Code == "23505":
//			return bites.Conflict("already exists").With("code", "duplicate")
//		}
//		return nil
//	})
//
// The function is given the error as it was returned, wrapped and joined as it came, and returns
// the answer, or nil for an error it does not know. It is asked only for an error whose chain has
// no *StatusError and no HasStatus, so the application's own typed answer always wins, and it is
// asked for every error that is answered: a handler's, a Provider's, a middleware's, a stream's
// and a WebSocket upgrade's. An answer is exactly a StatusError's: its message is the detail, an
// empty one leaves it out, and what With and WithHeader added is sent. The message of a 5xx is
// sent as written, so write one the client may read. A mapped 4xx is not logged; a mapped 5xx is,
// at Error with the original error. A status below 400 or above 599 is a logged 500. A function
// that panics is recovered: the panic is logged and the answer is the 500 of an unmapped error.
//
// It may be given more than once, and the functions are asked in the order given, the first
// answer winning. A nil function makes New panic. It is the app's, not a Group's: a driver's error
// means the same on every route.
func WithErrorMapper(fn func(error) *StatusError) Option {
	where := callerWhere()
	return func(a *App) {
		if fn == nil {
			panic(&RegistrationError{msg: "WithErrorMapper: the function is nil: give the function that turns an error into a *StatusError, or leave the option out\n\tregistered at " + where})
		}
		a.errMappers = append(a.errMappers, fn)
		a.custom["errorMapper"] = true
	}
}

// mapError asks the app's mappers, in order, for the answer an error stands for. A mapper that
// panics is logged and counts as having no answer, and none after it is asked.
func (a *App) mapError(err error) (se *StatusError) {
	if len(a.errMappers) == 0 {
		return nil
	}
	defer func() {
		if p := recover(); p != nil {
			a.log.Error("bites: an error mapper panicked; the error is answered as unmapped", "panic", p, "err", err)
			se = nil
		}
	}()
	for _, m := range a.errMappers {
		if se = m(err); se != nil {
			return se
		}
	}
	return nil
}

// NewError returns a StatusError for status, which should be 400 or above; any other status is
// answered as a 500 and logged, because it can only be a mistake.
func NewError(status int, message string) *StatusError {
	return &StatusError{Status: status, Message: message}
}

// BadRequest is a 400 for a mistake the Input's types could not catch.
func BadRequest(message string) *StatusError { return NewError(400, message) }

// Unauthorized is a 401.
func Unauthorized(message string) *StatusError { return NewError(401, message) }

// Forbidden is a 403.
func Forbidden(message string) *StatusError { return NewError(403, message) }

// NotFound is a 404.
func NotFound(message string) *StatusError { return NewError(404, message) }

// Conflict is a 409.
func Conflict(message string) *StatusError { return NewError(409, message) }

// PreconditionFailed is a 412: a condition the request sent, such as If-Match with the version
// the client last read, does not hold for the resource now (RFC 9110 section 15.5.13). A handler
// that compares the version itself returns it, so the client reads the resource again instead of
// overwriting a change it has not seen.
func PreconditionFailed(message string) *StatusError { return NewError(412, message) }

// PreconditionRequired is a 428: the route changes a resource only when the request says which
// version it read, and this one sent no If-Match (RFC 6585 section 3). Return it when the header
// is missing and PreconditionFailed when it names an old version; the message says what to send.
func PreconditionRequired(message string) *StatusError { return NewError(428, message) }

// UnprocessableEntity is a 422, for an application that wants it; the framework itself answers
// every input mistake with 400 (ADR 0012).
func UnprocessableEntity(message string) *StatusError { return NewError(422, message) }

// TooManyRequests is a 429.
func TooManyRequests(message string) *StatusError { return NewError(429, message) }

// ServiceUnavailable is a 503.
func ServiceUnavailable(message string) *StatusError { return NewError(503, message) }

// problem writes the answer of status as problem+json: title and status always, a detail when
// there is one (ADR 0012). It is the one place an error answer is built, so 404, 405, 413, 415,
// 500 and a status error all have one shape.
func problem(ex *engine.Exchange, status int, detail string) *engine.Response {
	r := ex.Respond(status)
	r.Header("Content-Type", problemType)
	b := r.Body()
	*b = appendProblemHead(*b, status, detail)
	*b = appendRequestID(*b, ex.RequestID())
	*b = append(*b, '}')
	return r
}

// appendRequestID adds the request id a middleware gave the request, as the last member of an
// error answer (ADR 0012). Nothing is added when the request has none.
func appendRequestID(b []byte, id string) []byte {
	if id == "" {
		return b
	}
	b = append(b, `,"request_id":`...)
	return appendQuoted(b, id)
}

func appendProblemHead(b []byte, status int, detail string) []byte {
	b = append(b, `{"title":`...)
	b = appendQuoted(b, engine.StatusText(status))
	b = append(b, `,"status":`...)
	b = strconv.AppendInt(b, int64(status), 10)
	if detail != "" {
		b = append(b, `,"detail":`...)
		b = appendQuoted(b, detail)
	}
	return b
}

// invalidInput writes the 400 of ADR 0012: every error found, locations in wire names, the list
// cut at maxErrors with "truncated": true.
func invalidInput(ex *engine.Exchange, errs *jsoncodec.Errors) { invalidInputAs(ex, 400, errs) }

// invalidInputAs is invalidInput with another status: the 413 of a field or a file over its bound
// is located like a 400.
func invalidInputAs(ex *engine.Exchange, status int, errs *jsoncodec.Errors) {
	r := ex.Respond(status)
	r.Header("Content-Type", problemType)
	b := r.Body()
	*b = appendProblemHead(*b, status, "")
	*b = append(*b, `,"errors":[`...)
	for i, e := range errs.List {
		if i > 0 {
			*b = append(*b, ',')
		}
		*b = append(*b, `{"location":`...)
		*b = appendQuoted(*b, e.Location)
		*b = append(*b, `,"message":`...)
		*b = appendQuoted(*b, e.Message)
		if e.Rule != "" {
			*b = append(*b, `,"rule":`...)
			*b = appendQuoted(*b, e.Rule)
			if e.Rule == wire.RuleMultiple {
				// The step of a MultipleOf, a JSON number for a float type (ADR 0021).
				*b = append(*b, `,"multiple":`...)
				if e.Float {
					*b = wire.AppendFloat(*b, e.MinF)
				} else {
					*b = strconv.AppendInt(*b, e.Min, 10)
				}
			} else if e.Float {
				// A float range: the same members, as JSON numbers; a bound that is exclusive is
				// named exclusive_min or exclusive_max instead of min or max (ADR 0021).
				minKey, maxKey := `,"min":`, `,"max":`
				if e.ExMin {
					minKey = `,"exclusive_min":`
				}
				if e.ExMax {
					maxKey = `,"exclusive_max":`
				}
				if !math.IsInf(e.MinF, 0) {
					*b = append(*b, minKey...)
					*b = wire.AppendFloat(*b, e.MinF)
				}
				if !math.IsInf(e.MaxF, 0) {
					*b = append(*b, maxKey...)
					*b = wire.AppendFloat(*b, e.MaxF)
				}
			} else if e.Rule == wire.RuleFormat {
				*b = append(*b, `,"format":`...)
				*b = appendQuoted(*b, e.Format)
			} else if e.Rule == wire.RuleLength || e.Rule == wire.RuleRange || e.Rule == wire.RuleItems || e.Rule == wire.RuleMembers {
				// A side the type does not bound is left out (ADR 0021).
				if e.Min != math.MinInt64 {
					*b = append(*b, `,"min":`...)
					*b = strconv.AppendInt(*b, e.Min, 10)
				}
				if e.Max != math.MaxInt64 {
					*b = append(*b, `,"max":`...)
					*b = strconv.AppendInt(*b, e.Max, 10)
				}
			}
		}
		*b = append(*b, '}')
	}
	*b = append(*b, ']')
	if errs.Truncated {
		*b = append(*b, `,"truncated":true`...)
	}
	*b = appendRequestID(*b, ex.RequestID())
	*b = append(*b, '}')
}

// statusAnswer writes a StatusError, with its extra members in name order so the answer is the
// same bytes every time.
func (a *App) statusAnswer(ex *engine.Exchange, se *StatusError) {
	status := se.Status
	if status < 400 || status > 599 {
		a.log.Error("bites: a status error was returned whose status is not an error status",
			"status", se.Status, "method", string(ex.Head().Method), "path", string(ex.Head().Path))
		problem(ex, 500, "")
		return
	}
	r := ex.Respond(status)
	r.Header("Content-Type", problemType)
	for _, h := range se.headers {
		if !isToken(h.name) || engineHeader(h.name) {
			a.log.Error("bites: a status error header was left out: its name is not a token or is the engine's", "header", h.name)
			continue
		}
		r.Header(h.name, h.value)
	}
	b := r.Body()
	*b = appendProblemHead(*b, status, se.Message)
	for _, k := range slices.Sorted(mapKeys(se.members)) {
		switch k {
		case "title", "status", "detail":
			continue
		}
		v, err := jsonv2.Marshal(se.members[k])
		if err != nil {
			a.log.Warn("bites: a status error member could not be encoded", "member", k, "err", err)
			continue
		}
		*b = append(*b, ',')
		*b = appendQuoted(*b, k)
		*b = append(*b, ':')
		*b = append(*b, v...)
	}
	*b = appendRequestID(*b, ex.RequestID())
	*b = append(*b, '}')
}

// engineHeader reports whether name is a header the framework or the engine writes itself.
func engineHeader(name string) bool {
	for _, h := range [...]string{"Content-Type", "Content-Length", "Date", "Connection", "Transfer-Encoding"} {
		if foldEq(name, h) {
			return true
		}
	}
	return false
}

func mapKeys(m map[string]any) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// appendQuoted appends s as a JSON string. A string that is not valid UTF-8 (a client's bytes in
// a location) is made valid first, so the answer is always parseable.
func appendQuoted(b []byte, s string) []byte {
	if q, err := jsontext.AppendQuote(b, s); err == nil {
		return q
	}
	q, _ := jsontext.AppendQuote(b, strings.ToValidUTF8(s, "�"))
	return q
}
