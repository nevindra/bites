package bites

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/nevindra/bites/internal/clock"
)

// Sessions (ADR 0056): a value of the application's type T kept for a browser across requests,
// sealed with AES-256-GCM in a cookie, or kept in a SessionStore with only a sealed id in the
// cookie. A handler reads it as an Input field filled by a Provider and changes it by returning
// the cookie Save gives it, so the response still comes only from the return value (ADR 0009).

// SessionKeyLen is the length of a session key: 32 bytes, for AES-256-GCM.
const SessionKeyLen = 32

// maxCookieLen is what a browser keeps of one cookie, name, value and attributes together.
const maxCookieLen = 4096

// SessionOptions configures NewSessions.
type SessionOptions struct {
	// Keys are the keys that seal and open session cookies, each SessionKeyLen random bytes
	// (crypto/rand). The first seals; every one opens, so a key is rotated by putting the new one
	// first and dropping the old one once the longest session sealed with it has expired.
	// Required. Keep them out of the source, as any secret.
	Keys [][]byte

	// Cookie is the cookie's name: "__Host-session" when empty, a prefix that makes the browser
	// refuse the cookie from anywhere but this host over HTTPS, so a sibling subdomain cannot plant
	// one; "session" when Domain, a Path other than "/" or NotSecure rule the prefix out.
	Cookie string

	// MaxAge is how long a session lives after its last Save; 24 hours when zero. Save seals its
	// own time in the cookie and every request checks it against the MaxAge in force, besides
	// MaxAge being the cookie's Max-Age, so a browser that keeps the cookie longer does not keep
	// the session, and a MaxAge shortened in a deploy applies to the cookies already out.
	MaxAge time.Duration

	// Path and Domain are the cookie's; Path is "/" and Domain is the host alone when empty.
	Path, Domain string

	// SameSite is the cookie's; Lax when zero (ADR 0043).
	SameSite SameSite

	// NotSecure drops the cookie's Secure attribute. Browsers treat localhost as secure, so
	// development over plain HTTP does not need it.
	NotSecure bool

	// Store keeps sessions on the server; the cookie then holds a sealed random id. When nil the
	// value itself is sealed in the cookie, which needs no storage and cannot be revoked before it
	// expires.
	Store SessionStore

	// RefreshAfter makes a session slide: a request whose session was saved more than RefreshAfter
	// ago gets it saved again by the framework, as if the handler had called Save with the value it
	// loaded, so a session in use lives MaxAge from its last use and one left alone still expires.
	// Sessions.Load saves it (in the Store, with the new expiry) and the new cookie is added to the
	// answer when the answer's status is under 400 and the handler set no cookie of the session's
	// name itself, so a Save, Renew or Clear of the handler wins. Zero means never: a session lives
	// MaxAge from the handler's last Save. It must be under MaxAge; MaxAge/2 saves a session at
	// most twice a MaxAge. A refresh that fails (the Store's error) is logged at Warn and the
	// request goes on with the session it loaded.
	RefreshAfter time.Duration
}

// SessionStore keeps sessions on the server (Redis, Postgres), for sessions too large for a
// cookie or that must be revoked at once. An id is 22 characters of base64url over 16 random
// bytes, made by the framework.
type SessionStore interface {
	// Load returns the data saved for id, and false when there is none or it has expired. Whether
	// it has expired is judged on bites.Now(ctx), the clock that computed expires, and not on the
	// store's own or a database's.
	Load(ctx context.Context, id string) (data []byte, found bool, err error)
	// Save keeps data for id until expires, replacing what id had. It is not called for a session
	// type that has a SessionOwner method: such a type needs an OwnerSessionStore, and every save
	// goes through SaveOwned. A store written only for owned types may return an error from Save.
	Save(ctx context.Context, id string, data []byte, expires time.Time) error
	// Delete forgets id. Deleting an id that has nothing is not an error.
	Delete(ctx context.Context, id string) error
}

// OwnerSessionStore is a SessionStore that can find every session of one owner, the User a
// session belongs to (ADR 0056). A session type T that has a SessionOwner method needs one: the
// owner is then given to SaveOwned on every Save and Renew, and Sessions.RevokeOwner calls
// DeleteOwner. The store keeps its own index from an owner to its ids; how it does that (a
// column, a Redis set) is its own. A store that is an OwnerSessionStore is not told an owner for a
// T that has no SessionOwner: Save is called as before. For a T that has one, Save is never called.
// The store is told no tenant or other column: what it needs beyond the owner it reads from its
// own rows of that owner.
type OwnerSessionStore interface {
	SessionStore
	// SaveOwned is Save that also records owner for id, replacing the owner id had. An empty owner
	// is a session no owner can reach: it is kept, and DeleteOwner never finds it.
	SaveOwned(ctx context.Context, id, owner string, data []byte, expires time.Time) error
	// DeleteOwner forgets every session of owner but the one whose id is keep, and returns how
	// many it forgot. An empty keep keeps none (Sessions.RevokeOwner); a keep that is not a
	// session of owner keeps nothing of owner's either (Session.RevokeOthers). An owner with no
	// session is (0, nil), not an error.
	DeleteOwner(ctx context.Context, owner, keep string) (int, error)
}

// Sessions loads and saves the Session of type T (ADR 0056). Make one with NewSessions and give
// its Load to Provide:
//
//	carts := bites.NewSessions[Cart](bites.SessionOptions{Keys: keys})
//	app.Provide(carts.Load)
type Sessions[T any] struct {
	aeads  []cipher.AEAD
	name   string
	maxAge time.Duration
	cookie Cookie // the attributes every cookie it sets has
	aad    []byte // the cookie's name, the additional data of every seal
	attrs  int    // the length of the Set-Cookie value of an empty session cookie
	store  SessionStore
	ostore OwnerSessionStore // store, when T states an owner; nil otherwise
	owner  func(T) string    // T's SessionOwner, nil when T states none
	slide  time.Duration     // SessionOptions.RefreshAfter; zero for none
}

// Session is the session of the request (ADR 0056): the value T the application keeps for a
// browser, and the way to change it. It is a plain value, filled by the Provider Sessions.Load.
// Changing Value changes nothing by itself: Save, Renew and Clear return the Cookie the handler
// puts in its answer's Cookies, as any cookie (ADR 0043).
type Session[T any] struct {
	// Value is the session's value, or the zero T for a new session.
	Value T

	s     *Sessions[T]
	clk   Clock
	id    string // the store's id, when the session came from a store
	found bool
}

// sealed plaintext: a mode byte, the issue time in Unix seconds, then the JSON of T or the store id.
const (
	modeCookie byte = 'c'
	modeStore  byte = 's'
	sealHead        = 1 + 8
	nonceLen        = 12
	storeIDLen      = 16
)

// NewSessions returns the Sessions of type T. It panics when there is no key, a key is not
// SessionKeyLen bytes, is all zeros or is given twice, the cookie name is not a token or its
// prefix cannot hold with the other options, MaxAge is negative or not whole seconds, SameSite is
// None without Secure, T cannot be encoded as JSON, or T states an owner (a SessionOwner method)
// and Store is missing, is not an OwnerSessionStore, or the method is not func() string on T.
func NewSessions[T any](o SessionOptions) *Sessions[T] {
	const fn = "NewSessions"
	if len(o.Keys) == 0 {
		refuseMiddleware(fn, "Keys is empty: give at least one key of %d random bytes (crypto/rand), kept outside the source", SessionKeyLen)
	}
	s := &Sessions[T]{store: o.Store}
	for i, k := range o.Keys {
		if len(k) != SessionKeyLen {
			refuseMiddleware(fn, "Keys[%d] is %d bytes; a session key is %d random bytes, for AES-256-GCM", i, len(k), SessionKeyLen)
		}
		if subtle.ConstantTimeCompare(k, make([]byte, SessionKeyLen)) == 1 {
			refuseMiddleware(fn, "Keys[%d] is all zeros, a placeholder and not a key: generate %d random bytes with crypto/rand", i, SessionKeyLen)
		}
		for j := range i {
			if subtle.ConstantTimeCompare(k, o.Keys[j]) == 1 {
				refuseMiddleware(fn, "Keys[%d] is the same key as Keys[%d]", i, j)
			}
		}
		b, _ := aes.NewCipher(k)
		g, _ := cipher.NewGCM(b)
		s.aeads = append(s.aeads, g)
	}
	path := o.Path
	if path == "" {
		path = "/"
	}
	name := o.Cookie
	if name == "" {
		name = "__Host-session"
		if o.Domain != "" || path != "/" || o.NotSecure {
			name = "session"
		}
	}
	if !validCookieName(name) {
		refuseMiddleware(fn, "Cookie %q is not a cookie name: use an HTTP token", name)
	}
	if strings.HasPrefix(name, "__Host-") && (o.Domain != "" || path != "/" || o.NotSecure) {
		refuseMiddleware(fn, "Cookie %q has the __Host- prefix, which browsers accept only with Secure, Path=/ and no Domain: drop the prefix or those options", name)
	}
	if strings.HasPrefix(name, "__Secure-") && o.NotSecure {
		refuseMiddleware(fn, "Cookie %q has the __Secure- prefix, which browsers accept only with Secure: drop the prefix or NotSecure", name)
	}
	if o.SameSite == NoSameSite && o.NotSecure {
		refuseMiddleware(fn, "SameSite is None without Secure, which browsers reject: drop NotSecure")
	}
	if strings.ContainsAny(path, ";\r\n") || strings.ContainsAny(o.Domain, ";\r\n") {
		refuseMiddleware(fn, "Path or Domain has a character a cookie attribute cannot carry")
	}
	if o.MaxAge < 0 || o.MaxAge%time.Second != 0 {
		refuseMiddleware(fn, "MaxAge is %v, which is not a whole number of seconds: give zero for 24 hours, or whole seconds", o.MaxAge)
	}
	s.maxAge = o.MaxAge
	if s.maxAge == 0 {
		s.maxAge = 24 * time.Hour
	}
	if o.RefreshAfter < 0 || o.RefreshAfter >= s.maxAge {
		refuseMiddleware(fn, "RefreshAfter is %v, but it must be under MaxAge (%v) and not negative, or a session would expire before it is refreshed: give MaxAge/2, or zero for a session that does not slide", o.RefreshAfter, s.maxAge)
	}
	s.slide = o.RefreshAfter
	var zero T
	if _, err := jsonv2.Marshal(zero); err != nil {
		refuseMiddleware(fn, "the session's type %s cannot be encoded as JSON, so it cannot be kept in a cookie or a store: give a type of plain fields", typeLabel(reflect.TypeFor[T]()))
	}
	if owner, ok := ownerMethod[T](fn); ok {
		if o.Store == nil {
			refuseMiddleware(fn, "%s states an owner (a SessionOwner method) but SessionOptions has no Store, and a session sealed in a cookie cannot be revoked: give a Store that implements OwnerSessionStore, or drop the SessionOwner method", typeLabel(reflect.TypeFor[T]()))
		}
		os, ok := o.Store.(OwnerSessionStore)
		if !ok {
			refuseMiddleware(fn, "%s states an owner (a SessionOwner method) but the Store %T does not implement OwnerSessionStore (SaveOwned and DeleteOwner), so it cannot find the sessions of one owner: add those two methods to the store, or drop the SessionOwner method", typeLabel(reflect.TypeFor[T]()), o.Store)
		}
		s.owner, s.ostore = owner, os
	}
	s.name = name
	s.cookie = Cookie{Name: name, Path: path, Domain: o.Domain, SameSite: o.SameSite, NotSecure: o.NotSecure}
	s.aad = []byte(name)
	empty := s.cookie
	empty.MaxAge = s.maxAge
	b, _ := appendSetCookie(nil, &empty)
	s.attrs = len(b)
	return s
}

// sessionOwnerName is the method a session type states its owner with.
const sessionOwnerName = "SessionOwner"

// ownerMethod returns T's SessionOwner as a function, and whether T has one. It refuses a method
// that is on *T only or whose signature is not func() string, because either would be found by
// nobody and the revocation would silently reach no session (ADR 0056).
func ownerMethod[T any](fn string) (func(T) string, bool) {
	t := reflect.TypeFor[T]()
	if t.Kind() == reflect.Interface {
		return nil, false
	}
	m, ok := t.MethodByName(sessionOwnerName)
	if !ok {
		if _, onPtr := reflect.PointerTo(t).MethodByName(sessionOwnerName); onPtr {
			refuseMiddleware(fn, "SessionOwner is declared on *%[1]s but a Session holds a %[1]s, so it would never be found: declare it on the value receiver, func (%[1]s) SessionOwner() string", typeLabel(t))
		}
		return nil, false
	}
	f, ok := m.Func.Interface().(func(T) string)
	if !ok {
		refuseMiddleware(fn, "%s has a SessionOwner method of type %s; it must be func() string, returning the owner of the session", typeLabel(t), m.Type.String())
	}
	return f, true
}

// RevokeOwner ends every session of owner at once and returns how many it ended: "sign out
// everywhere", and a User who has been disabled. It needs a T with a SessionOwner method and a
// Store that is an OwnerSessionStore, which NewSessions checked; it returns an error for a T
// without SessionOwner, and for an empty owner, which names no one. A browser that still holds
// the cookie of an ended session is a new session on its next request. A request that loaded the
// session before this call and saves it after puts it back, so the Provider of the User must
// refuse a disabled User on every request (ADR 0056).
func (s *Sessions[T]) RevokeOwner(ctx context.Context, owner string) (int, error) {
	if s.ostore == nil {
		return 0, fmt.Errorf("bites: RevokeOwner on the Sessions of %s, which has no SessionOwner method: add func (%[1]s) SessionOwner() string and give a Store that implements OwnerSessionStore", typeLabel(reflect.TypeFor[T]()))
	}
	if owner == "" {
		return 0, errors.New("bites: RevokeOwner was given an empty owner, which names no one: pass the id of the User whose sessions end")
	}
	return s.ostore.DeleteOwner(ctx, owner, "")
}

// Load is the Provider of Session[T]. It opens the request's session cookie and answers its
// value; a request with no cookie, one that does not open with any key (forged, sealed with a
// key since dropped, cut short) or one past its MaxAge is a new session, IsNew true and Value
// zero, not an error. Requiring a session is the application's own Provider, of a User from a
// Session, answering 401. A store's error is the answer (a 500).
func (s *Sessions[T]) Load(ctx context.Context, req Request, _ struct{}) (Session[T], error) {
	out := Session[T]{s: s, clk: req.clock()}
	now := out.clk.Now()
	cookies := req.headers
	for cookies != "" {
		var line string
		var ok bool
		if line, cookies, ok = cutLine(cookies); !ok {
			break
		}
		for line != "" {
			var pair string
			pair, line, _ = strings.Cut(line, ";")
			name, value, ok := strings.Cut(strings.TrimSpace(pair), "=")
			if !ok || name != s.name {
				continue
			}
			plain, ok := s.open(value, now)
			if !ok {
				continue
			}
			found, err := s.fill(ctx, &out, plain)
			if err != nil {
				return Session[T]{s: s, clk: out.clk}, err
			}
			if found {
				if s.slide > 0 && now.Sub(sealedAt(plain)) >= s.slide {
					s.refresh(ctx, req, out)
				}
				return out, nil
			}
		}
	}
	return out, nil
}

// sealedAt is the time a session's cookie was sealed, by its last Save.
func sealedAt(plain []byte) time.Time {
	return time.Unix(int64(binary.BigEndian.Uint64(plain[1:sealHead])), 0)
}

// refresh saves out again under its id and asks for the new cookie to be added to the answer of
// req (SessionOptions.RefreshAfter). A failure is logged and the request goes on.
func (s *Sessions[T]) refresh(ctx context.Context, req Request, out Session[T]) {
	if !req.live() {
		return
	}
	log := req.run.a.log
	c, err := out.save(ctx, out.Value, false)
	if err == nil {
		var b []byte
		if b, err = appendSetCookie(nil, &c); err == nil {
			req.run.ex.SetLateCookie(s.name, string(b))
			return
		}
	}
	log.Warn("bites: a session was not refreshed; it keeps the expiry of its last Save", "cookie", s.name, "err", err)
}

// fill sets out from an opened cookie, and says whether it holds a session.
func (s *Sessions[T]) fill(ctx context.Context, out *Session[T], plain []byte) (bool, error) {
	mode, body := plain[0], plain[sealHead:]
	switch {
	case mode == modeCookie && s.store == nil:
		var v T
		if jsonv2.Unmarshal(body, &v) != nil {
			return false, nil // T changed since it was sealed: a new session
		}
		out.Value, out.found = v, true
		return true, nil
	case mode == modeStore && s.store != nil && len(body) == storeIDLen:
		id := base64.RawURLEncoding.EncodeToString(body)
		data, found, err := s.store.Load(ctx, id)
		if err != nil || !found {
			return false, err
		}
		var v T
		if jsonv2.Unmarshal(data, &v) != nil {
			return false, nil
		}
		out.Value, out.id, out.found = v, id, true
		return true, nil
	}
	return false, nil
}

// IsNew reports whether the request came with no session: no cookie, one that did not open, or
// one that had expired.
func (s Session[T]) IsNew() bool { return !s.found }

// Save makes v the session's value and returns the Cookie that carries it, to put in the answer's
// Cookies. The session lives for MaxAge from now. With a Store, v is saved there under the
// session's id, or a new id for a new session, and the cookie carries the sealed id. It is an
// error when the cookie would be over 4096 bytes, which a browser drops (keep less in the session,
// or give a Store), and when the Store fails.
func (s Session[T]) Save(ctx context.Context, v T) (Cookie, error) {
	return s.save(ctx, v, false)
}

// Renew is Save under a new id: the old id is deleted from the Store and the cookie carries a new
// one. Call it when the session's privilege changes, at login above all, so an id that someone
// planted or saw before cannot follow the session up (session fixation). Without a Store, every
// Save already seals afresh, and Renew is Save.
func (s Session[T]) Renew(ctx context.Context, v T) (Cookie, error) {
	return s.save(ctx, v, true)
}

// Clear ends the session: it deletes it from the Store, and returns the Cookie that deletes it
// from the browser. Without a Store a copy of the old cookie taken before Clear still opens until
// it expires; a session that must be revocable needs a Store.
func (s Session[T]) Clear(ctx context.Context) (Cookie, error) {
	if s.s == nil {
		return Cookie{}, errors.New("bites: Clear on a Session that no Sessions.Load filled")
	}
	if s.s.store != nil && s.id != "" {
		if err := s.s.store.Delete(ctx, s.id); err != nil {
			return Cookie{}, err
		}
	}
	c := s.s.cookie
	c.Value = "deleted"
	c.MaxAge = -1
	return c, nil
}

// RevokeOthers ends every other session of this session's owner and keeps this one, and returns
// how many it ended: "sign out everywhere else", and a password change. The browser that sent the
// request stays signed in with no new cookie, so the handler returns none. It needs what
// Sessions.RevokeOwner needs (a T with a SessionOwner method and an OwnerSessionStore), and a
// session that is in the store: it returns an error for a new session (Save it first), for an
// owner that is empty, and for a session whose id this request already replaced with Renew or
// deleted with Clear, so it ends nothing it should keep. To renew the id as well, call
// RevokeOthers first and Renew after it.
func (s Session[T]) RevokeOthers(ctx context.Context) (int, error) {
	ss := s.s
	if ss == nil {
		return 0, errors.New("bites: RevokeOthers on a Session that no Sessions.Load filled")
	}
	if ss.ostore == nil {
		return 0, fmt.Errorf("bites: RevokeOthers on a Session of %s, which has no SessionOwner method: add func (%[1]s) SessionOwner() string and give a Store that implements OwnerSessionStore", typeLabel(reflect.TypeFor[T]()))
	}
	if s.id == "" {
		return 0, errors.New("bites: RevokeOthers on a new session, which is not in the store, so it has nothing to keep: Save the session first, or end every session of the owner with Sessions.RevokeOwner")
	}
	owner := ss.owner(s.Value)
	if owner == "" {
		return 0, errors.New("bites: RevokeOthers on a session whose SessionOwner is empty, which names no one: it ends the other sessions of the User the session belongs to")
	}
	if _, found, err := ss.store.Load(ctx, s.id); err != nil {
		return 0, err
	} else if !found {
		return 0, errors.New("bites: RevokeOthers on a session that is no longer in the store: Renew or Clear replaced it in this request, or it ended elsewhere; call RevokeOthers before Renew, so the session it keeps is the one the browser holds")
	}
	return ss.ostore.DeleteOwner(ctx, owner, s.id)
}

func (s Session[T]) save(ctx context.Context, v T, renew bool) (Cookie, error) {
	ss := s.s
	if ss == nil {
		return Cookie{}, errors.New("bites: Save on a Session that no Sessions.Load filled")
	}
	data, err := jsonv2.Marshal(v)
	if err != nil {
		return Cookie{}, fmt.Errorf("bites: the session value cannot be encoded: %w", err)
	}
	now := s.clk.Now()
	expires := now.Add(ss.maxAge)
	var plain []byte
	if ss.store == nil {
		plain = sealPlain(modeCookie, now, data)
	} else {
		id := s.id
		if id == "" || renew {
			if renew && id != "" {
				if err := ss.store.Delete(ctx, id); err != nil {
					return Cookie{}, err
				}
			}
			raw := make([]byte, storeIDLen)
			rand.Read(raw)
			id = base64.RawURLEncoding.EncodeToString(raw)
		}
		raw, _ := base64.RawURLEncoding.DecodeString(id)
		if ss.owner != nil {
			err = ss.ostore.SaveOwned(ctx, id, ss.owner(v), data, expires)
		} else {
			err = ss.store.Save(ctx, id, data, expires)
		}
		if err != nil {
			return Cookie{}, err
		}
		plain = sealPlain(modeStore, now, raw)
	}
	c := ss.cookie
	c.Value = ss.seal(plain)
	c.MaxAge = ss.maxAge
	if n := ss.attrs + len(c.Value); n > maxCookieLen {
		return Cookie{}, fmt.Errorf("bites: the session cookie %s would be %d bytes, over the %d a browser keeps: keep less in the session, or give SessionOptions a Store", ss.name, n, maxCookieLen)
	}
	return c, nil
}

func sealPlain(mode byte, issued time.Time, body []byte) []byte {
	p := make([]byte, sealHead, sealHead+len(body))
	p[0] = mode
	binary.BigEndian.PutUint64(p[1:], uint64(issued.Unix()))
	return append(p, body...)
}

// seal encrypts plain with the first key, a fresh random nonce and the cookie's name as the
// additional data, so a value sealed for another cookie never opens as this one.
func (s *Sessions[T]) seal(plain []byte) string {
	g := s.aeads[0]
	out := make([]byte, nonceLen, nonceLen+len(plain)+g.Overhead())
	rand.Read(out)
	out = g.Seal(out, out[:nonceLen], plain, s.aad)
	return base64.RawURLEncoding.EncodeToString(out)
}

// open decodes and opens a cookie value with each key in turn and checks its expiry. A value
// longer than a cookie can be is not looked at.
func (s *Sessions[T]) open(value string, now time.Time) ([]byte, bool) {
	if len(value) > maxCookieLen {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) < nonceLen+sealHead {
		return nil, false
	}
	for i, g := range s.aeads {
		// The last key opens in place; an earlier one into a buffer of its own, because a failed
		// Open may overwrite its destination and the next key needs the ciphertext whole.
		var dst []byte
		if i == len(s.aeads)-1 {
			dst = raw[nonceLen:nonceLen]
		}
		plain, err := g.Open(dst, raw[:nonceLen], raw[nonceLen:], s.aad)
		if err != nil {
			continue
		}
		// The issue time is sealed, and the MaxAge in force now is added to it, so a MaxAge
		// shortened in a deploy applies to the cookies already out.
		if len(plain) < sealHead || int64(binary.BigEndian.Uint64(plain[1:]))+int64(s.maxAge/time.Second) <= now.Unix() {
			return nil, false
		}
		return plain, true
	}
	return nil, false
}

// clock is the app's clock, or the real one for a Request that is no longer live.
func (r Request) clock() Clock {
	if r.live() {
		return r.run.a.clk
	}
	return clock.Real()
}

// cutLine returns the value of the next Cookie line of a block of header lines, the rest of the
// block, and whether there was one. Lines of other headers are skipped.
func cutLine(b string) (string, string, bool) {
	for b != "" {
		i := strings.IndexByte(b, '\n')
		var line string
		if i < 0 {
			line, b = b, ""
		} else {
			line, b = b[:i], b[i+1:]
		}
		if v, ok := headerLineValue(line, "Cookie"); ok {
			return v, b, true
		}
	}
	return "", "", false
}
