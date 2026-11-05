# Fiber v3 feature inventory

Sources: pages of https://docs.gofiber.io/ (read via WebFetch for api/fiber, api/app, api/ctx (part), api/bind, api/redirect, api/hooks, api/services, guide/routing, guide/error-handling; the rest read from the same pages' Markdown sources in the docs/ folder of github.com/gofiber/fiber, cloned shallow, which docs.gofiber.io publishes), plus github.com/gofiber/contrib (v3/*) and github.com/gofiber/storage READMEs. URL base for docs pages: https://docs.gofiber.io/<path>. Format: `name: what it does (key options) [URL]`.

## 1. App configuration (fiber.Config) [https://docs.gofiber.io/api/fiber]
- AppName: application name shown at startup and passed to OnListen hooks; does not set Server header [https://docs.gofiber.io/api/fiber]
- BodyLimit: max request body (default 4 MiB), larger gets 413 [https://docs.gofiber.io/api/fiber]
- CaseSensitive: /Foo and /foo differ when true (default false) [https://docs.gofiber.io/api/fiber]
- StrictRouting: trailing slash significant [https://docs.gofiber.io/api/fiber]
- UnescapePath: decode all percent-escapes before routing [https://docs.gofiber.io/api/fiber]
- CBORDecoder / CBOREncoder: pluggable CBOR codec (unimplemented by default) [https://docs.gofiber.io/api/fiber]
- MsgPackDecoder / MsgPackEncoder: pluggable MessagePack codec (unimplemented by default; e.g. shamaton/msgpack) [https://docs.gofiber.io/api/fiber]
- JSONDecoder / JSONEncoder: pluggable JSON codec (default encoding/json) [https://docs.gofiber.io/api/fiber]
- XMLDecoder / XMLEncoder: pluggable XML codec (default encoding/xml) [https://docs.gofiber.io/api/fiber]
- ColorScheme: ANSI colors for startup message, route list, some middleware [https://docs.gofiber.io/api/fiber]
- CompressedFileSuffixes: suffixes for pre-compressed static variants (gzip .fiber.gz, br .fiber.br, zstd .fiber.zst) [https://docs.gofiber.io/api/fiber]
- Concurrency: max concurrent connections (default 256*1024) [https://docs.gofiber.io/api/fiber]
- DisableDefaultContentType: omit default Content-Type header [https://docs.gofiber.io/api/fiber]
- DisableDefaultDate: omit Date header [https://docs.gofiber.io/api/fiber]
- DisableFlashMessages: turn off redirect flash messages and cookie handling [https://docs.gofiber.io/api/fiber]
- DisableHeadAutoRegister: stop auto-generating HEAD routes from GET [https://docs.gofiber.io/api/fiber]
- DisableHeaderNormalizing: keep header name casing as sent [https://docs.gofiber.io/api/fiber]
- DisableKeepalive: close connection after first response [https://docs.gofiber.io/api/fiber]
- DisablePreParseMultipartForm: skip pre-parsing multipart bodies [https://docs.gofiber.io/api/fiber]
- EnableIPValidation: validate IPs returned by c.IP()/c.IPs() [https://docs.gofiber.io/api/fiber]
- EnableSplittingOnParsers: split query/body/header values on commas when binding [https://docs.gofiber.io/api/fiber]
- ErrorHandler: central handler for returned errors and refused requests (default DefaultErrorHandler) [https://docs.gofiber.io/api/fiber]
- GETOnly: reject all methods but GET/HEAD with 405 [https://docs.gofiber.io/api/fiber]
- IdleTimeout: max wait for next keep-alive request (0 uses ReadTimeout) [https://docs.gofiber.io/api/fiber]
- ReadTimeout / WriteTimeout: max time to read full request / write response (0 unlimited) [https://docs.gofiber.io/api/fiber]
- Immutable: make context method return values safe beyond the handler [https://docs.gofiber.io/api/fiber]
- MaxRanges: max ranges parsed from Range header (default 16) [https://docs.gofiber.io/api/fiber]
- PassLocalsToContext: propagate StoreInContext locals into request context [https://docs.gofiber.io/api/fiber]
- PassLocalsToViews: pass locals to the template engine [https://docs.gofiber.io/api/fiber]
- ProxyHeader: header to read real client IP from (needs TrustProxy) [https://docs.gofiber.io/api/fiber]
- TrustProxy / TrustProxyConfig: trust proxy headers from listed IPs, CIDRs, ranges (also Loopback/LinkLocal/Private options) [https://docs.gofiber.io/api/fiber] [https://docs.gofiber.io/guide/reverse-proxy]
- ReadBufferSize / WriteBufferSize: per-connection buffers (default 8192); read buffer also caps header size (431) [https://docs.gofiber.io/api/fiber]
- ReduceMemoryUsage: lower memory at CPU cost [https://docs.gofiber.io/api/fiber]
- RegexHandler: compiler used for regex() route constraints (swap regex engine) [https://docs.gofiber.io/api/fiber] [https://docs.gofiber.io/guide/faster-fiber]
- RequestMethods: allowed HTTP methods; others get 501 [https://docs.gofiber.io/api/fiber]
- ServerHeader: value of Server response header [https://docs.gofiber.io/api/fiber]
- Services: services (containers, DBs) started with the app and terminated on shutdown [https://docs.gofiber.io/api/fiber] [https://docs.gofiber.io/api/services]
- ServicesStartupContextProvider / ServicesShutdownContextProvider: context supplied to service Start/State and Terminate [https://docs.gofiber.io/api/fiber]
- SharedStorage / SharedStatePrefix: storage and key prefix backing SharedState [https://docs.gofiber.io/api/fiber]
- SkipUnmatchedRoutes: answer unmatched paths with 404/405 before middleware runs [https://docs.gofiber.io/api/fiber]
- StreamRequestBody: stream request body instead of buffering [https://docs.gofiber.io/api/fiber]
- StructValidator: validator invoked automatically during binding [https://docs.gofiber.io/api/fiber]
- Views / ViewsLayout: template engine interface and global default layout [https://docs.gofiber.io/api/fiber]
- fiber.New(config...), NewWithCustomCtx(fn, config...): construct app, optionally with custom context type [https://docs.gofiber.io/api/fiber] [https://docs.gofiber.io/api/app]
- fiber.NewError / NewErrorf: build *fiber.Error with status code and (formatted) message [https://docs.gofiber.io/api/fiber]
- fiber.IsChild(): whether the process is a prefork child [https://docs.gofiber.io/api/fiber]
- Config() (read-only app config), Server() (underlying *fasthttp.Server), Handler() (fasthttp handler for custom servers) [https://docs.gofiber.io/api/fiber] [https://docs.gofiber.io/api/app]
- Constants package: HTTP methods, MIME types, status codes, errors, network types, content-coding names, SameSite modes, route constraint names [https://docs.gofiber.io/api/constants]
- Custom Ctx interface (CustomCtx / NewWithCustomCtx): embed fiber.DefaultCtx in your own type [https://docs.gofiber.io/whats_new]
- Protocol error responses: 431 oversize headers, 413 oversize body, 408/502 read errors, 501 bad method/Transfer-Encoding, 400 parse failures [https://docs.gofiber.io/guide/error-handling]
- App.State(), App.SharedState(): process-local and storage-backed state (see section 11) [https://docs.gofiber.io/api/app]
- App.GetString/GetBytes: detach values when Immutable is on [https://docs.gofiber.io/api/app]
- App.ReloadViews / App.Render: reload view engines; render a template to an io.Writer without sending [https://docs.gofiber.io/api/app]
- App.AcquireCtx / ReleaseCtx: pooled ctx for manually run handlers [https://docs.gofiber.io/api/app]
- App.ErrorHandler(c, err): run error handler manually (mounted sub-apps may have own) [https://docs.gofiber.io/api/app]
- App.SetTLSHandler(*TLSHandler): record ClientHello info for a custom Listener [https://docs.gofiber.io/api/app]
- Configurable TLS minimum version (TLSMinVersion, TLS 1.2/1.3) [https://docs.gofiber.io/whats_new]
- Custom JSON encoder/decoder guide (sonic, go-json, etc.) and alternative regex engines for regex() [https://docs.gofiber.io/guide/faster-fiber]

## 2. Listening, TLS, prefork, shutdown
- Listen(addr, ListenConfig...): serve HTTP/HTTPS [https://docs.gofiber.io/api/fiber]
- Listener(ln, ListenConfig...): serve on a caller-supplied net.Listener (ignores most TLS/prefork fields) [https://docs.gofiber.io/api/fiber]
- ListenConfig.CertFile / CertKeyFile: TLS cert and key [https://docs.gofiber.io/api/fiber]
- ListenConfig.CertClientFile: CA bundle for client certificate verification (mTLS) [https://docs.gofiber.io/api/fiber]
- ListenConfig.TLSConfig / TLSConfigFunc / TLSMinVersion: supply or tweak tls.Config (TLSConfig overrides cert fields) [https://docs.gofiber.io/api/fiber]
- ListenConfig.AutoCertManager: ACME / Let's Encrypt via autocert.Manager [https://docs.gofiber.io/api/fiber]
- ListenConfig.ListenerNetwork: tcp, tcp4 (default), tcp6, unix [https://docs.gofiber.io/api/fiber]
- ListenConfig.UnixSocketFileMode: file mode of Unix socket (default 0770); Fiber cleans stale socket [https://docs.gofiber.io/api/fiber] [https://docs.gofiber.io/whats_new]
- ListenConfig.ListenerAddrFunc: callback with listener address (e.g. OS-chosen port) [https://docs.gofiber.io/api/fiber]
- ListenConfig.BeforeServeFunc: run before serving; error aborts startup [https://docs.gofiber.io/api/fiber]
- ListenConfig.DisableStartupMessage / EnablePrintRoutes: hide banner / print route table [https://docs.gofiber.io/api/fiber]
- ListenConfig.EnablePrefork: multiple processes on same port (SO_REUSEPORT); PreforkRecoverInterval, PreforkRecoverThreshold, PreforkShutdownGracePeriod, PreforkLogger [https://docs.gofiber.io/api/fiber]
- ListenConfig.GracefulContext / ShutdownTimeout (default 10s, negative disables): context-triggered graceful shutdown [https://docs.gofiber.io/api/fiber]
- Shutdown() / ShutdownWithTimeout(d) / ShutdownWithContext(ctx): graceful stop [https://docs.gofiber.io/api/fiber]
- Reverse-proxy guide: real client IP, TrustProxyConfig options, scheme/host behind proxy, HTTP/2, HTTP/3 (QUIC) [https://docs.gofiber.io/guide/reverse-proxy]
- Hijack/Hijacked/Drop/End on Ctx for connection takeover (see section 5) [https://docs.gofiber.io/api/ctx]

## 3. Routing [https://docs.gofiber.io/guide/routing]
- Method helpers: Get, Head, Post, Put, Delete, Connect, Options, Trace, Patch, Query (RFC QUERY method) [https://docs.gofiber.io/api/app]
- Add(methods, path, handlers): same handlers on several methods [https://docs.gofiber.io/api/app]
- All(path, handlers): every HTTP method on exact path [https://docs.gofiber.io/api/app]
- Use: prefix-matching middleware or sub-app, any method; accepts handler alone, path, []string paths [https://docs.gofiber.io/guide/routing]
- c.Next() chain control; multiple handlers per route [https://docs.gofiber.io/guide/routing]
- Registration-order matching, first match wins [https://docs.gofiber.io/guide/routing]
- Path normalization (unreserved percent-escapes decoded, . and .. resolved) [https://docs.gofiber.io/guide/routing]
- Named params `:name` -> c.Params("name") [https://docs.gofiber.io/guide/routing]
- Optional params `:name?` [https://docs.gofiber.io/guide/routing]
- Plus wildcard `+` (one or more chars), asterisk wildcard `*` (zero or more); indexed `*1`, `+1` [https://docs.gofiber.io/guide/routing]
- Escaped special characters with backslash (custom verbs) [https://docs.gofiber.io/guide/routing]
- Multiple params per segment, literal separators (- . :) [https://docs.gofiber.io/guide/routing]
- Constraints `:param<c>`: int, bool, float, guid, minLen, maxLen, len, betweenLen, min, max, range, alpha, datetime(layout), regex(expr); multiple joined by `;`; work on optional params; failure => 404 [https://docs.gofiber.io/guide/routing]
- RegisterCustomConstraint (CustomConstraint interface; ConstraintHandler, ConstraintAnalyzer for arg parsing); can override built-in names [https://docs.gofiber.io/guide/routing] [https://docs.gofiber.io/api/app]
- RoutePatternMatch(path, pattern, cfg): test whether a path matches a route pattern [https://docs.gofiber.io/guide/utils]
- Group(prefix, handlers...): nested sub-routers with shared prefix/middleware [https://docs.gofiber.io/guide/grouping]
- Route(prefix, fn, name?): scoped route definitions inside a callback [https://docs.gofiber.io/guide/grouping]
- RouteChain(path): chain several methods on one path, route-local middleware [https://docs.gofiber.io/guide/grouping]
- Domain(host): host-based routing with `:param` segments (needs TrustProxy for forwarded hosts); DomainParam(c, key, default) [https://docs.gofiber.io/guide/grouping] [https://docs.gofiber.io/api/app]
- Mounting: app.Use("/prefix", subApp); MountPath(); OnMount hook; sub-app ErrorHandler; Mount() removed in v3 [https://docs.gofiber.io/api/app] [https://docs.gofiber.io/whats_new]
- Name(name): name latest route; GetRoute(name).URL(params); c.GetRouteURL(name, params) for URL generation [https://docs.gofiber.io/api/app] [https://docs.gofiber.io/api/ctx]
- GetRoutes(filterUseOption), Stack(), HandlersCount(): route listing/introspection [https://docs.gofiber.io/api/app]
- Automatic HEAD routes for GET (override via explicit Head, disable via DisableHeadAutoRegister, per sub-app opt-out) [https://docs.gofiber.io/guide/routing]
- 404 / 405 with Allow header; SkipUnmatchedRoutes [https://docs.gofiber.io/guide/routing]
- RebuildTree, RemoveRoute, RemoveRouteByName, RemoveRouteFunc: runtime route changes (dev, not thread safe) [https://docs.gofiber.io/api/app]
- Handler shapes: func(Ctx) error, func(Ctx), Express-style (Req, Res, next) 10 shapes, net/http (Handler, HandlerFunc, func(w,r)), fasthttp (RequestHandler, func(*RequestCtx) error); others panic at registration [https://docs.gofiber.io/guide/routing]
- Route Matcher: interactive pattern tester [https://docs.gofiber.io/extra/route-matcher]
- c.Route(), c.RouteName(), c.FullPath(), c.Endpoint(), c.Matched(), c.IsFinal(), c.IsMiddleware(), c.RestartRouting(), c.OverrideParam() [https://docs.gofiber.io/api/ctx]

## 4. Ctx request API [https://docs.gofiber.io/api/ctx]
- Lifecycle/pooling: Abandon, IsAbandoned, ForceRelease, ScheduleReclaim, Reset
- App(), Bind(), Redirect(), Request() (*fasthttp.Request), RequestCtx(), Response()
- Context()/SetContext(): context.Context integration; Ctx also satisfies context.Context (Deadline, Done, Err, Value) [https://docs.gofiber.io/guide/context]
- Locals(key, value) + generic fiber.Locals[V]; SetLocal; ViewBind (template vars); Value
- ID(), StartTime(), Elapsed(), String()
- Hijack/Hijacked, LocalAddr, RemoteAddr, ClientHelloInfo (TLS)
- Error(): build *fiber.Error without writing
- Headers: Get, GetAll, GetHeaders/GetReqHeaders, HasHeader, HasHeaderValue, generic fiber.GetReqHeader[V]
- Content negotiation: Accepts, AcceptsCharsets, AcceptsEncodings, AcceptsLanguages, AcceptsLanguagesExtended, AcceptsEventStream, AcceptsHTML, AcceptsJSON, AcceptsXML, AcceptEncoding, AcceptLanguage
- Auth headers: Authorization (scheme+credentials split), Bearer
- Content type: ContentType, MediaType, Charset, ContentLength, Is, IsForm, IsJSON, IsMultipart, IsWebSocket, IsPreflight (CORS), IsIdempotent, IsSafe, XHR
- Conditional/caching: Fresh, Stale, IfModifiedSince, IfNoneMatch
- Origin, Referer, UserAgent, RequestID
- Body: Body (auto-decompressed per Content-Encoding), BodyRaw (raw), BodyStream (with StreamRequestBody), HasBody
- Forms/files: FormValue, FormFile, MultipartForm, SaveFile, SaveFileToStorage
- Cookies: Cookies, CookieNames, AllCookies
- URL/host: Method (override), Path (override), OriginalURL, FullURL, BaseURL, URI (*fasthttp.URI), Host, Hostname, Port, Subdomains, Scheme, Secure, Protocol
- Params/query: Params (with default; generic fiber.Params[V]), Query (generic fiber.Query[V]), Queries, Range (Range header parsing, MaxRanges)
- Client IP: IP, IPs (X-Forwarded-For), IsFromLocal, IsFromUnixSocket, IsProxyTrusted
- Generic helpers: fiber.Convert[T], GetReqHeader, Locals, Params, Query [https://docs.gofiber.io/guide/utils]
- IsMethodSafe / IsMethodIdempotent helpers [https://docs.gofiber.io/guide/utils]
- Context helpers: requestid.FromContext, csrf.TokenFromContext, session.FromContext, basicauth.UsernameFromContext, keyauth.TokenFromContext usable from c or context.Context; PassLocalsToContext / StoreInContext [https://docs.gofiber.io/guide/context]
- Using context outside handler, cancellation with goroutines [https://docs.gofiber.io/guide/context]
- Req/Res split: c.Req() / c.Res() method placement [https://docs.gofiber.io/whats_new]

## 5. Ctx response API [https://docs.gofiber.io/api/ctx]
- Headers: Set, Add, Append, Del, GetRespHeader, GetRespHeaders, Vary (case-insensitive, `*` handling), Links (Link header), Location, Type (content type from extension), ContentType/ContentLength/Body (Res)
- Status: Status (chainable, validates 100-999), StatusCode, SendStatus, NoContent (204 without Content-Type)
- Body writers: Send, SendString, Write, Writef, WriteString, Written, ResetBody
- JSON(data, ctype?), JSONP(data, callback; sanitized callback name, nosniff), XML, CBOR, MsgPack (need encoders for last two)
- AutoFormat(body): negotiate text/JSON/XML/MsgPack/CBOR from Accept, adds Vary: Accept
- Format(ResFmt...): Accept-based handler selection with "default", 406 otherwise
- Render(name, bind, layouts...): template view -> text/html (Views engine, ViewBind, PassLocalsToViews)
- SendFile(path, SendFile config): FS (fs.FS/embed.FS), Compress (cached compressed), ByteRange, Download, CacheDuration, MaxAge
- Download(file, filename?): attachment via SendFile; Attachment(filename?): Content-Disposition (adds filename* for non-ASCII)
- SendStream(reader, size?): async stream body; SendStreamWriter(func(*bufio.Writer)): buffered stream writer (SSE-style)
- SendEarlyHints(links...): 103 Early Hints
- Cookie(*Cookie): Path, Domain, MaxAge, Expires, Secure, HTTPOnly, SameSite, Partitioned, SessionOnly (SameSite=None forces Secure); ClearCookie(names...); GetCookie/GetCookies (pending response cookies)
- Drop(): close connection without response; End(): flush and close
- Redirect() helper (section 7)

## 6. Binding and validation [https://docs.gofiber.io/api/bind] [https://docs.gofiber.io/guide/validation]
- c.Bind().Body (decoder by Content-Type), Form (urlencoded/multipart incl. *multipart.FileHeader, []*multipart.FileHeader fields), JSON, XML, CBOR, MsgPack, Query, Header, Cookie, URI, RespHeader, All (fixed precedence URI > Body > Query > Headers > Cookies; `binding_source` tag customises), Custom(name)
- Struct tags: query, form, header, cookie, respHeader, uri, json, xml, cbor, msgpack; `default:` option; `required` option; `validate` tag
- Map binding (map[string]string, map[string][]string, bracket/dot notation `attributes[color]=red`), nested/array notation, 64-segment key depth limit (ErrKeyTooDeep)
- WithAutoHandling (bind error -> 400 *fiber.Error) / WithoutAutoHandling (default, returns *BindError); SkipValidation(bool)
- BindError (Source, Field, MultiError via errors.As)
- RegisterCustomBinder (CustomBinder interface, by name or MIME type)
- SetParserDecoder: global options and type converters for form-style decoder
- StructValidator interface `Validate(any) error` in Config (go-playground/validator wiring guide, custom rules, error shaping)
- EnableSplittingOnParsers: comma splitting for arrays/header lists
- Extractors package (fiber/v3/extractors): FromAuthHeader, FromCookie, FromParam, FromForm, FromHeader, FromQuery, FromCustom, Chain, Resolve (value + source), Walk, Contains; Source enum; ErrNotFound, ErrChainCycle [https://docs.gofiber.io/guide/extractors]

## 7. Redirect and flash messages [https://docs.gofiber.io/api/redirect]
- c.Redirect().To(path) (default 303), Route(name, RedirectConfig{Params, Queries}), Back(fallback) (same-origin referer), Status(code)
- With(key, value, level?): flash message in cookie; Messages(), Message(key) (reading removes)
- WithInput(): store submitted form/query in cookie; OldInputs(), OldInput(key)
- DisableFlashMessages config

## 8. Error handling [https://docs.gofiber.io/guide/error-handling]
- Handlers return errors; central ErrorHandler; DefaultErrorHandler (plain text)
- fiber.NewError/NewErrorf, *fiber.Error status passthrough
- Sub-app ErrorHandler (literal-path mounts and Domain mounts)
- 404 as fiber.ErrNotFound; 405 with Allow; 413, 431, 501, 408/502, 400 mappings
- Recover middleware with PanicHandler
- Status code validation (outside 100-999 => 500)

## 9. Hooks [https://docs.gofiber.io/api/hooks]
- OnRoute, OnName, OnGroup, OnGroupName, OnListen, OnPreStartupMessage, OnPostStartupMessage, OnFork (prefork master gets child PID), OnPreShutdown, OnPostShutdown (receives shutdown error), OnMount
- Client hooks: request hooks, final request hooks, response hooks, built-ins (parserRequestURL, parserRequestHeader, parserRequestBody, parserResponseCookie, logger) [https://docs.gofiber.io/client/hooks]

## 10. Templates / views [https://docs.gofiber.io/guide/templates]
- Views interface for template engines (gofiber/template engines), default html/template, ViewsLayout, c.Render, app.Render to writer, mounted-app views, custom functions, ReloadViews

## 11. Services and state [https://docs.gofiber.io/api/services] [https://docs.gofiber.io/api/state]
- Service interface (Start, String, State, Terminate); auto start in order, terminate in reverse; startup status in banner; error aggregation; unique names; context providers
- GetService[T], MustGetService[T]
- app.State(): Set, Get, MustGet, Has, Delete, Reset, Keys, Len, Services, ServicesLen, typed getters (GetString, GetInt, GetBool, GetFloat64, GetUint, Int8-64, Uint8-64, Uintptr, Float32, Complex64/128)
- Generic: GetState[T], MustGetState[T], GetStateWithDefault[T]
- SharedState (prefork-safe, storage-backed): Set/Get (+WithContext), SetJSON/GetJSON, SetMsgPack/GetMsgPack, SetCBOR/GetCBOR, SetXML/GetXML, etc.; SharedStorage closed after services terminate
- testcontainers contrib module provides ready-made Services

## 12. Logging [https://docs.gofiber.io/api/log]
- fiber/v3/log: levels Trace..Panic; Trace/Debug/Info/Warn/Error/Fatal/Panic, f-variants, w (key-value) variants; global logger; SetLevel; SetOutput; custom Logger interface; WithContext bound context with context tags (requestid etc.), custom context tags

## 13. Testing [https://docs.gofiber.io/api/app]
- app.Test(*http.Request, TestConfig{Timeout (default 1s), FailOnTimeout}): in-memory request with no network

## 14. HTTP client package (fiber/v3/client) [https://docs.gofiber.io/client/rest] [https://docs.gofiber.io/client/request] [https://docs.gofiber.io/client/response]
- Client constructors: New, NewWithClient, NewWithHostClient, NewWithLBClient; default client C(); package-level Get/Post/Put/Patch/Query/Delete/Head/Options/Replace
- Request: methods Get/Post/Put/Patch/Delete/Head/Options/Query/Custom; Acquire/ReleaseRequest; Method, URL, Client, Context
- Headers, Params (query), PathParams (:id substitution), Cookies, UserAgent, Referer, Boundary
- Body: SetJSON, SetXML, SetCBOR, SetRawBody, FormData, files (File, Files, FileByPath, AddFile, AddFileWithReader)
- *WithStruct setters for params, cookies, form, path params
- Timeout, MaxRedirects, RedirectSensitiveHeaders, DisablePathNormalizing
- Client: BaseURL, Debug/DisableDebug, SetLogger, SetDial, SetProxyURL, TLS (SetTLSConfig, SetCertificates, SetRootCertificate(FromString)), StreamResponseBody, MaxResponseBodySize, RetryConfig, Cookie Jar (SetCookieJar, AcquireCookieJar, Get/Set/SetByHost/SetKeyValue), codec setters (JSON/XML/CBOR Marshal/Unmarshal), FasthttpClient/HostClient/LBClient access, Do/DoTimeout/DoDeadline/DoRedirects, CloseIdleConnections, Reset
- Hooks: RequestHook, FinalRequestHook (AddRequestHook, AddFinalRequestHook), ResponseHook (AddResponseHook) [https://docs.gofiber.io/client/hooks]
- Response: Status, StatusCode, Protocol, Header, Headers, Cookies, Body, BodyStream, IsStreaming, String, JSON, XML, CBOR, Save, Close, Reset
- Retry addon (fiber/v3/addon/retry): exponential backoff, InitialInterval (1s), MaxBackoffTime (32s), Multiplier (2.0), MaxRetryCount (10) [https://docs.gofiber.io/addon/retry]

## 15. Adaptor (net/http interop) [https://docs.gofiber.io/middleware/adaptor]
- HTTPHandler, HTTPHandlerWithContext, HTTPHandlerFunc, HTTPMiddleware (net/http -> Fiber)
- FiberHandler, FiberHandlerFunc, FiberApp (Fiber -> net/http)
- ConvertRequest, LocalContextFromHTTPRequest, CopyContextToFiberContext
- Direct registration of net/http handlers on router methods

## 16. Official middleware (github.com/gofiber/fiber/v3/middleware/*)
- adaptor: see section 15 [https://docs.gofiber.io/middleware/adaptor]
- basicauth: HTTP basic auth; Users (hashed passwords: bcrypt, {SHA256}), Realm, Charset (UTF-8), HeaderLimit, Authorizer, Unauthorized, BadRequest, Next [https://docs.gofiber.io/middleware/basicauth]
- cache: response cache; Expiration, CacheHeader, DisableCacheControl, AllowStaleCredentialResponses, CacheInvalidator, KeyGenerator, DisableQueryKeys, KeyHeaders, KeyCookies, KeyCredentialHeaders, Methods, DisableVaryHeaders, ExpirationGenerator, Storage, StoreResponseHeaders, MaxBytes, DisableValueRedaction; honours request Cache-Control (no-cache, no-store, max-age, min-fresh, max-stale, only-if-cached); X-Cache/Age headers [https://docs.gofiber.io/middleware/cache]
- compress: gzip, deflate, brotli, zstd by Accept-Encoding; Level; Next; skips bodies <200B [https://docs.gofiber.io/middleware/compress]
- cors: AllowOrigins (wildcard subdomains), AllowOriginsFunc, AllowMethods, AllowHeaders, AllowCredentials, ExposeHeaders, MaxAge, AllowPrivateNetwork, DisableValueRedaction, Next [https://docs.gofiber.io/middleware/cors]
- csrf: token validation on unsafe methods; CookieName/Domain/Path/Secure/HTTPOnly/SameSite/SessionOnly, IdleTimeout, KeyGenerator, ErrorHandler, Extractor, Session, Storage, TrustedOrigins, SingleUseToken, DisableValueRedaction [https://docs.gofiber.io/middleware/csrf]
- earlydata: TLS 1.3 0-RTT guard; IsEarlyData, AllowEarlyData, Error (425), needs TrustProxy [https://docs.gofiber.io/middleware/earlydata]
- encryptcookie: AES-GCM cookie value encryption; Key (16/24/32 bytes base64), Except, Encryptor, Decryptor, GenerateKey [https://docs.gofiber.io/middleware/encryptcookie]
- envvar: expose selected env vars as JSON; ExportVars [https://docs.gofiber.io/middleware/envvar]
- etag: ETag generation/If-None-Match; Weak [https://docs.gofiber.io/middleware/etag]
- expvar: /debug/vars runtime variables [https://docs.gofiber.io/middleware/expvar]
- favicon: serve/cache or drop /favicon.ico; File, Data, URL, FileSystem, CacheControl, MaxBytes [https://docs.gofiber.io/middleware/favicon]
- healthcheck: liveness/readiness/startup probes (/livez /readyz /startupz constants); Probe func, ResponseFormat (text, JSON, XML, MsgPack, CBOR) [https://docs.gofiber.io/middleware/healthcheck]
- helmet: security headers; XSSProtection, ContentTypeNosniff, XFrameOptions, HSTSMaxAge, HSTSExcludeSubdomains, HSTSPreloadEnabled, ContentSecurityPolicy, CSPReportOnly, ReferrerPolicy, PermissionPolicy, CrossOriginEmbedderPolicy, CrossOriginOpenerPolicy, CrossOriginResourcePolicy, OriginAgentCluster, XDNSPrefetchControl, XDownloadOptions, XPermittedCrossDomain [https://docs.gofiber.io/middleware/helmet]
- hostauthorization: validate Host header against allowlist (DNS rebinding defence); AllowedHosts (wildcard), AllowedHostsFunc, ErrorHandler [https://docs.gofiber.io/middleware/hostauthorization]
- idempotency: Idempotency-Key replay; Lifetime, KeyHeader, KeyHeaderValidate, KeyScope, KeepResponseHeaders, Lock, Storage, DisableValueRedaction; IsMethodSafe/IsMethodIdempotent Next helpers [https://docs.gofiber.io/middleware/idempotency]
- keyauth: API key auth; Validator (required), Extractor, SuccessHandler, ErrorHandler, Realm, Challenge, Error/ErrorDescription/ErrorURI/Scope (RFC 6750 Bearer challenges) [https://docs.gofiber.io/middleware/keyauth]
- limiter: rate limiting; Max, MaxFunc, Expiration, ExpirationFunc, KeyGenerator, LimitReached, SkipFailedRequests, SkipSuccessfulRequests, DisableHeaders (X-RateLimit-*, Retry-After), Storage, LimiterMiddleware (FixedWindow default, SlidingWindow), DisableValueRedaction [https://docs.gofiber.io/middleware/limiter]
- logger: request logging; Format (tags), predefined DefaultFormat/CommonFormat/CombinedFormat/JSONFormat/ECSFormat, TimeFormat, TimeZone, TimeInterval, Stream, Skip, Done, CustomTags, BeforeHandlerFunc, LoggerFunc (zerolog/zap/logrus bridge), DisableColors, ForceColors; tags pid, time, referer, protocol, scheme, port, ip, ips, host, method, path, url, ua, latency, status, resBody, reqHeaders, queryParams, body, bytesSent, bytesReceived, route, error, reqHeader:, respHeader:, query:, form:, cookie:, locals:, colors; auto-registered tags from requestid, basicauth, keyauth, csrf, session; control-character sanitization [https://docs.gofiber.io/middleware/logger]
- paginate: page/offset/cursor pagination from query; PageKey, DefaultPage, LimitKey, DefaultLimit, MaxLimit, SortKey, DefaultSort, AllowedSorts, OffsetKey, CursorKey, CursorParam; PageInfo helpers (Start, SortBy, Next/PreviousPageURL, NextCursorURL, CursorValues, SetNextCursor) [https://docs.gofiber.io/middleware/paginate]
- pprof: /debug/pprof; Prefix [https://docs.gofiber.io/middleware/pprof]
- proxy: Balancer (round-robin Servers, ModifyRequest/Response, Timeout, MaxConnsPerHost, buffer sizes, KeepConnectionHeader, TLSConfig, DialDualStack, Client, SecurityPolicy, MaxResponseBodySize); Forward, Do, DoRedirects, DoDeadline, DoTimeout, DomainForward, BalancerForward, WithClient, WithSecurityPolicy, DefaultSecurityPolicy; SSRF protection, hop-by-hop stripping, Via header, redirect downgrade rules [https://docs.gofiber.io/middleware/proxy]
- recover: panic recovery; PanicHandler, EnableStackTrace, StackTraceHandler [https://docs.gofiber.io/middleware/recover]
- redirect: rule-based redirects; RuleList (ordered, $1 captures), Rules (deprecated map), StatusCode [https://docs.gofiber.io/middleware/redirect]
- requestid: Header, Generator; request ID in response/context [https://docs.gofiber.io/middleware/requestid]
- responsetime: elapsed time into a response header (Go duration string); Header [https://docs.gofiber.io/middleware/responsetime]
- rewrite: path rewrite; RuleList (ordered, $1 captures) [https://docs.gofiber.io/middleware/rewrite]
- session: Middleware pattern (FromContext) and Store pattern; Store, Storage, Extractor, KeyGenerator, IdleTimeout, AbsoluteTimeout, CookieSecure/HTTPOnly/SameSite/Path/Domain/SessionOnly, ErrorHandler; methods Get/Set/Delete/Keys/ID/Fresh/Regenerate/Reset/Destroy/Save/SetIdleTimeout/Release + WithContext variants; Store.GetByID/Delete/Reset/RegisterType (custom types) [https://docs.gofiber.io/middleware/session]
- skip: skip.New(handler, predicate) bypass a handler when predicate true [https://docs.gofiber.io/middleware/skip]
- sse: Server-Sent Events handler; Handler, OnClose, Retry, HeartbeatInterval, DisableHeartbeat; event fields Data, ID, Retry, event name; disconnect detection via Flush errors [https://docs.gofiber.io/middleware/sse]
- static: GET/HEAD file serving; FS (fs.FS, embed.FS, os.DirFS), Compress, ByteRange, Browse (directory listing), Download, AllowSymlinksOutsideRoot, IndexNames, CacheDuration, MaxAge, ModifyResponse, NotFoundHandler, Next; single file and SPA examples [https://docs.gofiber.io/middleware/static]
- timeout: context.WithTimeout wrapper returning 503; Timeout, OnTimeout, Errors (custom timeout errors), Next [https://docs.gofiber.io/middleware/timeout]
- Filesystem middleware replaced by Static; Monitor moved to contrib [https://docs.gofiber.io/whats_new]

## 17. gofiber/contrib (github.com/gofiber/contrib/v3/<name>) [https://github.com/gofiber/contrib]
- jwt: JWT auth; SigningKey, SigningKeys (kid), Claims, Extractor, TokenProcessorFunc, KeyFunc, JWKSetURLs, ParserOptions, KnownCriticalHeaders, Realm, SuccessHandler, ErrorHandler [https://github.com/gofiber/contrib/tree/main/v3/jwt]
- websocket: fasthttp/websocket wrapper with Ctx accessors (Locals, Params, Query, Cookies); HandshakeTimeout, Subprotocols, Origins, AllowEmptyOrigin, Read/WriteBufferSize, WriteBufferPool, EnableCompression, RecoverHandler [https://github.com/gofiber/contrib/tree/main/v3/websocket]
- socketio: Engine.IO v4 / Socket.IO v5 wire-protocol server; PingInterval, PingTimeout, HandshakeTimeout, MaxPayload, MaxAuthPayload, MaxBatchPackets, MaxEventNameLength, MaxEventArgs, CloseTimeout, WriteTimeout, OutboundAckTimeout, SendQueueSize, DropFramesOnOverflow; EmitWithAck [https://github.com/gofiber/contrib/tree/main/v3/socketio]
- otel: OpenTelemetry server spans and HTTP metrics; options WithNext, WithTracerProvider, WithMeterProvider, WithoutMetrics, WithPropagators, WithResponsePropagators, WithTraceResponseHeader, WithPublicEndpoint(Fn), WithSpanStartOptions, WithSpanNameFormatter, WithPort, WithClientIP, WithCapturedRequest/ResponseHeaders, WithRedactedQueryParams, WithCustomAttributes and response/metric variants [https://github.com/gofiber/contrib/tree/main/v3/otel]
- casbin: RBAC/ABAC authorization; ModelFilePath, PolicyAdapter, Enforcer, Lookup, Unauthorized, Forbidden [https://github.com/gofiber/contrib/tree/main/v3/casbin]
- swaggerui: serve Swagger UI from an OpenAPI file/spec (renamed from contrib/swagger) [https://github.com/gofiber/contrib/tree/main/v3/swaggerui]
- swaggo: swaggo/swag generated docs UI (replaces archived gofiber/swagger) [https://github.com/gofiber/contrib/tree/main/v3/swaggo]
- circuitbreaker: circuit breaker; FailureThreshold, Timeout, SuccessThreshold, HalfOpenMaxConcurrent, Interval, IsFailure, Clock, OnOpen/OnHalfOpen/OnClose; GetState, Metrics, HealthHandler, ForceOpen, ForceClose, Reset, SetTimeout [https://github.com/gofiber/contrib/tree/main/v3/circuitbreaker]
- loadshed: CPU-based load shedding; Criteria, OnShed, LowerThreshold, UpperThreshold, Interval, Getter [https://github.com/gofiber/contrib/tree/main/v3/loadshed]
- monitor: real-time dashboard/JSON metrics (process, runtime, system, HTTP); Title, Refresh, APIOnly, EnableGCPauseMetrics, FaviconURL [https://github.com/gofiber/contrib/tree/main/v3/monitor]
- coraza: Coraza WAF; DirectivesFile, RootFS, BlockHandler, ErrorHandler, BlockMessage, LogLevel, RequestBodyAccess, MetricsCollector [https://github.com/gofiber/contrib/tree/main/v3/coraza]
- opa: Open Policy Agent policy checks; RegoQuery, RegoPolicy, IncludeQueryString, IncludeHeaders, DeniedStatusCode, DeniedResponseMessage, InputCreationMethod [https://github.com/gofiber/contrib/tree/main/v3/opa]
- paseto: PASETO token auth; SymmetricKey, PrivateKey, PublicKey, Validate, Extractor, handlers [https://github.com/gofiber/contrib/tree/main/v3/paseto]
- hcaptcha: HCaptcha bot protection; SecretKey, ResponseKeyFunc, SiteVerifyURL, ValidateFunc [https://github.com/gofiber/contrib/tree/main/v3/hcaptcha]
- i18n: go-i18n localization; RootPath, AcceptLanguages, FormatBundleFile, DefaultLanguage, Loader (EmbedLoader), UnmarshalFunc, LangHandler [https://github.com/gofiber/contrib/tree/main/v3/i18n]
- prometheus: request metrics endpoint; ServiceName, Subsystem, MetricsPath, Labels, Registerer, Gatherer, histogram buckets, native histograms, OpenMetrics, exemplars, scrape limits [https://github.com/gofiber/contrib/tree/main/v3/prometheus]
- sentry: Sentry error reporting; Repanic, WaitForDelivery, Timeout [https://github.com/gofiber/contrib/tree/main/v3/sentry]
- newrelic: New Relic APM; License, AppName, Enabled, Application, ErrorStatusCodeHandler, RequestHeaderFilter [https://github.com/gofiber/contrib/tree/main/v3/newrelic]
- zap: zap request logging; Logger, Messages, Levels, SkipURIs, GetResBody, CoreConfigs, ExtraKeys, ZapOptions [https://github.com/gofiber/contrib/tree/main/v3/zap]
- zerolog: zerolog request logging; Logger/GetLogger, Skip, SkipField, SkipHeader, RedactHeader, WrapHeaders, Messages, Levels, GetResBody [https://github.com/gofiber/contrib/tree/main/v3/zerolog]
- spnego: Kerberos/SPNEGO authentication (gokrb5), KeytabLookup [https://github.com/gofiber/contrib/tree/main/v3/spnego]
- uptime: in-process heartbeat history and status page; ServiceID/Name, Endpoints probes, SampleInterval, RetentionDays, Redis Store [https://github.com/gofiber/contrib/tree/main/v3/uptime]
- fgprof: fgprof full-goroutine profiling; Prefix [https://github.com/gofiber/contrib/tree/main/v3/fgprof]
- testcontainers: Testcontainers as a Fiber Service; ServiceKey, Image, Run [https://github.com/gofiber/contrib/tree/main/v3/testcontainers]

## 18. gofiber/storage drivers (Storage interface) [https://github.com/gofiber/storage]
- Storage interface: Get/Set/Delete/Reset/Close plus *WithContext variants; Set takes expiration (0 none); missing key returns nil,nil
- Drivers: aerospike, arangodb, azureblob, badger, bbolt, cassandra, clickhouse, cloudflarekv, coherence, couchbase, dynamodb, etcd, firestore, leveldb, memcache, memory, minio, mockstorage, mongodb, mssql, mysql, nats, neo4j, pebble, postgres, redis, ristretto, rueidis, s3, scylladb, sqlite3, surrealdb, valkey; plus testhelpers [https://github.com/gofiber/storage/tree/main/<name>]
- Used by middleware: cache, limiter, session, csrf, idempotency, SharedState (Config.SharedStorage)
