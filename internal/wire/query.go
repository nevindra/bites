package wire

import "bytes"

// EachQuery calls fn with each key and value of a raw query string, still percent-encoded
// (ADR 0042: decoding happens once, into the slab, by the caller). Pairs split on '&', and
// the key ends at the first '='; a key with no '=' has an empty value, and an empty pair
// ("a=1&&b=2", a trailing '&') is skipped. A pair with an empty key ("=x") is yielded as is.
// fn returns false to stop. The slices alias raw and nothing is allocated.
func EachQuery(raw []byte, fn func(key, value []byte) bool) {
	for len(raw) > 0 {
		var pair []byte
		if i := bytes.IndexByte(raw, '&'); i >= 0 {
			pair, raw = raw[:i], raw[i+1:]
		} else {
			pair, raw = raw, nil
		}
		if len(pair) == 0 {
			continue
		}
		key, value := pair, []byte(nil)
		if i := bytes.IndexByte(pair, '='); i >= 0 {
			key, value = pair[:i], pair[i+1:]
		}
		if !fn(key, value) {
			return
		}
	}
}
