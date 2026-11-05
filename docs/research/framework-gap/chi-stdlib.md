# Feature inventory: chi ecosystem and Go net/http (server side)

Sources read (all fetched in this session): C=https://github.com/go-chi/chi/blob/master/README.md, CP=https://pkg.go.dev/github.com/go-chi/chi/v5, CM=https://pkg.go.dev/github.com/go-chi/chi/v5/middleware, CL=https://github.com/go-chi/chi/blob/master/CHANGELOG.md. pkg.go.dev for net/http showed Go 1.27.3 docs, so a few items may postdate 1.25 (flagged "(doc shows, release note not read)" where the 1.22-1.25 notes do not list them).

# 1. chi ecosystem

## 1.1 chi core: characteristics
- Size and structure: about 1000 LOC router built on a Patricia radix trie, no external dependencies (stdlib only), Go modules since v5 [https://github.com/go-chi/chi/blob/master/README.md]
- net/http compatibility: handlers are http.Handler / http.HandlerFunc, middleware is func(http.Handler) http.Handler [https://github.com/go-chi/chi/blob/master/README.md]
- Context control: built on stdlib context, values via context.WithValue and r.WithContext [https://github.com/go-chi/chi/blob/master/README.md]
- Subpackages: middleware, render, docgen [https://github.com/go-chi/chi/blob/master/README.md]
- Zero-allocation routing via sync.Pool reuse of context objects (since v0.9.0) [https://github.com/go-chi/chi/blob/master/CHANGELOG.md]
- Benchmarks: go-http-routing-benchmark suite, e.g. Chi_Param about 384 ns/op 2 allocs; allocations come from http.Request.WithContext cloning [https://github.com/go-chi/chi/blob/master/README.md]

## 1.2 chi core: pattern syntax
- Static path: /articles/search [https://github.com/go-chi/chi/blob/master/README.md]
- Named parameter: /users/{userID} [https://github.com/go-chi/chi/blob/master/README.md]
- Multiple params in one segment: /{month}-{day}-{year} [https://github.com/go-chi/chi/blob/master/README.md]
- Regexp param: {name:regex}, e.g. /{articleSlug:[a-z-]+} [https://github.com/go-chi/chi/blob/master/README.md]
- Wildcard: /admin/* [https://github.com/go-chi/chi/blob/master/README.md]
- chi.URLParam(r, "name") and chi.URLParam(r, "*") for wildcard value [https://github.com/go-chi/chi/blob/master/README.md]
- Trailing-slash routing on mounted sub-routers (v3.3.2) [https://github.com/go-chi/chi/blob/master/CHANGELOG.md]
- URL param syntax {id} and regexp patterns introduced in v3.0.0 [https://github.com/go-chi/chi/blob/master/CHANGELOG.md]

## 1.3 chi core: Router API (Router / Mux)
- NewRouter() / NewMux(): create a Mux (Router interface) [https://pkg.go.dev/github.com/go-chi/chi/v5]
- Use(middlewares...): append middleware to the mux stack [https://pkg.go.dev/github.com/go-chi/chi/v5]
- With(middlewares...): inline middleware applied to the next endpoint only [https://pkg.go.dev/github.com/go-chi/chi/v5]
- Group(fn): inline router with a copy of the current middleware stack [https://pkg.go.dev/github.com/go-chi/chi/v5]
- Route(pattern, fn): create sub-router mounted at pattern [https://pkg.go.dev/github.com/go-chi/chi/v5]
- Mount(pattern, handler): attach any http.Handler / router as sub-router [https://pkg.go.dev/github.com/go-chi/chi/v5]
- Handle / HandleFunc: any method [https://pkg.go.dev/github.com/go-chi/chi/v5]
- Method / MethodFunc: specific method, including custom ones [https://pkg.go.dev/github.com/go-chi/chi/v5]
- Connect, Delete, Get, Head, Options, Patch, Post, Put, Query, Trace: per-method helpers (Query = HTTP QUERY method) [https://pkg.go.dev/github.com/go-chi/chi/v5]
- NotFound(h) / NotFoundHandler(): custom and default 404 [https://pkg.go.dev/github.com/go-chi/chi/v5]
- MethodNotAllowed(h) / MethodNotAllowedHandler(methodsAllowed...): custom and default 405 [https://pkg.go.dev/github.com/go-chi/chi/v5]
- Middlewares(): the mux's middleware stack [https://pkg.go.dev/github.com/go-chi/chi/v5]
- Routes(): routing tree as traversable structure [https://pkg.go.dev/github.com/go-chi/chi/v5]
- Match(rctx, method, path) bool: check for a matching route without running it [https://pkg.go.dev/github.com/go-chi/chi/v5]
- Find(rctx, method, path) string: return the route pattern matching method and path [https://pkg.go.dev/github.com/go-chi/chi/v5]
- ServeHTTP: Mux is an http.Handler [https://pkg.go.dev/github.com/go-chi/chi/v5]
- RegisterMethod(method): add a custom HTTP method usable with Method/MethodFunc (since v3.3.0; LINK/UNLINK in v3.0.0) [https://pkg.go.dev/github.com/go-chi/chi/v5]
- Walk(r, walkFn): walk any Routes implementation, calling WalkFunc per method and route [https://pkg.go.dev/github.com/go-chi/chi/v5]
- Types Route (Pattern, Handlers by method, SubRoutes), Routes, WalkFunc, Middlewares (Chain, Handler, HandlerFunc), ChainHandler (Endpoint, Middlewares) [https://pkg.go.dev/github.com/go-chi/chi/v5]

## 1.4 chi core: routing Context
- RouteCtxKey: context key under which chi's Context is stored [https://pkg.go.dev/github.com/go-chi/chi/v5]
- RouteContext(ctx) / NewRouteContext(): get / make a *chi.Context [https://pkg.go.dev/github.com/go-chi/chi/v5]
- URLParam(r, key) / URLParamFromCtx(ctx, key): read path parameter [https://pkg.go.dev/github.com/go-chi/chi/v5]
- Context fields: Routes, RoutePath (path override), RouteMethod (method override), URLParams (stack), RoutePatterns (stack) [https://pkg.go.dev/github.com/go-chi/chi/v5]
- Context.RoutePattern(): full matched pattern string at the current point [https://pkg.go.dev/github.com/go-chi/chi/v5]
- Context.URLParam(key), Context.Reset(), Context.Clone() (must be called before handler returns to use the context beyond the request) [https://pkg.go.dev/github.com/go-chi/chi/v5]
- RouteParams (Keys, Values, Add) [https://pkg.go.dev/github.com/go-chi/chi/v5]

## 1.5 chi/v5/middleware package (every exported item on the page)
### Request identity and logging
- RequestID: injects a request ID into the context; reads/writes header per RequestIDHeader [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- RequestIDHeader (var, default "X-Request-Id"), RequestIDKey (const ctx key), GetReqID(ctx), NextRequestID() uint64 (atomic sequence) [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- Logger: logs request start/end, status, bytes, elapsed (colored on TTY) [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- DefaultLogger (var, reassignable), IsTTY (var, controls colors) [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- RequestLogger(f LogFormatter): Logger with custom formatter [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- LogFormatter (NewLogEntry), LogEntry (Write(status, bytes, header, elapsed, extra), Panic(v, stack)), LoggerInterface (Print), DefaultLogFormatter (Logger, NoColor) [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- LogEntryCtxKey, GetLogEntry(r), WithLogEntry(r, entry): per-request log entry in context [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- LogEntry Write receives response header (v4.1.0) [https://github.com/go-chi/chi/blob/master/CHANGELOG.md]
- WrapResponseWriter / NewWrapResponseWriter(w, protoMajor): interface exposing Status, BytesWritten, Tee(io.Writer), Unwrap, Discard [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
### Client IP
- ClientIPFromRemoteAddr: client IP from TCP RemoteAddr, for no-proxy deployments (v5.3.0) [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- ClientIPFromHeader(trustedHeader, trustedIPPrefixes...): single-IP header such as CF-Connecting-IP, optional trusted CIDRs [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- ClientIPFromXFF(trustedIPPrefixes...): walks X-Forwarded-For right to left skipping trusted CIDRs, at least one prefix required [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- ClientIPFromXFFTrustedProxies(n): fixed trusted proxy hop count, n >= 1 [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- GetClientIP(ctx) string, GetClientIPAddr(ctx) netip.Addr; IPv4-mapped IPv6 normalized to IPv4, zones removed [https://github.com/go-chi/chi/blob/master/README.md]
- RealIP: deprecated; overwrites RemoteAddr from True-Client-IP / X-Real-IP / X-Forwarded-For, spoofable [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
### Safety, limits, timeouts
- Recoverer: recovers panics, logs, pretty stack trace, returns 500 [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- PrintPrettyStack(rvr): print a formatted stack for a recovered value [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- Timeout(d): cancels request context after d, responds 504 [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- Throttle(limit): cap in-flight requests [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- ThrottleBacklog(limit, backlogLimit, backlogTimeout): cap plus queued backlog with timeout [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- ThrottleWithOpts(ThrottleOpts): options RetryAfterFn(ctxDone) (enables Retry-After), Limit, BacklogLimit, BacklogTimeout, StatusCode [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- RequestSize(bytes): limits body via MaxBytesReader (v5.0.9) [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- BasicAuth(realm, creds map): HTTP basic auth [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
### Content negotiation, headers, compression
- AllowContentType(types...): whitelist request Content-Type, 415 otherwise [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- AllowContentEncoding(encodings...): whitelist request Content-Encoding, 415 otherwise [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- ContentCharset(charsets...): 415 unless request charset allowed [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- Compress(level, types...): response compression by Accept-Encoding for listed content types [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- Compressor / NewCompressor(level, types...) (rejects catch-all */*), Handler(next), SetEncoder(encoding, EncoderFunc) to add algorithms such as br; EncoderFunc func(w io.Writer, level int) io.Writer [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- SetHeader(key, value): set a response header on every response [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- NoCache: anti-caching response headers [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- WithValue(key, val): set request context value [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- Deprecation(deprecatedAt, links...): RFC 9745 Deprecation header, panics on zero time (v5.5.0) [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- Sunset(sunsetAt, links...): RFC 8594 Sunset header, panics on zero time (v5.0.11) [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
### Routing helpers
- CleanPath: collapse double slashes (v1.5.1) [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- StripSlashes: strip trailing slash and keep routing [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- RedirectSlashes: redirect trailing-slash path to non-slash path [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- StripPrefix(prefix): remove prefix from path before routing (v5.2.0) [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- PathRewrite(old, new): rewrite request URL path [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- URLFormat: strips extension (.json, .xml) from path and stores it in context; URLFormatCtxKey [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- GetHead: route HEAD to GET handler when no HEAD handler [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- RouteHeaders: route/apply middleware by request header value; HeaderRouter with Route(header, match), RouteAny(header, matches), RouteDefault(handler), Handler(next); HeaderRoute (Middleware, MatchOne, MatchAny, IsMatch); Pattern/NewPattern(value).Match with wildcard (v4.1.1) [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- SupressNotFound(router) (sic): return 404 immediately for unmatched routes (v5.0.11) [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- Maybe(mw, maybeFn): apply middleware only when predicate on request is true [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- New(h): wrap an http.Handler as middleware [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- PageRoute(path, handler): serve a handler for a static GET path at middleware level [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
### Operations
- Heartbeat(endpoint): health endpoint such as /ping [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]
- Profiler(): subrouter that mounts net/http/pprof [https://pkg.go.dev/github.com/go-chi/chi/v5/middleware]

## 1.6 go-chi/render (v1.0.3)
- Respond / Decode: package-level replaceable responder and decoder variables (DefaultResponder, DefaultDecoder) [https://pkg.go.dev/github.com/go-chi/render]
- DefaultResponder: streams JSON or XML chosen from request headers, default JSON [https://pkg.go.dev/github.com/go-chi/render]
- DefaultDecoder: picks decoder from request content type [https://pkg.go.dev/github.com/go-chi/render]
- JSON(w, r, v): marshal, HTML-escape, application/json [https://pkg.go.dev/github.com/go-chi/render]
- XML(w, r, v): marshal, application/xml, adds XML header if missing [https://pkg.go.dev/github.com/go-chi/render]
- HTML(w, r, string), PlainText(w, r, string), Data(w, r, []byte) (application/octet-stream), NoContent (204) [https://pkg.go.dev/github.com/go-chi/render]
- Status(r, code): store status hint in context; StatusCtxKey [https://pkg.go.dev/github.com/go-chi/render]
- Bind(r, Binder): decode body then call Bind(r) hook; Binder interface [https://pkg.go.dev/github.com/go-chi/render]
- Render(w, r, Renderer) / RenderList(w, r, []Renderer): Renderer interface Render(w, r) for response payloads [https://pkg.go.dev/github.com/go-chi/render]
- DecodeJSON, DecodeXML, DecodeForm (v1.0.2) [https://pkg.go.dev/github.com/go-chi/render]
- SetContentType(ContentType) middleware forcing response content type [https://pkg.go.dev/github.com/go-chi/render]
- ContentType enum: Unknown, PlainText, HTML, JSON, XML, Form, EventStream; GetContentType(s), GetRequestContentType(r), GetAcceptedContentType(r); ContentTypeCtxKey [https://pkg.go.dev/github.com/go-chi/render]
- M: alias for map[string]interface{} [https://pkg.go.dev/github.com/go-chi/render]

## 1.7 go-chi/httprate (v0.16.1)
- LimitBy(requestLimit, windowLength, keyFn, options...): canonical rate-limit middleware [https://pkg.go.dev/github.com/go-chi/httprate]
- KeyFunc func(r) (string, error); Key(constant), KeyByEndpoint, JoinKeys(fns...) joined with ":" [https://pkg.go.dev/github.com/go-chi/httprate]
- Deprecated: Limit, LimitAll, LimitByIP, LimitByRealIP, KeyByIP, KeyByRealIP, WithKeyByIP, WithKeyByRealIP, WithKeyFuncs [https://pkg.go.dev/github.com/go-chi/httprate]
- CanonicalizeIP(ip): IPv6 reduced to /64 prefix [https://pkg.go.dev/github.com/go-chi/httprate]
- Options: WithLimitHandler (default 429 "Too Many Requests"), WithErrorHandler, WithLimitCounter (pluggable backend such as Redis), WithResponseHeaders, WithNoop, WithIncrement(ctx, n), WithRequestLimit(ctx, n) [https://pkg.go.dev/github.com/go-chi/httprate]
- ResponseHeaders struct: Limit, Remaining, Increment, Reset, RetryAfter (defaults X-RateLimit-Limit, -Remaining, -Increment, -Reset, Retry-After; empty omits) [https://pkg.go.dev/github.com/go-chi/httprate]
- LimitCounter interface (Config, Increment, IncrementBy, Get); NewLocalLimitCounter(window) default in-memory; LimitCounterKey [https://pkg.go.dev/github.com/go-chi/httprate]
- RateLimiter (v0.13.1): NewRateLimiter, Handler(next), OnLimit(w, r, key), RespondOnLimit(w, r, key), Status(key), Counter() for manual use [https://pkg.go.dev/github.com/go-chi/httprate]

## 1.8 go-chi/cors
- New(Options) *Cors, Handler(Options) middleware, (*Cors).Handler, AllowAll() permissive config [https://pkg.go.dev/github.com/go-chi/cors]
- Options: AllowedOrigins (default ["*"], one wildcard per origin), AllowOriginFunc(r, origin), AllowedMethods (default HEAD GET POST), AllowedHeaders (Origin always appended, "*" allowed), ExposedHeaders, AllowCredentials, MaxAge (seconds), OptionsPassthrough, Debug [https://pkg.go.dev/github.com/go-chi/cors]
- Cors.Log (Logger interface with Printf) [https://pkg.go.dev/github.com/go-chi/cors]

## 1.9 go-chi/jwtauth/v5 (v5.4.0)
- New(alg, signKey, verifyKey, validateOptions...) *JWTAuth; Encode(claims), Decode(tokenString), ValidateOptions() [https://pkg.go.dev/github.com/go-chi/jwtauth/v5]
- Verifier(ja): finds, decodes, verifies token, stores in context, always calls next [https://pkg.go.dev/github.com/go-chi/jwtauth/v5]
- Verify(ja, findTokenFns...): custom token lookup; VerifyRequest, VerifyToken [https://pkg.go.dev/github.com/go-chi/jwtauth/v5]
- Authenticator(ja): 401 for unverified tokens [https://pkg.go.dev/github.com/go-chi/jwtauth/v5]
- Token finders: TokenFromHeader (Authorization BEARER), TokenFromCookie ("jwt"), TokenFromQuery ("jwt") [https://pkg.go.dev/github.com/go-chi/jwtauth/v5]
- Context: NewContext, FromContext(ctx) (token, claims, err), TokenCtxKey, ErrorCtxKey [https://pkg.go.dev/github.com/go-chi/jwtauth/v5]
- Claim helpers: SetExpiry, SetExpiryIn, SetIssuedAt, SetIssuedNow, ExpireIn, EpochNow, UnixTime, ErrorReason [https://pkg.go.dev/github.com/go-chi/jwtauth/v5]
- Errors: ErrUnauthorized, ErrExpired, ErrNBFInvalid, ErrIATInvalid, ErrNoTokenFound, ErrAlgoInvalid [https://pkg.go.dev/github.com/go-chi/jwtauth/v5]

## 1.10 go-chi/httplog/v3
- RequestLogger(*slog.Logger, *Options): structured request logging on log/slog [https://pkg.go.dev/github.com/go-chi/httplog/v3]
- Options: Level (Debug all incl. OPTIONS, Info excl. OPTIONS, Warn 4xx/5xx except 429, Error 5xx), Schema, RecoverPanics, Skip(req, status), LogRequestHeaders (default Content-Type, Origin), LogRequestBody, LogResponseHeaders, LogResponseBody, LogBodyContentTypes, LogBodyMaxLen (default 1024, -1 full), LogExtraAttrs [https://pkg.go.dev/github.com/go-chi/httplog/v3]
- Schemas SchemaECS (Elastic), SchemaOTEL, SchemaGCP; Schema.Concise(bool); Schema.ReplaceAttr (slog ReplaceAttr) [https://pkg.go.dev/github.com/go-chi/httplog/v3]
- SetAttrs(ctx, attrs...), SetError(ctx, err), CURL(req, body) curl reproduction, ErrClientAborted, ErrorKey, GCPLevel, per-schema ResponseDuration constants [https://pkg.go.dev/github.com/go-chi/httplog/v3]

## 1.11 go-chi/stampede
- Handler(logger, cache, ttl, options...): coalesce and cache identical requests [https://pkg.go.dev/github.com/go-chi/stampede]
- HandlerWithKey(..., CacheKeyFunc, ...): custom cache key [https://pkg.go.dev/github.com/go-chi/stampede]
- Singleflight(logger, varyRequestHeaders): coalesce without caching [https://pkg.go.dev/github.com/go-chi/stampede]
- `NewStampede[V](logger, cache, options...)`: generic coalescer [https://pkg.go.dev/github.com/go-chi/stampede]
- Options: WithTTL (default 1 min, DefaultCacheTTL), WithSkipCache, WithHTTPCacheKeyRequestBody (default true), WithHTTPCacheKeyRequestHeaders, WithHTTPStatusTTL(status -> ttl) [https://pkg.go.dev/github.com/go-chi/stampede]
- BytesToHash, StringToHash helpers [https://pkg.go.dev/github.com/go-chi/stampede]

## 1.12 go-chi/hostrouter
- New() Routes (map host -> chi.Router); Map(host, router); Unmap(host); ServeHTTP selects router by Host [https://pkg.go.dev/github.com/go-chi/hostrouter]
- Host patterns in README example: exact host, "*.doma.in" subdomains, "*" fallback, "" default [https://pkg.go.dev/github.com/go-chi/hostrouter]
- Implements chi.Routes: Find, Match, Middlewares, Routes [https://pkg.go.dev/github.com/go-chi/hostrouter]

## 1.13 go-chi/docgen
- PrintRoutes(r), JSONRoutesDoc(r), MarkdownRoutesDoc(r, MarkdownOpts), BuildDoc(r), GetFuncInfo(i) [https://pkg.go.dev/github.com/go-chi/docgen]
- MarkdownOpts: ProjectPath, Intro, ForceRelativeLinks, URLMap; MarkdownDoc.Generate/String/WriteIntro/WriteRoutes [https://pkg.go.dev/github.com/go-chi/docgen]
- Doc types: Doc, DocRouter, DocRoutes, DocRoute, DocHandlers, DocHandler, DocMiddleware, FuncInfo (Pkg, Func, Comment, File, Line, Anonymous, Unresolvable) [https://pkg.go.dev/github.com/go-chi/docgen]

## 1.14 go-chi/transport (client RoundTripper middleware, v0.6.2)
- Chain(base, mw...), If(cond, mw), SetHeader, SetHeaderFunc, LogRequests(LogOptions{Concise, CURL}), Retry(base, max), DelayedRequest, DelayedResponse (timeout testing), CloneRequest, RoundTripFunc [https://pkg.go.dev/github.com/go-chi/transport]

## 1.15 go-chi/metrics
- Collector(CollectorOpts{Host, Proto, Skip}): request metrics middleware; Handler(): OpenMetrics output; Transport(TransportOpts{Host}): client metrics [https://pkg.go.dev/github.com/go-chi/metrics]
- Counter, Gauge, Histogram(name, help, buckets) and typed-label generics CounterWith[T], GaugeWith[T], HistogramWith[T] [https://pkg.go.dev/github.com/go-chi/metrics]

## 1.16 Other go-chi repositories
- httptracer: "Tracing middleware for go-chi" (README body not readable) [https://github.com/go-chi/httptracer]
- httpvcr: record/replay HTTP interactions to cassette files for deterministic tests, filters sensitive data, ignores headers, archived 2025-03-23 [https://github.com/go-chi/httpvcr]
- docs: chi docs; typeid: archived; org lists 23 repos, 10 shown [https://github.com/go-chi]

## 1.17 chi version history (feature origins)
- v5.0.0 module path /v5; v1.5.1 CleanPath, ServerBaseContext removed; v4.1.1 RouteHeaders; v4.0.4 pretty stacks in Recoverer; v3.3.2 ContentCharset; v3.3.1 AllowContentType, SetHeader; v3.3.0 RegisterMethod; v3.2.1 Match, RouteMethod, GetHead; v3.1.0 URLFormat; v2.1.0 render, MethodNotAllowed [https://github.com/go-chi/chi/blob/master/CHANGELOG.md]

# 2. Go standard library net/http (server side)

## 2.1 ServeMux patterns (Go 1.22+)
- Pattern form `[METHOD ][HOST]/[PATH]`, all parts optional; method followed by space or tab (multiple spaces/tabs allowed since 1.23) [https://pkg.go.dev/net/http] [https://go.dev/doc/go1.23]
- Methods: no method matches all; GET also matches HEAD; otherwise exact [https://pkg.go.dev/net/http]
- Hosts: pattern with host matches only that host (port stripped from Host) [https://pkg.go.dev/net/http]
- Wildcards `{NAME}` (one segment) and `{NAME...}` (rest of path, must be last); names are Go identifiers; wildcard must fill a whole segment (`/b_{bucket}` invalid) [https://pkg.go.dev/net/http]
- Trailing slash acts as anonymous `...`; `{$}` matches end of URL only (`/{$}` matches only "/") [https://pkg.go.dev/net/http]
- Escaping: segments unescaped one at a time, %2F is not a separator [https://pkg.go.dev/net/http]
- Precedence: most specific wins, conflicts panic at Handle/HandleFunc; exception: host pattern beats conflicting no-host pattern; registration order irrelevant [https://pkg.go.dev/net/http] [https://go.dev/doc/go1.22]
- Method-qualified pattern beats unqualified [https://go.dev/doc/go1.22]
- Subtree root without trailing slash redirects to slashed form unless registered; redirect status 307 since 1.26 [https://pkg.go.dev/net/http] [https://go.dev/doc/go1.26]
- Request sanitizing: `.`, `..`, repeated slashes redirected to clean URL; CONNECT uses host and path unchanged [https://pkg.go.dev/net/http]
- GODEBUG=httpmuxgo121=1 restores pre-1.22 behavior [https://go.dev/doc/go1.22]
- ServeMux methods: NewServeMux, Handle, HandleFunc, Handler(r) (handler, pattern), ServeHTTP; package-level Handle/HandleFunc on DefaultServeMux [https://pkg.go.dev/net/http]
- Request.PathValue(name), Request.SetPathValue(name, value) (1.22) [https://go.dev/doc/go1.22]
- Request.Pattern: the matched ServeMux pattern (1.23), unset with httpmuxgo121=1 [https://go.dev/doc/go1.23]

## 2.2 http.Server fields
- Addr (default ":http"/":https"), Handler (nil = DefaultServeMux) [https://pkg.go.dev/net/http]
- DisableGeneralOptionsHandler: when true OPTIONS * goes to Handler, else 200 with Content-Length 0 [https://pkg.go.dev/net/http]
- TLSConfig (cloned by ServeTLS/ListenAndServeTLS) [https://pkg.go.dev/net/http]
- ReadTimeout (whole request incl. body), ReadHeaderTimeout (falls back to ReadTimeout), WriteTimeout (resets on each new request header), IdleTimeout (falls back to ReadTimeout) [https://pkg.go.dev/net/http]
- MaxHeaderBytes (default DefaultMaxHeaderBytes 1 MB, includes request line) and MaxHeaderValueCount (default 500; repeated header lines counted each, trailers separately) [https://pkg.go.dev/net/http]
- TLSNextProto: ALPN protocol handlers; non-nil disables automatic HTTP/2 [https://pkg.go.dev/net/http]
- ConnState: callback on connection state change [https://pkg.go.dev/net/http]
- ErrorLog: logger for accept errors and misbehaving handlers [https://pkg.go.dev/net/http]
- BaseContext(listener) base context for requests; ConnContext(ctx, conn) per-connection context carrying ServerContextKey [https://pkg.go.dev/net/http]
- HTTP2 (*HTTP2Config) and Protocols (*Protocols), both 1.24 [https://go.dev/doc/go1.24]
- DisableClientPriority: ignore RFC 9218 priorities, round-robin [https://pkg.go.dev/net/http]

## 2.3 http.Server methods and lifecycle
- ListenAndServe, ListenAndServeTLS(cert, key), Serve(listener), ServeTLS(listener, cert, key); always return non-nil error, ErrServerClosed after Shutdown/Close [https://pkg.go.dev/net/http]
- Package functions ListenAndServe, ListenAndServeTLS, Serve, ServeTLS using default Server [https://pkg.go.dev/net/http]
- Shutdown(ctx): close listeners, close idle connections, wait for active ones; returns ctx error if it expires; does not handle hijacked connections; server not reusable [https://pkg.go.dev/net/http]
- RegisterOnShutdown(f): hook run at Shutdown (for hijacked / ALPN-upgraded connections), must not block [https://pkg.go.dev/net/http]
- Close(): immediately close listeners and connections in New/Active/Idle [https://pkg.go.dev/net/http]
- SetKeepAlivesEnabled(bool) [https://pkg.go.dev/net/http]
- ConnState values: StateNew, StateActive, StateIdle, StateHijacked, StateClosed; ConnState.String() [https://pkg.go.dev/net/http]
- Context keys ServerContextKey (the *Server) and LocalAddrContextKey (local address) [https://pkg.go.dev/net/http]

## 2.4 HTTP/2, h2c, TLS
- Automatic HTTP/2 on Server over TLS (ALPN "h2"); Serve enables it only for *tls.Conn with "h2" in NextProtos [https://pkg.go.dev/net/http]
- Server.Protocols / Transport.Protocols (1.24): type Protocols with HTTP1, HTTP2 (TLS), UnencryptedHTTP2 (h2c) and methods HTTP1(), HTTP2(), UnencryptedHTTP2(), SetHTTP1/2/UnencryptedHTTP2, String [https://pkg.go.dev/net/http] [https://go.dev/doc/go1.24]
- UnencryptedHTTP2 uses prior knowledge (RFC 9113 3.3); `Upgrade: h2c` not supported; HTTP/1 and h2c can share a port [https://go.dev/doc/go1.24]
- HTTP2Config (1.24): MaxConcurrentStreams (server, default at least 100), MaxDecoderHeaderTableSize, MaxEncoderHeaderTableSize, MaxReadFrameSize (16 KiB-16 MiB), MaxReceiveBufferPerConnection, MaxReceiveBufferPerStream, SendPingTimeout, PingTimeout (0 = 15 s), WriteByteTimeout, PermitProhibitedCipherSuites, CountError callback; StrictMaxConcurrentRequests (1.26, transport) [https://pkg.go.dev/net/http] [https://go.dev/doc/go1.26]
- GODEBUG http2server=0, http2client=0, http2debug=1/2; build tag omithttp2 [https://pkg.go.dev/net/http]
- ListenAndServeTLS: cert file should include intermediates; TLSConfig.Certificates/GetCertificate/GetConfigForClient allow no files [https://pkg.go.dev/net/http]
- Pusher interface: Push(target, *PushOptions) HTTP/2 server push (Method GET/HEAD, Header); ErrNotSupported if disabled [https://pkg.go.dev/net/http]
- crypto/tls related: server min TLS 1.2 default (1.22); Encrypted Client Hello via Config.EncryptedClientHelloKeys (1.24), GetEncryptedClientHelloKeys (1.25) [https://go.dev/doc/go1.22] [https://go.dev/doc/go1.24] [https://go.dev/doc/go1.25]

## 2.5 Request fields and methods (server relevant)
- Fields: Method, URL, Proto/ProtoMajor/ProtoMinor, Header (Host moved to Host), Body (always non-nil, server closes), ContentLength (-1 unknown), TransferEncoding, Close, Host (validate against DNS rebinding), Form/PostForm/MultipartForm, Trailer, RemoteAddr, RequestURI, TLS, Pattern [https://pkg.go.dev/net/http]
- Context(), WithContext(ctx), Clone(ctx) [https://pkg.go.dev/net/http]
- ParseForm, ParseMultipartForm(maxMemory), MultipartReader, FormValue, PostFormValue, FormFile (ErrMissingFile) [https://pkg.go.dev/net/http]
- ErrMissingBoundary, ErrNotMultipart [https://pkg.go.dev/net/http]
- BasicAuth() / SetBasicAuth, Referer(), UserAgent(), ProtoAtLeast [https://pkg.go.dev/net/http]
- Cookie(name), Cookies(), CookiesNamed(name) (1.23), AddCookie [https://pkg.go.dev/net/http] [https://go.dev/doc/go1.23]

## 2.6 ResponseWriter and related interfaces
- ResponseWriter: Header (changes after WriteHeader ignored except 1xx/trailers; nil value suppresses auto headers like Date), Write (implies 200, sniffs first 512 bytes), WriteHeader (1xx informational allowed multiple times, then one 2xx-5xx) [https://pkg.go.dev/net/http]
- Expect: 100-continue: server sends 100 Continue automatically on first body read [https://pkg.go.dev/net/http]
- Trailers: declare via Trailer header before write, or use TrailerPrefix ("Trailer:") on Header keys after write [https://pkg.go.dev/net/http]
- Flusher (Flush), Hijacker (Hijack, HTTP/1 only), CloseNotifier (deprecated), Pusher [https://pkg.go.dev/net/http]
- ResponseController (1.20): NewResponseController(rw), Flush, Hijack, SetReadDeadline, SetWriteDeadline, EnableFullDuplex; follows Unwrap chains; ErrNotSupported [https://pkg.go.dev/net/http]
- Handler interface, HandlerFunc; panic in handler recovered and logged; panic(ErrAbortHandler) aborts silently [https://pkg.go.dev/net/http]
- Error(w, msg, code) (plain text, nosniff), NotFound / NotFoundHandler, Redirect(w, r, url, code), RedirectHandler [https://pkg.go.dev/net/http]
- 1xx limits: ErrBodyNotAllowed, ErrHijacked, ErrContentLength, ErrHandlerTimeout [https://pkg.go.dev/net/http]

## 2.7 Handler wrappers
- MaxBytesReader(w, r, n) returning *MaxBytesError (Limit), may close connection; MaxBytesHandler(h, n) [https://pkg.go.dev/net/http]
- TimeoutHandler(h, dt, msg): 503 on timeout, later writes get ErrHandlerTimeout, supports Pusher not Hijacker/Flusher [https://pkg.go.dev/net/http]
- StripPrefix(prefix, h): strips from Path and RawPath, 404 if no exact match [https://pkg.go.dev/net/http]
- AllowQuerySemicolons(h): restores pre-1.17 `;` as separator [https://pkg.go.dev/net/http]
- CrossOriginProtection (1.25): NewCrossOriginProtection, AddTrustedOrigin(origin) error, AddInsecureBypassPattern(pattern) (ServeMux syntax, panics on conflict, direct matches only), SetDenyHandler(h) (default 403), Check(req) error, Handler(h) [https://pkg.go.dev/net/http] [https://go.dev/doc/go1.25]
- CrossOriginProtection logic: GET/HEAD/OPTIONS allowed; uses Sec-Fetch-Site or compares Origin host with Host; no headers means allowed; zero value usable [https://pkg.go.dev/net/http]

## 2.8 Static files and content
- ServeContent(w, req, name, modtime, io.ReadSeeker): Range, conditional headers, MIME by name; strips Cache-Control/Content-Encoding/Etag/Last-Modified on error (1.23, GODEBUG httpservecontentkeepheaders=1) [https://pkg.go.dev/net/http] [https://go.dev/doc/go1.23]
- ServeFile (rejects `..`, redirects /index.html), ServeFileFS (1.22) [https://pkg.go.dev/net/http] [https://go.dev/doc/go1.22]
- FileServer(FileSystem), FileServerFS(fs.FS) (1.22); /index.html redirects; Dir type (follows symlinks, serves dot-files); FileSystem and File interfaces; FS(fs.FS) adapter [https://pkg.go.dev/net/http]
- NewFileTransport / NewFileTransportFS (1.22) [https://go.dev/doc/go1.22]
- DetectContentType (first 512 bytes), TimeFormat, ParseTime, CanonicalHeaderKey, StatusText, status and method constants [https://pkg.go.dev/net/http]

## 2.9 Cookies and headers
- Cookie fields: Name, Value, Quoted (1.23), Path, Domain, Expires, RawExpires, MaxAge, Secure, HttpOnly, SameSite, Partitioned (1.23), Raw, Unparsed; String(), Valid() [https://pkg.go.dev/net/http] [https://go.dev/doc/go1.23]
- SameSite constants: SameSiteDefaultMode, LaxMode, StrictMode, NoneMode [https://pkg.go.dev/net/http]
- SetCookie(w, cookie); ParseCookie (Cookie header) and ParseSetCookie (1.23); ErrNoCookie [https://pkg.go.dev/net/http] [https://go.dev/doc/go1.23]
- Header: Add, Set, Get, Values, Del, Clone, Write, WriteSubset [https://pkg.go.dev/net/http]
- 1.22: server rejects invalid empty Content-Length (GODEBUG httplaxcontentlength=1) [https://go.dev/doc/go1.22]
- 1.23: on-the-fly Content-Encoding in wrapping middleware affected by ServeContent header-stripping change [https://go.dev/doc/go1.23]

## 2.10 Release-note items 1.22 to 1.26 (net/http and server related)
- 1.22: enhanced patterns, PathValue/SetPathValue, ServeFileFS/FileServerFS/NewFileTransportFS, io.Copy splice/sendfile on Linux [https://go.dev/doc/go1.22]
- 1.23: Request.Pattern, Request.CookiesNamed, Cookie.Quoted/Partitioned, ParseCookie/ParseSetCookie, httptest.NewRequestWithContext, spaces/tabs after method in patterns, net.KeepAliveConfig [https://go.dev/doc/go1.23]
- 1.24: Server.Protocols, Server.HTTP2, unencrypted HTTP/2, 1xx limit change, os.Root, ECH in TLS server, MPTCP default listen on Linux [https://go.dev/doc/go1.24]
- 1.25: net/http.CrossOriginProtection (only net/http entry); crypto/tls GetEncryptedClientHelloKeys [https://go.dev/doc/go1.25]
- 1.26: HTTP2Config.StrictMaxConcurrentRequests, Transport.NewClientConn, ServeMux trailing-slash redirect 307, httptest Server.Client redirects example.com, ReverseProxy.Director deprecated for Rewrite [https://go.dev/doc/go1.26]

## 2.11 net/http/httptest
- NewRecorder() / ResponseRecorder: Header, Write, WriteString, WriteHeader, Flush, Result [https://pkg.go.dev/net/http/httptest]
- NewRequest / NewRequestWithContext (server-side requests) [https://pkg.go.dev/net/http/httptest]
- NewServer, NewTLSServer, NewUnstartedServer, NewTestServer(t, handler) (in-memory network, doc shows 1.27); Server: Start, StartTLS, Client, Certificate, Close, CloseClientConnections [https://pkg.go.dev/net/http/httptest]
- DefaultRemoteAddr "1.2.3.4" [https://pkg.go.dev/net/http/httptest]

## 2.12 net/http/pprof and expvar
- pprof endpoints: /debug/pprof/, cmdline, profile (default 30 s), symbol, trace (default 1 s), {name} for runtime profiles; params debug=N, gc=N (heap), seconds=N (delta profile); GET only since 1.22; functions Cmdline, Handler(name), Index, Profile, Symbol, Trace [https://pkg.go.dev/net/http/pprof]
- expvar: /debug/vars JSON (GET only since 1.22), cmdline and memstats auto-published; Publish, Get, Do, Handler, NewInt/NewFloat/NewMap/NewString, types Int, Float, Map, String, Func, KeyValue, Var [https://pkg.go.dev/expvar]

## 2.13 net/http/httputil
- ReverseProxy: Rewrite (preferred) with ProxyRequest{In, Out, SetURL, SetXForwarded}; Director (deprecated); Transport; FlushInterval (negative = flush each write; streaming flushed immediately); ErrorLog; BufferPool (Get/Put); ModifyResponse; ErrorHandler (default 502); forwards 1xx; removes hop-by-hop headers (Connection, Proxy-Connection, Keep-Alive, Proxy-Authenticate, Proxy-Authorization, TE, Trailer, Transfer-Encoding, Upgrade) [https://pkg.go.dev/net/http/httputil]
- NewSingleHostReverseProxy(target) (uses Director, does not rewrite Host) [https://pkg.go.dev/net/http/httputil]
- DumpRequest, DumpRequestOut, DumpResponse; NewChunkedReader/Writer; deprecated ClientConn/ServerConn [https://pkg.go.dev/net/http/httputil]
