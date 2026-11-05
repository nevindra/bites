package engine

import "strconv"

// statusText is the reason phrase of the statuses the engine and the framework answer with, and
// of every error status IANA registers (RFC 9110, 6585 and the others), so a NewError of any of
// them has its own title. A status with no registered phrase is "Status N".
// It is a table of our own because the core never imports net/http (ADR 0006).
func statusText(code int) string {
	switch code {
	case 100:
		return "Continue"
	case 101:
		return "Switching Protocols"
	case 200:
		return "OK"
	case 201:
		return "Created"
	case 202:
		return "Accepted"
	case 204:
		return "No Content"
	case 206:
		return "Partial Content"
	case 301:
		return "Moved Permanently"
	case 302:
		return "Found"
	case 303:
		return "See Other"
	case 304:
		return "Not Modified"
	case 307:
		return "Temporary Redirect"
	case 308:
		return "Permanent Redirect"
	case 400:
		return "Bad Request"
	case 401:
		return "Unauthorized"
	case 403:
		return "Forbidden"
	case 404:
		return "Not Found"
	case 405:
		return "Method Not Allowed"
	case 408:
		return "Request Timeout"
	case 409:
		return "Conflict"
	case 411:
		return "Length Required"
	case 412:
		return "Precondition Failed"
	case 413:
		return "Content Too Large"
	case 414:
		return "URI Too Long"
	case 415:
		return "Unsupported Media Type"
	case 416:
		return "Range Not Satisfiable"
	case 417:
		return "Expectation Failed"
	case 422:
		return "Unprocessable Content"
	case 429:
		return "Too Many Requests"
	case 431:
		return "Request Header Fields Too Large"
	case 500:
		return "Internal Server Error"
	case 501:
		return "Not Implemented"
	case 502:
		return "Bad Gateway"
	case 503:
		return "Service Unavailable"
	case 504:
		return "Gateway Timeout"
	case 505:
		return "HTTP Version Not Supported"
	case 402:
		return "Payment Required"
	case 406:
		return "Not Acceptable"
	case 407:
		return "Proxy Authentication Required"
	case 410:
		return "Gone"
	case 421:
		return "Misdirected Request"
	case 423:
		return "Locked"
	case 424:
		return "Failed Dependency"
	case 425:
		return "Too Early"
	case 426:
		return "Upgrade Required"
	case 428:
		return "Precondition Required"
	case 451:
		return "Unavailable For Legal Reasons"
	case 506:
		return "Variant Also Negotiates"
	case 507:
		return "Insufficient Storage"
	case 508:
		return "Loop Detected"
	case 511:
		return "Network Authentication Required"
	}
	return "Status " + strconv.Itoa(code)
}

// StatusText is the reason phrase for code, which the framework also uses as the "title" of a
// problem+json answer (ADR 0012), so the two can never name a status differently.
func StatusText(code int) string { return statusText(code) }

func appendStatusLine(b []byte, code int) []byte {
	b = append(b, "HTTP/1.1 "...)
	b = strconv.AppendInt(b, int64(code), 10)
	b = append(b, ' ')
	b = append(b, statusText(code)...)
	return append(b, '\r', '\n')
}

// appendProblem appends the small problem+json body of ADR 0012 for an answer the engine
// itself gives (a refused head, a panic, a missing response).
// A request id a middleware gave is added as request_id (ADR 0012).
func appendProblem(b []byte, code int, id string) []byte {
	b = append(b, `{"title":"`...)
	b = append(b, statusText(code)...)
	b = append(b, `","status":`...)
	b = strconv.AppendInt(b, int64(code), 10)
	if id != "" {
		b = append(b, `,"request_id":"`...)
		for i := 0; i < len(id); i++ {
			switch c := id[i]; {
			case c == '"' || c == '\\':
				b = append(b, '\\', c)
			case c < 0x20 || c >= 0x7f:
				b = append(b, '?')
			default:
				b = append(b, c)
			}
		}
		b = append(b, '"')
	}
	return append(b, '}')
}
