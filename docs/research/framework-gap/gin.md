# Gin feature inventory

Sources read (all fetched during this task). Abbreviations: D = https://gin-gonic.com/en/docs ; PKG = https://pkg.go.dev/github.com/gin-gonic/gin ; BIND = https://pkg.go.dev/github.com/gin-gonic/gin/binding ; REND = https://pkg.go.dev/github.com/gin-gonic/gin/render ; SRC = https://raw.githubusercontent.com/gin-gonic/gin/master/gin.go ; GC = https://github.com/gin-contrib/<repo> (README read via raw.githubusercontent.com/gin-contrib/<repo>/master/README.md). Gin version on pkg.go.dev: v1.12.0, min Go 1.25.

## 1. Engine construction and options
- gin.New(opts...): bare engine, no middleware [PKG]
- gin.Default(opts...): New plus Logger and Recovery middleware; does not change any engine default [PKG, SRC, D/middleware/without-middleware/]
- OptionFunc (func(*Engine)) and Engine.With(opts...): apply configuration options to an engine; no built-in WithXxx helpers in gin.go [SRC]
- Engine implements http.Handler (ServeHTTP) so it can be passed to a custom http.Server [D/server-config/]
- Engine.Handler(): returns engine as http.Handler (used for h2c) [PKG]
- Engine.Use(middleware...): global middleware [PKG]
- Engine.Routes(): returns RoutesInfo (RouteInfo: method, path, handler) for route listing [PKG]
- Engine.HandleContext(c): re-enters the handler chain for a context (internal redirect after changing c.Request.URL.Path) [PKG, D/routing/redirects/]
- Engine.NoRoute(handlers...) / NoMethod(handlers...): handlers for unmatched path / unmatched method [PKG]
- Engine.SecureJsonPrefix(prefix): prefix for SecureJSON, default `while(1);` [PKG, D/rendering/secure-json/]
- Engine.Delims(left,right): custom HTML template delimiters [PKG, D/rendering/html-rendering/]
- Engine.SetFuncMap(template.FuncMap), SetHTMLTemplate(*template.Template): template functions / prebuilt template [PKG, D/rendering/html-rendering/]
- Engine.LoadHTMLGlob / LoadHTMLFiles / LoadHTMLFS (v1.11+): load templates from glob, file list or http.FileSystem [PKG, D/rendering/html-rendering/]
- Engine.HTMLRender field (render.HTMLRender): plug a custom renderer (e.g. multitemplate) [SRC, D/rendering/multiple-template/]
- Engine.FuncMap field (template.FuncMap, default empty) [SRC]

## 2. Engine settings (fields) [SRC, PKG]
- RedirectTrailingSlash (default true): 301 for GET, 307 other methods, when only the slash-variant has a handler
- RedirectFixedPath (default false): cleans path and does case-insensitive lookup, then redirects
- HandleMethodNotAllowed (default false): 405 (with Allow header) if route exists for another method, else NoRoute [also D/faq/]
- ForwardedByClientIP (default true): ClientIP read from RemoteIPHeaders when remote addr is a trusted proxy, else RemoteAddr
- RemoteIPHeaders (default X-Forwarded-For, X-Real-IP)
- TrustedPlatform (string): trust a platform's client-IP header, takes priority over TrustedProxies; constants PlatformGoogleAppEngine (X-Appengine-Remote-Addr), PlatformCloudflare (CF-Connecting-IP), PlatformFlyIO (Fly-Client-IP), or custom header name [PKG, D/server-config/trusted-proxies/]
- AppEngine (bool): deprecated, use TrustedPlatform
- UseRawPath (default false): match params using url.RawPath (a hint)
- UseEscapedPath (default false, added 1.12): match using url.EscapedPath(), overrides UseRawPath [also release notes https://gin-gonic.com/en/blog/news/gin-1-12-0-release-announcement/]
- UnescapePathValues (default true): unescape path param values when UseRawPath/UseEscapedPath
- RemoveExtraSlash (default false): clean path so params match URLs with extra slashes
- MaxMultipartMemory (default 32<<20): memory limit for ParseMultipartForm; larger parts spill to temp files [D/routing/upload-file/]
- ReadHeaderTimeout (default 10s): read-header timeout for servers made by Run, RunTLS, RunListener
- UseH2C (default false): enable HTTP/2 cleartext through Handler()
- ContextWithFallback (default false): Context.Deadline/Done/Err/Value fall back to Request.Context()
- Engine.SetTrustedProxies([]string): IPv4/IPv6 addresses and CIDRs; default trusts all proxies (documented as not safe); SetTrustedProxies(nil) disables proxy handling [D/server-config/trusted-proxies/, D/security-guide]

## 3. Modes and global functions [PKG, D/deployment/, D/testing/]
- Modes: gin.DebugMode, ReleaseMode, TestMode; gin.SetMode(), gin.Mode(), gin.IsDebugging(); env GIN_MODE (EnvGinMode); release disables debug logging
- PORT env var: default port for Run() with no args (default :8080) [D/deployment/, D/routing/http-method/]
- gin.Version constant [PKG]
- gin.DefaultWriter (default os.Stdout), gin.DefaultErrorWriter (os.Stderr) [PKG]
- gin.DebugPrintFunc, gin.DebugPrintRouteFunc(httpMethod, absolutePath, handlerName, nuHandlers): custom debug and route-registration output [PKG, D/logging/define-format-for-the-log-of-routes/]
- gin.DisableConsoleColor() / ForceConsoleColor(): log coloring (auto by TTY by default) [D/logging/controlling-log-output-coloring/]
- gin.DisableBindValidation(): turn off default validator [PKG]
- gin.EnableJsonDecoderUseNumber(), EnableJsonDecoderDisallowUnknownFields() [PKG; flags binding.EnableDecoderUseNumber / EnableDecoderDisallowUnknownFields in BIND]
- gin.CreateTestContext(w) / CreateTestContextOnly(w, r): test helpers [PKG]
- gin.H: map[string]any shorthand with MarshalXML [PKG]
- gin.HandlerFunc, HandlersChain (Last), IRouter, IRoutes, Skipper type, Negotiate struct, Accounts, Param/Params (ByName, Get), ResponseWriter interface, LogFormatter, LogFormatterParams, LoggerConfig, RecoveryFunc [PKG]
- Constants for keys: AuthUserKey, AuthProxyUserKey, BindKey, BodyBytesKey, ContextKey [PKG]
- MIME constants: MIMEJSON, MIMEHTML, MIMEXML, MIMEXML2, MIMEPlain, MIMEPOSTForm, MIMEMultipartPOSTForm, MIMEYAML, MIMEYAML2, MIMETOML, MIMEPROTOBUF, MIMEBSON [PKG]
- gin.WrapF(http.HandlerFunc) / WrapH(http.Handler): adapt net/http handlers (WrapH used for promhttp in D/server-config/metrics/) [PKG]
- gin.Dir(root, listDirectory) and OnlyFilesFS (exported in 1.11): http.FileSystem that hides directory listings [PKG, 1.11 notes https://gin-gonic.com/en/blog/news/gin-1-11-0-release-announcement/]

## 4. Running the server
- Run(addr...): HTTP server, default :8080 (PORT env) [PKG, D/deployment/]
- RunTLS(addr, certFile, keyFile): HTTPS [PKG, D/server-config/http2-server-push/]
- RunQUIC(addr, cert, key) (v1.11.0, experimental HTTP/3 via quic-go) [PKG, 1.11 notes]
- RunUnix(file), RunFd(fd), RunListener(net.Listener) [PKG]
- Custom http.Server wrapping the engine (Addr, Handler, ReadTimeout, WriteTimeout, MaxHeaderBytes) [D/server-config/custom-http-config/]
- Multiple services in one process: several engines with errgroup.Group (g.Go, g.Wait), per-server http.Server settings [D/server-config/run-multiple-service/]
- Graceful shutdown with stdlib: http.Server.Shutdown(ctx), signal.Notify SIGINT/SIGTERM, context.WithTimeout; third-party endless, manners, graceful, grace listed [D/server-config/graceful-restart-or-stop/]
- Let's Encrypt: github.com/gin-gonic/autotls: autotls.Run(router, domains...), autotls.RunWithManager(router, &autocert.Manager{Prompt, HostPolicy, Cache}); needs ports 80 and 443 reachable [D/server-config/support-lets-encrypt/]
- HTTP/2 server push: c.Writer.Pusher().Push(target, opts); docs note browsers deprecated push (suggests 103 Early Hints, link preload) [D/server-config/http2-server-push/]
- h2c via Engine.UseH2C [SRC]
- Custom JSON codec at runtime: assign json.API (package github.com/gin-gonic/gin/codec/json) before engine starts; implement Marshal, Unmarshal, MarshalIndent, NewEncoder, NewDecoder [D/server-config/custom-json-codec/]
- Build tags: go_json (goccy), jsoniter, sonic avx (ByteDance), nomsgpack (drop MsgPack) [D/build-tags/, /json-replacement/, /nomsgpack/]
- WebSocket: no built-in, upgrade via gorilla/websocket (Upgrader, CheckOrigin, Subprotocols, ping/pong); c.IsWebsocket() [D/server-config/websocket/, PKG]
- Health checks: liveness /healthz, readiness /readyz patterns, Kubernetes probes [D/server-config/health-check/]
- Metrics: gin-contrib/openmetrics (NewOpenMetrics, WithExpose) and prometheus client with c.FullPath() label, gin.WrapH(promhttp.Handler()), separate metrics port [D/server-config/metrics/]
- Database integration patterns (database/sql, GORM, pool settings, transactions) [D/server-config/database/]
- Context and cancellation guidance: c.Request.Context(), timeout middleware pattern, c.Copy() for goroutines [D/server-config/context/]
- Deployment guides: Railway, Seenode, Koyeb, Qovery, Render, Google App Engine, self-hosted [D/deployment/]

## 5. Routing [D/routing/*, PKG]
- HTTP method routes: GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS; Any (all methods); Match(methods, path); Handle(method, path) [PKG, D/routing/http-method/]
- Path params `:name` (one segment), c.Param(), Params.Get/ByName; AddParam for tests [D/routing/param-in-path/]
- Catch-all `*action` (captured value includes leading slash) [D/routing/param-in-path/]
- Route-conflict detection: panic at startup for conflicting patterns [D/routing/param-in-path/]
- Radix tree router (httprouter-based), zero allocations per lookup claimed [D/routing/]
- c.FullPath(): matched route pattern [PKG]
- RouterGroup: Group(prefix, handlers...), nested groups, Use(), BasePath(); middleware scoped to the group [D/routing/grouping-routes/]
- Per-route middleware as extra handler args [D/middleware/using-middleware/]
- Redirects: c.Redirect(301/302/307/308, location); router-level redirect with HandleContext [D/routing/redirects/]
- Trailing-slash and fixed-path redirection via engine settings (section 2)
- API design patterns page: response envelope, limit/offset and cursor pagination, filter/sort allow-lists, URL and header (Accept-Version) versioning, AppError + error middleware, resource route registration functions [D/routing/api-design/]
- Static serving on RouterGroup: Static(rel, root), StaticFS(rel, http.FileSystem), StaticFile(rel, path), StaticFileFS(rel, path, fs) [PKG, D/rendering/serving-static-files/]

## 6. Request input (Context)
- Query: Query, DefaultQuery, GetQuery, QueryArray, GetQueryArray, QueryMap (`ids[a]=1`), GetQueryMap [PKG, D/routing/querystring-param/, /map-as-querystring-or-postform/]
- Form: PostForm, DefaultPostForm, GetPostForm, PostFormArray, GetPostFormArray, PostFormMap, GetPostFormMap (urlencoded and multipart) [PKG, D/routing/multipart-urlencoded-form/, /query-and-post-form/]
- Files: FormFile(name), MultipartForm(), SaveUploadedFile(file, dst, perm...) ; docs warn not to trust file.Filename (use filepath.Base) [PKG, D/routing/upload-file/*]
- Upload size cap: http.MaxBytesReader + ParseMultipartForm, detect *http.MaxBytesError → 413 [D/routing/upload-file/limit-bytes/]
- Headers: GetHeader(key); response c.Header(key,val) (empty removes) [PKG]
- Cookies: Cookie(name), SetCookie(name,value,maxAge,path,domain,secure,httpOnly), SetCookieData(*http.Cookie) (v1.11+, SameSite, Partitioned), SetSameSite [PKG, D/server-config/cookie/]
- ClientIP() (honours trusted proxies/TrustedPlatform), RemoteIP(), ContentType(), IsWebsocket() [PKG, D/server-config/trusted-proxies/]
- GetRawData(): raw body bytes [PKG]
- Context fields: Request, Writer, Params, Keys, Errors, Accepted [PKG]

## 7. Context state and flow
- Set, Get, MustGet, Delete [PKG]
- Typed getters: GetBool, GetString, GetInt, GetInt8/16/32/64, GetUint, GetUint8/16/32/64, GetFloat32/64, GetTime, GetDuration, GetError, GetErrorSlice, GetStringSlice, GetStringMap, GetStringMapString, GetStringMapStringSlice, GetIntSlice and all Int/Uint/Float slice variants [PKG, 1.11/1.12 notes]
- Next(), Abort(), AbortWithStatus, AbortWithError (400 + SetType), AbortWithStatusJSON, AbortWithStatusPureJSON (v1.11+), IsAborted [PKG, D/rendering/pure-json/]
- Copy(): snapshot safe for goroutines (contexts are pooled via sync.Pool) [PKG, D/middleware/goroutines-inside-a-middleware/]
- Handler(), HandlerName(), HandlerNames() [PKG]
- context.Context implementation: Deadline, Done, Err, Value (fallback to Request.Context() when ContextWithFallback) [PKG]
- Errors: c.Error(err) returns *Error; c.Errors collection; Error.SetType/SetMeta/IsType/JSON/MarshalJSON/Unwrap/Error; ErrorType flags (e.g. ErrorTypeBind); c.Errors.Last().Err; gin.ErrorLogger(), ErrorLoggerT(type) middleware [PKG, D/middleware/error-handling-middleware/, /routing/api-design/]

## 8. Binding [BIND, PKG, D/binding/*]
- Binders (vars in package binding): JSON, XML, Form, Query, FormPost, FormMultipart, ProtoBuf, MsgPack, YAML, Uri, Header, Plain, TOML, BSON [BIND]
- binding.Default(method, contentType): picks binder (GET→query/form; POST/PUT by Content-Type) [BIND, D/binding/bind-query-or-post/]
- Bind* (abort 400 text/plain via MustBindWith, SetType(ErrorTypeBind)): Bind, BindJSON, BindXML, BindYAML, BindTOML, BindPlain, BindQuery, BindHeader, BindUri, MustBindWith (BindWith deprecated) [PKG, D/binding/binding-and-validation/]
- ShouldBind* (returns error): ShouldBind, ShouldBindJSON, XML, YAML, TOML, Plain, Query, Header, Uri, ShouldBindWith [PKG]
- ShouldBindBodyWith(obj, binding.JSON) caches body to bind several times; ShouldBindBodyWithJSON/XML/YAML/TOML/Plain [PKG, D/binding/bind-body-into-different-structs/]
- ShouldBindQuery: only query string regardless of method [D/binding/only-bind-query-string/]
- Tags: json, xml, yaml, toml, form (query+form+body), uri, header (case-insensitive names), binding (validator rules), `binding:"-"` skip [D/binding/*]
- Header binding: missing headers leave zero values unless `binding:"required"` [D/binding/bind-header/]
- Uri binding: `uri:"id"` with `binding:"uuid"` etc. [D/binding/bind-uri/]
- form tag `default=` value (scalars, and slices/arrays since 1.11; `;` separators for multi/csv) [D/binding/bind-default-values/]
- collection_format tag (v1.11+): multi (default), csv, ssv, tsv, pipes [D/binding/collection-format-for-arrays/]
- Time: `time_format:"2006-01-02"`, `time_utc:"1"`; unixMilli and unixMicro formats (1.11) [D/binding/bind-query-or-post/, 1.11 notes]
- Checkbox / multi-value `form:"colors[]"` into []string [D/binding/bind-html-checkbox/]
- Nested structs, pointer-to-struct and anonymous struct fields bind from flat names [D/binding/bind-form-data-request-with-custom-struct/]
- Custom unmarshalers: `parser=encoding.TextUnmarshaler` in form/uri tag; binding.BindUnmarshaler (UnmarshalParam(string) error, v1.10), precedence rules [D/binding/bind-custom-unmarshaler/, BIND]
- Custom struct tag binding: implement binding.Binding (Name, Bind) using binding.MapFormWithTag(obj, values, tag) [D/binding/bind-form-data-custom-struct-tag/]
- Interfaces: Binding, BindingBody (BindBody), BindingUri (BindUri), StructValidator (ValidateStruct, Engine), SliceValidationError [BIND]
- binding.Validator: go-playground/validator v10 (tags required, email, min, max, gte, lte, alphanum, uuid, gtfield=, ...) [BIND, D/binding/binding-and-validation/]
- Custom validators: `binding.Validator.Engine().(*validator.Validate).RegisterValidation(name, fn)`; struct-level validation mentioned [D/binding/custom-validators/]
- binding.EnableDecoderUseNumber, EnableDecoderDisallowUnknownFields; errors ErrConvertMapStringSlice, ErrConvertToMapString, ErrMultiFileHeader, ErrMultiFileHeaderLenInvalid [BIND]
- Multipart file binding to *multipart.FileHeader / []*multipart.FileHeader fields (inferred from errors listed in BIND)
- Other Content-Types bound via ShouldBind: protobuf, msgpack (nomsgpack tag removes), BSON (added 1.12), TOML, Plain (BindPlain 1.11) [BIND, 1.11 and 1.12 notes]
- Bind(val) middleware: gin.Bind(val) binds request to a value per request [PKG]
- Binding data sources from encoding.UnmarshalText support in URI and query (1.12) [1.12 notes]

## 9. Rendering and response [PKG, REND, D/rendering/*]
- c.JSON, c.IndentedJSON (dev), c.SecureJSON (array prefix, default `while(1);`, set via SecureJsonPrefix), c.JSONP (callback query param, sanitised), c.AsciiJSON (\uXXXX escaping), c.PureJSON (no HTML escaping)
- c.XML, c.YAML (application/x-yaml), c.TOML (application/toml), c.ProtoBuf (application/x-protobuf), c.BSON (1.12), c.MsgPack (render.MsgPack)
- c.String(code, format, values...), c.Data(code, contentType, bytes), c.Status(code)
- c.HTML(code, name, data) with html/template (auto-escape; template.HTML disables escaping)
- c.Render(code, render.Render): generic renderer. render package types: AsciiJSON, BSON, Data, Delims, HTML, HTMLDebug, HTMLProduction, HTMLRender, IndentedJSON, JSON, JsonpJSON, MsgPack, ProtoBuf, PureJSON, Reader, Redirect, Render, SecureJSON, String, TOML, XML, YAML; functions WriteJSON, WriteMsgPack, WriteString [REND]
- Content negotiation: c.Negotiate(code, gin.Negotiate{Offered, HTMLName, HTMLData, JSONData, XMLData, YAMLData, Data, TOMLData...}), NegotiateFormat(offered...), SetAccepted(formats...); ProtoBuf negotiable since 1.12 [PKG, 1.12 notes]
- Multi-format by query param pattern [D/rendering/rendering/]
- Files: c.File(path), c.FileFromFS(path, http.FileSystem), c.FileAttachment(path, downloadName) (Content-Disposition attachment); path traversal warning [D/rendering/serving-data-from-file/]
- c.DataFromReader(code, contentLength, contentType, reader, extraHeaders) [D/rendering/serving-data-from-reader/]
- c.Stream(step func(w io.Writer) bool) bool, c.SSEvent(name, message) (Server-Sent Events; gin-contrib/sse encoder), c.Writer.Flush() [PKG, D/server-config/context/]
- c.Redirect(code, location) [PKG]
- HTML: LoadHTMLGlob (`templates/**/*` for nested dirs), LoadHTMLFiles, LoadHTMLFS, SetHTMLTemplate, Delims, SetFuncMap, `{{define "path"}}` naming [D/rendering/html-rendering/]
- Embedded templates with //go:embed + template.ParseFS + SetHTMLTemplate; go-assets alternative [D/rendering/bind-single-binary-with-template/]
- Multiple templates via gin-contrib/multitemplate: NewRenderer(), AddFromFiles, assigned to router.HTMLRender [D/rendering/multiple-template/]
- Static embed: StaticFS with http.FS(embed.FS); gin-contrib/static EmbedFolder [D/faq/]
- ResponseWriter: Status(), Size(), Pusher(), Flush() (no panic if writer lacks Flusher since 1.12), Hijack [PKG, D/server-config/http2-server-push/, 1.12 notes]

## 10. Built-in middleware
- gin.Logger(), LoggerWithConfig(LoggerConfig{Formatter, Output, SkipPaths, Skip func(*Context) bool, SkipQueryString}), LoggerWithFormatter(fn), LoggerWithWriter(w, skipPaths...) [PKG, D/logging/*]
- LogFormatterParams: Request, TimeStamp, StatusCode, Latency, ClientIP, Method, Path, ErrorMessage, BodySize, Keys, isTerm; color helpers StatusCodeColor, MethodColor, ResetColor, LatencyColor (colorized latency 1.12), IsOutputColor [PKG, D/logging/custom-log-format/, 1.12 notes]
- Log file output: gin.DefaultWriter = io.MultiWriter(file, os.Stdout); lumberjack rotation suggested [D/logging/write-log/]
- Skip logging by path or predicate; skip query strings [D/logging/skip-logging/, /avoid-logging-query-strings/]
- Structured logging with slog / zerolog middleware patterns, request-ID middleware pattern [D/logging/structured-logging/]
- gin.Recovery(), RecoveryWithWriter(out, recovery...), CustomRecovery(func(c, recovered any)), CustomRecoveryWithWriter; suppresses http.ErrAbortHandler (1.12) [PKG, D/middleware/custom-recovery/]
- gin.BasicAuth(Accounts), BasicAuthForRealm(accounts, realm), BasicAuthForProxy(accounts, realm); gin.AuthUserKey / AuthProxyUserKey [PKG, D/middleware/using-basicauth-middleware/]
- Middleware model: onion via c.Next(); global/group/per-route attach [D/middleware/, /using-middleware/, /custom-middleware/]
- Security headers middleware example (nosniff, X-Frame-Options, CSP, HSTS, Referrer-Policy, Permissions-Policy, Host check); gin-helmet third-party [D/middleware/security-headers/]
- Security guide: CORS, CSRF (gin-contrib/csrf: csrf.Middleware(Options{Secret, ErrorFunc}), csrf.GetToken), rate limiting with x/time/rate, validation, SQL injection, XSS, trusted proxies, autotls [D/middleware/security-guide/]
- Session management guide (gin-contrib/sessions, cookie/redis/memcached/mongo/postgres stores, Options Path/MaxAge/HttpOnly/Secure/SameSite) [D/middleware/session-management/]
- Dependency injection patterns: closure, struct handlers, middleware c.Set [D/middleware/dependency-injection/]

## 11. Testing [D/testing/]
- httptest.NewRecorder + router.ServeHTTP(w, req); gin.SetMode(gin.TestMode) in TestMain; table-driven tests; middleware tests; CreateTestContext helpers [PKG]

## 12. Other docs / FAQ [D/faq/, D/introduction/]
- Live reload with Air (`air init`) [D/faq/]
- JWT: use gin-contrib/jwt or custom middleware with golang-jwt [D/faq/]
- Intro features: fast radix router, middleware, crash-free recovery, JSON validation, route groups, error management, built-in rendering, extensible [D/introduction/]
- Release features: 1.11 HTTP/3 (RunQUIC), BindPlain, collection formats, defaults for collections, OnlyFilesFS exported; 1.12 BSON, GetError/GetErrorSlice, Context.Delete, UseEscapedPath, UnmarshalText in URI/query binding, protobuf negotiate, colorized latency, SkipQueryString, X-Forwarded-For trusted on Unix sockets, Go 1.25 minimum [https://gin-gonic.com/en/blog/news/gin-1-11-0-release-announcement/ , https://gin-gonic.com/en/blog/news/gin-1-12-0-release-announcement/]
- gin-gonic/examples repo directories: ace-template, app-engine, assets-in-binary, auto-tls, basic, conditional-routes, cookie, cors-middleware, custom-validation, file-binding, form-binding, forward-proxy, graceful-shutdown, group-routes, grpc, http-pusher, http2, multiple-service, new_relic, oidc, otel, ratelimiter, realtime-advanced, realtime-chat, reverse-proxy, secure-web-app, send_chunked_data, server-sent-event, struct-lvl-validations, template, upload-file, versioning, websocket [https://api.github.com/repos/gin-gonic/examples/contents]

## 13. gin-contrib organisation (https://github.com/gin-contrib ; org repo list read via https://github.com/gin-contrib and https://api.github.com/orgs/gin-contrib/repos)
- authz: Casbin authorization; authz.NewAuthorizer(enforcer); checks {subject, object, action}; 403 on deny [GC/authz]
- brotli: brotli compression middleware (README gives no options) [GC/brotli]
- cache: response caching; cache.CachePage(store, duration, handler); stores persistence.NewInMemoryStore, NewRedisCacheWithURL; Delete/Flush [GC/cache]
- cors: Config AllowAllOrigins, AllowOrigins, AllowOriginFunc, AllowOriginWithContextFunc, AllowMethods, AllowPrivateNetwork, AllowHeaders, AllowCredentials, ExposeHeaders, MaxAge (12h), AllowWildcard, AllowBrowserExtensions, CustomSchemas, AllowWebSockets, AllowFiles, OptionsResponseStatusCode (204); cors.Default(), DefaultConfig(), New(), AddAllowMethods/Headers, AddExposeHeaders, Validate() [GC/cors]
- expvar: expvar.Handler() mounted at /debug/vars [GC/expvar]
- graceful: graceful shutdown server; options WithAddr, WithTLS, WithUnix, WithFd, WithListener, WithServer, WithShutdownTimeout (30s), WithServerTimeouts (15/30/60s), WithBeforeShutdown, WithAfterShutdown [GC/graceful]
- gzip: gzip.Gzip(level), WithMinLength, WithExcludedExtensions, WithExcludedPaths, WithExcludedPathsRegexs [GC/gzip]
- httpsign: HTTP Signatures middleware; auth.Authenticated(); httpsign.NewAuthenticator(secrets); HmacSha256, HmacSha512, custom Crypto interface; per-KeyID Secrets [GC/httpsign]
- i18n: Localize(), WithBundle(&BundleCfg{RootPath, AcceptLanguage, DefaultLanguage, FallbackLanguages, FormatBundleFile, UnmarshalFunc}), WithGetLngHandle; MustGetMessage, HasLang, GetDefaultLanguage, GetCurrentLanguage; JSON/YAML bundles [GC/i18n]
- location: scheme/host detection (X-Forwarded-Proto/Host); Config Scheme, Host, Base, Headers.Scheme, Headers.Host; Default(), New(), Get(c) *url.URL; v2 module path [GC/location]
- logger: zerolog request logger; SetLogger with WithSkipPath, WithSkipPathRegexps, WithSkipper, WithUTC, WithLogger, WithContext [GC/logger]
- multitemplate: multiple HTML templates; NewRenderer, AddFromFiles; layout+include pattern [GC/multitemplate]
- opengintracing: OpenTracing; NewSpan, InjectToHeaders, SpanFromHeaders [GC/opengintracing]
- pprof: pprof.Register(router[, prefix]) (default debug/pprof), pprof.RouteRegister(group, prefix); heap, profile?seconds, block, trace [GC/pprof]
- requestid: X-Request-ID middleware; requestid.New(), WithGenerator, WithCustomHeaderStrKey, requestid.Get(c) [GC/requestid]
- rollbar: rollbar.Recovery(bool) error reporting via rollbar-go [GC/rollbar]
- secure: security headers; Config AllowedHosts, SSLRedirect, SSLHost, SSLProxyHeaders, IsDevelopment, STSSeconds, STSIncludeSubdomains, FrameDeny, ContentTypeNosniff, BrowserXssFilter, ContentSecurityPolicy, IENoOpen, ReferrerPolicy; DefaultConfig() [GC/secure]
- sentry (archived): sentry.Recovery(client, onlyCrashes) via raven; replaced by sentry-go's own gin middleware [GC/sentry]
- sessions: Sessions(name, store), SessionsMany, SessionsManyStores, Default(c), DefaultMany; Get/Set/Save/Clear/Options; stores: cookie, redis, memcached (ASCII and binary/SASL), mongo (mgo and mongo-driver), memstore, GORM, postgres, filesystem; Options Path/MaxAge/HttpOnly/Secure/SameSite [GC/sessions, D/middleware/session-management/]
- size (archived): limits.RequestSizeLimiter(bytes) → 413 `request too large`, Connection: close; recommends http.MaxBytesReader [GC/size]
- slog: slog request logger; SetLogger, Get(c); options WithLogger, WithContext, WithWriter, WithMessage, WithSkipPath, WithSkipPathRegexps, WithSkipper, WithUTC, WithDefaultLevel, WithClientErrorLevel, WithServerErrorLevel, WithPathLevel, WithSpecificLogLevelByStatusCode, WithRequestHeader, WithHiddenRequestHeaders [GC/slog]
- sse: Server-Sent Events encoder; sse.Encode(w, sse.Event{Id, Event, Data, Retry}), sse.ContentType; used by c.SSEvent [GC/sse]
- static: static.Serve(prefix, fs), static.EmbedFolder(embedFS, path), combine with NoRoute [GC/static]
- timeout: WithTimeout (default 5s), WithResponse (default 408); buffers response, recovers panics [GC/timeout]
- zap: Ginzap(logger, timeFormat, utc), GinzapWithConfig(Config{TimeFormat, UTC, SkipPaths, SkipPathRegexps, Skipper, Context}), RecoveryWithZap(logger, stack), Fn(...) [GC/zap]
- Referenced by gin docs but README not retrievable (raw README 404): csrf (csrf.Middleware, GetToken), openmetrics (NewOpenMetrics, WithExpose), jwt (named in FAQ) [D/middleware/security-guide/, D/server-config/metrics/, D/faq/]
- Org repo list as read from https://github.com/gin-contrib (25 repos per page header): i18n, zap, timeout, static, sse, slog, sessions, secure, rollbar, requestid, pprof, opengintracing, multitemplate, logger, location, httpsign, gzip, graceful, expvar, cors, authz, cache, size (archived), brotli, sentry (archived)
