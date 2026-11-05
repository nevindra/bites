# Feature inventory: Hertz and Fuego

Every bullet comes from a page read in this session; the URL closes each bullet. Notes on gaps are stated where a page did not give detail.

# 1. Hertz (CloudWeGo)

## 1.1 Engine and server options
- server.Default / server.New: Default() includes Recovery middleware, New() has none [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/engine/]
- Spin / Run: Spin() runs with graceful shutdown, Run() without [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/engine/]
- WithHostPorts: listen address, default :8888 [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/engine/]
- WithTransport / WithAltTransport / SetTransporter / GetTransporterName: replace the network transport (netpoll or standard) [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/engine/]
- WithNetwork: tcp, udp or unix domain socket [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/engine/]
- WithKeepAlive / WithKeepAliveTimeout (1 min) / WithIdleTimeout (3 min): keep-alive control [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/engine/]
- WithReadTimeout (3 min) / WithWriteTimeout (0, netpoll only): I/O timeouts [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/engine/]
- WithMaxRequestBodySize (4 MB) / WithMaxKeepBodySize (4 MB) / WithMaxHeaderBytes (1 MB, 431 on excess) / WithReadBufferSize (4 KB): size limits [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/engine/]
- WithRedirectTrailingSlash: redirect between /foo and /foo/ (301 for GET, 307 otherwise) [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/route/]
- WithRedirectFixedPath: repairs unmatched paths, 301 GET / 308 other [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/engine/]
- WithRemoveExtraSlash: let params match paths with extra slashes [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/engine/]
- WithUnescapePathValues / WithUseRawPath: path unescaping, raw-path matching [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/engine/]
- WithHandleMethodNotAllowed: 405 instead of 404 when path exists under other methods [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/engine/]
- WithDisablePreParseMultipartForm: skip multipart preprocessing so body is readable directly [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/engine/]
- WithStreamBody: process request bodies as streams [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/engine/]
- WithExitWaitTime: graceful shutdown window, default 5 s [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/engine/]
- WithTLS / WithALPN / WithListenConfig: server TLS, ALPN, listener settings such as port reuse [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/engine/]
- WithH2C: HTTP/2 cleartext [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/engine/]
- WithTracer / WithTraceLevel: inject tracer, set trace level [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/engine/]
- WithBasePath: base path (must start and end with /) [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/engine/]
- WithGetOnly: accept only GET [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/engine/]
- WithRegistry: service registration config [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/engine/]
- WithAutoReloadRender: auto-reload rendered templates [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/engine/]
- WithDisablePrintRoute: disable debug route printing [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/engine/]
- WithOnAccept / WithOnConnect: connection callbacks [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/engine/]
- WithDisableHeaderNamesNormalizing: no header-name capitalisation normalising [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/engine/]
- SetCustomSignalWaiter: replace the signal handling that triggers shutdown [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/engine/]
- AddProtocol: register a custom protocol server keyed by ALPN string [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/engine/]
- SetClientIPFunc / SetFormValueFunc: customise client IP and FormValue lookup [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/engine/]
- PanicHandler / ContinueHandler (100-continue) / NoRoute / NoMethod: engine-level handlers [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/engine/]
- Routes(): returns route list [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/engine/]
- NoHijackConnPool / HijackConnHandle: hijacked-connection pool control and handler [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/engine/]
- Delims / SetFuncMap / LoadHTMLGlob / LoadHTMLFiles: template engine setup [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/engine/]
- HERTZ_NO_NETPOLL=true env var: disables netpoll [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/network-lib/]
- HERTZ_DISABLE_REQUEST_CONTEXT_POOL=1 env var: disables RequestContext pooling for troubleshooting [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/context/]

## 1.2 Routing
- HTTP method registration: GET, POST, PUT, DELETE, PATCH, HEAD, OPTIONS [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/route/]
- Any: all methods; Handle: any method including custom ones (e.g. "LOAD") [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/route/]
- Handler-name variants: GETEX, POSTEX, PUTEX, DELETEEX, HEADEX, AnyEX, HandleEX; app.GetHandlerName [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/route/]
- Group(prefix, middleware...) and group.Use: prefix groups with scoped middleware [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/route/]
- Static, parametric (:name) and wildcard (*name) routes, read with c.Param; priority static > param > wildcard [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/route/]
- StaticFile / Static / StaticFS (app.FS, GenerateIndexPages): static file serving [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/route/]
- RoutesInfo: Method, Path, Handler, HandlerFunc per route [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/route/]
- NoRoute (404) and NoMethod (405, needs WithHandleMethodNotAllowed) [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/route/]
- Trailing slash redirect, disable with WithRedirectTrailingSlash(false) [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/route/]

## 1.3 RequestContext, request and response API
- Handler signature func(ctx context.Context, c *RequestContext); context.Context is the recommended cross-function carrier [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/context/]
- Get/Set metadata store (Set, Get, Value, MustGet, typed getters GetString/GetInt/GetBool/GetTime, ForEachKey) cleared after request [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/context/request/]
- Copy() for goroutines; Exile() marks context non-recyclable [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/context/]
- Concurrency note: header reads concurrent-read safe since v0.9.7 [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/context/]
- Request URI: Host, FullPath, SetFullPath, Path, Param, URI object with getters/setters [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/context/request/]
- Query: Query, DefaultQuery, GetQuery, QueryArgs; Args object (Set, Add, Del, Peek, PeekAll, Has, VisitAll, ParseBytes, ...) [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/context/request/]
- Method and header access: Method, IsGet/IsHead/IsPost, ContentType, UserAgent, Cookie, GetHeader, IfModifiedSince, RequestHeader.Set/Add/VisitAll [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/context/request/]
- Body: GetRawData/Body, RequestBodyStream (streaming) [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/context/request/]
- Forms: PostForm, DefaultPostForm, GetPostForm, PostArgs, FormValue (query, then post, then multipart), MultipartForm [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/context/request/]
- Files: FormFile, SaveUploadedFile [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/context/request/]
- Handler chain control: Next, Abort, IsAborted, Handlers, Handler, SetHandlers, HandlerName, GetIndex [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/context/request/]
- ClientIP (X-Forwarded-For, X-Real-IP, remote) with SetClientIPFunc [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/context/request/]
- Response status and headers: SetContentType, SetConnectionClose, SetStatusCode/Status, NotFound, NotModified, Redirect, Header, SetCookie (incl. partitioned cookie example) [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/context/response/]
- Abort helpers: AbortWithStatus, AbortWithError, AbortWithMsg, AbortWithStatusJSON [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/context/response/]
- Render helpers: String, JSON, PureJSON, IndentedJSON, XML, ProtoBuf, HTML, Data, Render [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/context/response/]
- Body writing: SetBodyStream, SetBodyString, Write, WriteString, Flush, GetResponse [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/context/response/]
- File responses: File, FileAttachment (Content-Disposition), FileFromFS (root, index, byte-range) [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/context/response/]
- ResponseHeader API: content type/length/encoding/range, trailer, Server, Location, cookie set/visit/delete, DelClientCookie, normalisation controls [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/context/response/]
- Constants package pkg/protocol/consts: HTTP methods, MIME types, status codes, header names, protocol versions [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/constants/]

## 1.4 Binding and validation
- Binding tags: path, form, query, cookie, header, json, raw_body, default; precedence path > form > query > cookie > header > json > raw_body [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/binding-and-validate/]
- required option inside a tag (query:"a,required") [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/binding-and-validate/]
- validate tag via go-playground/validator [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/binding-and-validate/]
- Methods: BindAndValidate, Bind, BindQuery, BindHeader, BindPath, BindForm, BindJSON, BindProtobuf, BindByContentType, Validate [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/binding-and-validate/]
- binding.NewBindConfig via server.WithBindConfig: LooseZeroMode, UseStdJSONUnmarshaler (default is sonic), UseThirdPartyJSONUnmarshaler, MustRegTypeUnmarshal (custom type resolution) [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/binding-and-validate/]
- WithCustomBinder (implement Name, Bind, BindQuery, BindHeader, BindPath, BindForm, BindJSON, BindProtobuf, Validate) [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/binding-and-validate/]
- WithCustomValidatorFunc (v0.10.3+) and RegisterValidation for custom validator tags [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/binding-and-validate/]
- hz api.vd annotation generates a vd tag [https://www.cloudwego.io/docs/hertz/tutorials/toolkit/annotation/]

## 1.5 Middleware (built-in)
- Middleware model: Use at server, group and route level; ordered; c.Next(ctx) for post-handle logic [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/middleware/]
- Early termination: Abort, AbortWithMsg, AbortWithStatus [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/middleware/]
- Client-side middleware: wraps client.Endpoint, Use, may run more than once on redirects [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/middleware/]
- Basic Auth: BasicAuth(accounts) and BasicAuthForRealm(accounts, realm, userKey); defaults realm "Authorization Required", userKey "user" [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/middleware/basic-auth/]
- Recovery: panic recovery, 500 status; WithRecoveryHandler(f) receives ctx, request context, panic value, stack [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/middleware/recovery/]

## 1.6 Protocols and network layer
- Network libs: netpoll (default, Linux/macOS amd64/arm64, LT event model, small requests) and go net (ET, large requests, Windows, TLS) [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/network-lib/]
- Switch transport: server.WithTransport(standard.NewTransporter / netpoll.NewTransporter); client WithDialer [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/network-lib/]
- Windows uses go net automatically; netpoll has no TLS, falls back to go net [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/network-lib/]
- Custom network library extension: network.Conn, network.StreamConn (QUIC), network.Dialer [https://www.cloudwego.io/docs/hertz/tutorials/framework-exten/network-lib/]
- Protocol extension: protocol.Server (byte-stream), protocol.StreamServer (HTTP3-type), ServerFactory, Core; built-in HTTP1/HTTP2/HTTP3 servers listed [https://www.cloudwego.io/docs/hertz/tutorials/framework-exten/protocol/]
- HTTP/1.1: built-in under the "http/1.1" ALPN key [https://www.cloudwego.io/docs/hertz/tutorials/framework-exten/protocol/]
- HTTP/2 (h2 and h2c) via hertz-contrib/http2; server options ReadTimeout, DisableKeepAlive; client options MaxHeaderListSize, AllowHTTP, ReadIdleTimeout, PingTimeout (15s), WriteByteTimeout, StrictMaxConcurrentStreams, DialTimeout, MaxIdleConnDuration, DisableKeepAlive, Dialer, TLSConfig, RetryConfig, WithMaxIdempotentCallAttempts [https://www.cloudwego.io/docs/hertz/tutorials/third-party/protocol/http2/]
- HTTP/3 / QUIC: no usage page exists; protocol list page lists only HTTP2 and WebSocket and notes HTTP/3 docs were removed; hertz-contrib/http3 repository is archived [https://www.cloudwego.io/docs/hertz/tutorials/third-party/protocol/] [https://github.com/hertz-contrib]
- WebSocket: recommended gorilla/websocket through adaptor.HertzHandler; deprecated hertz-contrib/websocket HertzUpgrader (ReadBufferSize, WriteBufferSize, WriteBufferPool, Subprotocols, Error, CheckOrigin, EnableCompression RFC 7692) [https://www.cloudwego.io/docs/hertz/tutorials/third-party/protocol/websocket/]
- SSE server: sse.NewWriter(c), WriteEvent(id, type, data), Close, GetLastEventID, WithSenseClientDisconnection [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/sse/]
- SSE client: sse.AddAcceptMIME, SetLastEventID, NewReader, ForEach, Read, LastEventID; Event struct ID/Type/Data/Retry [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/sse/]
- Streaming: streaming request read (WithStreamBody, RequestBodyStream), streaming response (SetBodyStream, chunked writes), HijackWriter [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/engine/]
- Response writer extension: HijackWriter, ExtWriter (Write, Flush, Finalize), NewChunkedBodyWriter for immediate chunk flush [https://www.cloudwego.io/docs/hertz/tutorials/framework-exten/response_writer/]
- HTTP Trailers example [https://www.cloudwego.io/docs/hertz/tutorials/example/]
- TLS: server.WithTLS, client.WithTLSConfig (standard dialer), mTLS via ClientAuth/ClientCAs, ALPN via NextProtos [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/tls/]
- autotls (Let's Encrypt): NewTlsConfig(domains...), Run, RunWithContext, NewServerWithManagerAndTlsConfig [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/tls/]

## 1.7 Client
- client.NewClient options: WithMaxConnsPerHost, WithMaxIdleConnDuration, WithMaxConnDuration, WithMaxConnWaitTimeout, WithKeepAlive, WithDialTimeout, WithClientReadTimeout, WithWriteTimeout, WithTLSConfig, WithDialer, WithDialFunc, WithResponseBodyStream, WithName, WithNoDefaultUserAgentHeader, WithDisablePathNormalizing, WithConnStateObserve, WithHostClientConfigHook [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/client/]
- Per-request options: WithDialTimeout, WithReadTimeout, WithWriteTimeout, WithRequestTimeout, WithTag, WithSD [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/client/]
- Send: Do, DoRedirects, Get, Post, DoTimeout, DoDeadline [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/client/]
- Request content: SetQueryString, SetFormData, SetMultipartFormData, SetBody, SetFile [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/client/]
- Streaming responses via BodyStream; CloseIdleConnections; GetDialerName [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/client/]
- Client middleware: Use, UseAsLast, TakeOutLastMiddleware [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/client/]
- Retry: WithRetryConfig (WithMaxAttemptTimes, WithInitDelay, WithMaxDelay, WithMaxJitter, WithDelayPolicy); policies CombineDelay, FixedDelayPolicy, BackOffDelayPolicy, RandomDelayPolicy, DefaultDelayPolicy; SetRetryIfFunc; DefaultRetryIf retries idempotent methods and io.EOF [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/retry/]
- Forward proxy: SetProxy, protocol.ProxyURI, type Proxy func [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/proxy/]
- Service discovery on client: sd.Discovery, Resolver interface, WithSD/WithTag, load balancer interface (default WeightedRandom), sd.WithLoadBalanceOptions [https://www.cloudwego.io/docs/hertz/tutorials/framework-exten/service_discovery/]

## 1.8 Render, JSON, adaptors
- Renders: JSON, PureJSON, IndentedJSON, Data, HTML, ProtoBuf, String, XML, custom render.Render (Render, WriteContentType) [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/render/]
- HTML templates: LoadHTMLGlob, LoadHTMLFiles, Delims, SetFuncMap [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/render/]
- JSON library: sonic default, stdlib fallback, -tags stdjson, render.ResetJSONMarshal [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/json/]
- net/http adaptor: NewHertzHTTPHandler, NewHertzHTTPHandlerFunc, GetCompatRequest, CopyToHertzRequest, GetCompatResponseWriter (documented as slower) [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/adaptor/]
- adaptor.HertzHandler: wrap http.Handler / http.HandlerFunc; http.FileServer with http.FS, embed.FS, gorilla/websocket [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/http-adaptor/]

## 1.9 Errors, hooks, shutdown, testing
- Error type: Err, Type, Meta; types Bind, Render, Private, Public, Any; constructors New, Newf, NewPublic, NewPrivate (+f); methods SetType, SetMeta, IsType, JSON; ErrorChain String/Errors/ByType/Last/JSON; c.Error(err) and c.Errors [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/error-handle/]
- Graceful shutdown: OnShutdown callbacks, WithExitWaitTime, Connection:Close on in-flight requests, deregister from registry, SIGTERM/SIGHUP/SIGINT (all graceful from v0.9.6) [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/graceful-shutdown/]
- Hooks: StartHook (OnRun), ShutdownHook (OnShutdown), OnAccept, OnConnect [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/hooks/]
- Unit test (ut package): CreateUtRequestContext, PerformRequest (no network), ResponseRecorder (Code, Body, Flushed, Header, Write, Result) [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/unit-test/]
- Migration helpers: script converting FastHTTP and Gin projects; handler/param mapping notes [https://www.cloudwego.io/docs/hertz/tutorials/service-migration/]

## 1.10 Observability and framework extension
- Logger interfaces in hlog: Logger, FormatLogger, CtxLogger, Control, FullLogger; SetLogger, SetOutput [https://www.cloudwego.io/docs/hertz/tutorials/framework-exten/log/]
- hlog usage: Info/Errorf/CtxTracef, SetLevel (Trace, Debug, Info, Notice, Warn, Error, Fatal), SetSilentMode, MultiWriter output [https://www.cloudwego.io/docs/hertz/tutorials/observability/log/]
- Tracer interface: Start/Finish, WithTracer (several allowed), TraceInfo via c.GetTraceInfo, stats.HTTPStart/HTTPFinish, Record/GetEvent/SendSize/RecvSize/Error/Panicked/Level/SetLevel [https://www.cloudwego.io/docs/hertz/tutorials/observability/tracing/] [https://www.cloudwego.io/docs/hertz/tutorials/framework-exten/monitor/]
- Trace levels: Disabled, Base, Detailed with fine-grained events (header/body read, handler, response write) [https://www.cloudwego.io/docs/hertz/tutorials/observability/instrumentation/]
- Registry interface: Register/Deregister with Info (ServiceName, Addr, Weight, Tags); server.WithRegistry [https://www.cloudwego.io/docs/hertz/tutorials/framework-exten/service_discovery/]
- Prometheus: prometheus.NewServerTracer(addr, path); throughput and latency metrics, tags HTTP method and statusCode [https://www.cloudwego.io/docs/hertz/tutorials/third-party/prometheus/]
- OpenTelemetry (obs-opentelemetry): tracing, metrics (rate, errors, duration, topology, Go runtime), logrus-based trace-correlated logger, provider options (WithServiceName, WithDeploymentEnvironment, WithServiceNamespace, WithResourceAttributes, WithEnableTracing, WithEnableMetrics, WithHeaders, WithInsecure, WithEnableCompression, WithSampler, ...), tracing options (WithTextMapPropagator, WithCustomResponseHandler, WithClientHttpRouteFormatter, WithServerHttpRouteFormatter, WithShouldIgnore), compile-time instrumentation [https://www.cloudwego.io/docs/hertz/tutorials/third-party/open-telemetry/]
- Sentinel via hertz-contrib/opensergo: SentinelServerMiddleware (429 default), SentinelClientMiddleware, WithServerResourceExtractor, WithServerBlockFallback, WithClientResourceExtractor, WithClientBlockFallback [https://www.cloudwego.io/docs/hertz/tutorials/third-party/sentinel/]
- Reverse proxy (hertz-contrib/reverseproxy): NewSingleHostReverseProxy, SetDirector, SetClient, SetModifyResponse, SetErrorHandler (default 502); WebSocket reverse proxy NewWSReverseProxy with WithDirector/WithDialer/WithUpgrader [https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/proxy/]

## 1.11 Third-party middleware (hertz-contrib)
- CORS: AllowAllOrigins, AllowOrigins, AllowOriginFunc, AllowMethods, AllowHeaders, AllowCredentials, ExposeHeaders, MaxAge, AllowWildcard, AllowBrowserExtensions, AllowWebSockets, AllowFiles [https://www.cloudwego.io/docs/hertz/tutorials/third-party/middleware/cors/]
- JWT: login/logout/refresh handlers; Realm, SigningAlgorithm, Key, KeyFunc, Timeout, MaxRefresh, Authenticator, Authorizator, PayloadFunc, Unauthorized, LoginResponse, LogoutResponse, RefreshResponse, IdentityHandler, IdentityKey, TokenLookup, TokenHeadName, WithoutDefaultTokenHeadName, TimeFunc, HTTPStatusMessageFunc, SendCookie, CookieMaxAge, SecureCookie, CookieHTTPOnly, CookieDomain, SendAuthorization, DisabledAbort, CookieName, CookieSameSite, ParseOptions [https://www.cloudwego.io/docs/hertz/tutorials/third-party/middleware/jwt/]
- Gzip: levels BestCompression/BestSpeed/DefaultCompression/NoCompression; WithExcludedExtensions, WithExcludedPaths, WithExcludedPathRegexes; Gzip Stream for chunked responses [https://www.cloudwego.io/docs/hertz/tutorials/third-party/middleware/gzip/]
- i18n: Localize, MustGetMessage, WithBundle (BundleCfg: DefaultLanguage, FormatBundleFile, AcceptLanguage, RootPath, UnmarshalFunc, Loader), WithGetLangHandle [https://www.cloudwego.io/docs/hertz/tutorials/third-party/middleware/i18n/]
- Sessions: cookie store, Redis store; sessions.New, Many, Default, DefaultMany; Get/Set/Delete/Clear/AddFlash/Flashes/Options/Save/ID [https://www.cloudwego.io/docs/hertz/tutorials/third-party/middleware/session/]
- Pprof: Register(h, prefix), RouteRegister(group, prefix); heap, CPU, block, trace; seconds query param [https://www.cloudwego.io/docs/hertz/tutorials/third-party/middleware/pprof/]
- KeyAuth: WithFilter, WithValidator, WithSuccessHandler, WithErrorHandler, WithKeyLookUp (cookie, header, param, query, form), WithContextKey [https://www.cloudwego.io/docs/hertz/tutorials/third-party/middleware/keyauth/]
- Swagger: swag generator plus adaptor (recommended), legacy hertz-contrib/swagger (deprecated) with URL, DocExpansion, DeepLinking, DefaultModelsExpandDepth, PersistAuthorization, Oauth2DefaultClientID; swag init options parseInternal, parseDependency, parseDepth, instanceName [https://www.cloudwego.io/docs/hertz/tutorials/third-party/middleware/swagger/]
- Request ID: X-Request-ID, WithGenerator, WithCustomHeaderStrKey, WithHandler, Get [https://www.cloudwego.io/docs/hertz/tutorials/third-party/middleware/requestid/]
- Access log: WithFormat, WithTimeFormat, WithTimeInterval, WithAccessLogFunc, WithTimeZoneLocation, WithLogConditionFunc; tags pid, port, protocol, time, latency, ip, ips, ua, referer, host, method, path, url, route, queryParams, reqHeaders, body, bytesReceived, status, resBody, resHeaders, bytesSent; accesslog.Tags custom [https://www.cloudwego.io/docs/hertz/tutorials/third-party/middleware/access-log/]
- Secure: WithSSLRedirect, WithIsDevelopment, WithSTSSecond, WithFrameDeny, WithContentTypeNosniff, WithBrowserXssFilter, WithContentSecurityPolicy, WithIENoOpen, WIthSSLProxyHeaders, WithAllowedHosts, WithSSLTemporaryRedirect, WithSSLHost, WithSTSIncludeSubdomains, WithCustomFrameOptionsValue, WithReferrerPolicy, WithBadHostHandler, WithFeaturePolicy, WithDontRedirectIPV4Hostnames [https://www.cloudwego.io/docs/hertz/tutorials/third-party/middleware/secure/]
- Sentry: hertzsentry options rePanic, waitForDelivery, sendRequest, sendBody, timeout; GetHubFromContext; Flush [https://www.cloudwego.io/docs/hertz/tutorials/third-party/middleware/sentry/]
- CSRF: WithSecret, WithIgnoredMethods, WithKeyLookUp, WithErrorFunc, WithNext, WithExtractor; csrf.GetToken [https://www.cloudwego.io/docs/hertz/tutorials/third-party/middleware/csrf/]
- Casbin: ACL/RBAC/ABAC; NewCasbinMiddleware, NewCasbinMiddlewareFromEnforcer, RequiresPermissions, RequiresRoles; WithLogic (AND/OR/CUSTOM), WithPermissionParser, WithPermissionParserSeparator, WithUnauthorized, WithForbidden [https://www.cloudwego.io/docs/hertz/tutorials/third-party/middleware/casbin/]
- ETag: WithWeak, WithNext, WithGenerator [https://www.cloudwego.io/docs/hertz/tutorials/third-party/middleware/etag/]
- Cache: memory and Redis stores; NewCacheByRequestURI, NewCacheByRequestPath, NewCache, NewCacheByRequestURIWithIgnoreQueryOrder; WithOnHitCache, WithOnMissCache, WithBeforeReplyWithCache, WithOnShareSingleFlight, WithSingleFlightForgetTimeout, WithPrefixKey, WithoutHeader, WithCacheStrategyByRequest [https://www.cloudwego.io/docs/hertz/tutorials/third-party/middleware/cache/]
- Paseto: v1 to v4, local and public; DefaultGenTokenFunc; WithNext, WithErrorFunc, WithSuccessHandler, WithKeyLookUp, WithTokenPrefix, WithParseFunc [https://www.cloudwego.io/docs/hertz/tutorials/third-party/middleware/paseto/]
- Note: the middleware index lists exactly these 17; no limiter, loadbalance or opensergo middleware page exists in it (opensergo appears only as the Sentinel integration) [https://www.cloudwego.io/docs/hertz/tutorials/third-party/middleware/]

## 1.12 Service registry and logger integrations
- Registry backends in hertz-contrib/registry: nacos, consul, etcd, eureka, polaris, servicecomb, zookeeper, redis [https://www.cloudwego.io/docs/hertz/tutorials/third-party/service_discovery/]
- Nacos: NewDefaultNacosRegistry/NewNacosRegistry/NewDefaultNacosResolver/NewNacosResolver, WithRegistryCluster, WithRegistryGroup, v1 and v2 SDK modules [https://www.cloudwego.io/docs/hertz/tutorials/third-party/service_discovery/nacos/]
- Logger adapters with lumberjack rotation: logrus, zap, zerolog, slog [https://www.cloudwego.io/docs/hertz/tutorials/third-party/log/]
- zap adapter options: WithCoreEnc, WithCoreWs, WithCoreLevel, WithCores, WithZapOptions, WithExtraKeys, WithExtraKeyAsStr [https://www.cloudwego.io/docs/hertz/tutorials/third-party/log/zap/]
- slog adapter options: WithOutput, WithLevel, WithHandlerOptions (JSON handler only) [https://www.cloudwego.io/docs/hertz/tutorials/third-party/log/slog/]

## 1.13 hz code generator
- Commands: new, update, model, client, help; global flags --verbose, --help, --version [https://www.cloudwego.io/docs/hertz/tutorials/toolkit/command/]
- IDL input: Thrift and Protobuf via --idl, with --thriftgo/-t, --protoc/-p, --proto_path/-I [https://www.cloudwego.io/docs/hertz/tutorials/toolkit/command/]
- Layout flags: --module, --out_dir, --handler_dir, --model_dir, --router_dir, --client_dir, --use, --service [https://www.cloudwego.io/docs/hertz/tutorials/toolkit/command/]
- Generation flags: --option_package, --trim_gopackage, --no_recurse, --force, --enable_extends, --sort_router, --json_enumstr, --unset_omitempty, --pb_camel_json_tag, --snake_tag, --rm_tag, --exclude_file, --handler_by_method [https://www.cloudwego.io/docs/hertz/tutorials/toolkit/command/]
- Customisation flags: --customize_layout, --customize_layout_data_path, --customize_package, --protoc-plugins, --thrift-plugins [https://www.cloudwego.io/docs/hertz/tutorials/toolkit/command/]
- Client flags: --base_domain, --force_client_dir, --force_client, --enable_optional, --query_enumint [https://www.cloudwego.io/docs/hertz/tutorials/toolkit/command/]
- Field annotations: api.raw_body, api.query, api.header, api.cookie, api.body, api.path, api.form, api.go_tag / go.tag, api.vd, api.none [https://www.cloudwego.io/docs/hertz/tutorials/toolkit/annotation/]
- Method annotations: api.get, api.post, api.put, api.delete, api.patch, api.options, api.head, api.any; client annotation api.base_domain [https://www.cloudwego.io/docs/hertz/tutorials/toolkit/annotation/]
- Generated layout: biz/handler, biz/model, biz/router (register.go, middleware.go per group), router.go, router_gen.go, main.go, .hz, build.sh, script/bootstrap.sh; client: hertz_client.go [https://www.cloudwego.io/docs/hertz/tutorials/toolkit/layout/]
- More features: custom templates, access plugin for third-party generators, client code generation, api.none usage [https://www.cloudwego.io/docs/hertz/tutorials/toolkit/more-feature/]

# 2. Fuego (go-fuego)

## 2.1 Core and server
- OpenAPI from code: spec generated from Go types, not comments or YAML [https://github.com/go-fuego/fuego]
- net/http based: any http.Handler middleware works; supports slog, context, html/template; routing on Go 1.22 mux (Go 1.22+ required) [https://github.com/go-fuego/fuego] [https://go-fuego.dev/docs/]
- fuego.NewServer / Run: server creation and start [https://go-fuego.dev/docs/]
- NewEngine: engine without a server, for use with Gin or Echo [https://go-fuego.dev/docs/guides/alternative-routers-support/gin]
- Server options: WithAddr, WithListener, WithBasePath, WithTimeouts (ServerTimeouts), WithMaxBodySize, WithStripTrailingSlash, WithEngineOptions, WithRouteOptions [https://pkg.go.dev/github.com/go-fuego/fuego] [https://go-fuego.dev/docs/guides/options]
- WithGlobalMiddlewares: runs on every request including unmatched routes [https://go-fuego.dev/docs/guides/middlewares]
- WithLogHandler (slog.Handler), WithoutLogger, WithLoggingMiddleware (LoggingConfig: toggles request/response logging, custom request ID func), WithoutStartupMessages [https://pkg.go.dev/github.com/go-fuego/fuego]
- WithValidator: supply your own go-playground validator [https://pkg.go.dev/github.com/go-fuego/fuego]
- WithDisallowUnknownFields: reject unknown JSON fields [https://pkg.go.dev/github.com/go-fuego/fuego]
- WithAutoAuth(verifyUserInfo), WithSecurity(schemes): built-in auth wiring and OpenAPI security schemes [https://pkg.go.dev/github.com/go-fuego/fuego]
- WithSerializer / WithErrorSerializer: replace response and error serialisers; s.Serialize override [https://pkg.go.dev/github.com/go-fuego/fuego] [https://go-fuego.dev/docs/guides/serialization]
- WithTemplateFS, WithTemplateGlobs, WithTemplates: template sources for rendering [https://pkg.go.dev/github.com/go-fuego/fuego]
- WithoutAutoGroupTags: turn off automatic tagging by return type/group [https://pkg.go.dev/github.com/go-fuego/fuego] [https://github.com/go-fuego/fuego]
- Engine options: WithErrorHandler, DisableErrorHandler, WithMiddlewareConfig, WithOpenAPIConfig, WithOpenAPIGeneratorSchemaCustomizer, WithRequestContentType, WithResponseContentType (defaults JSON and XML), WithoutFlowHeaders [https://pkg.go.dev/github.com/go-fuego/fuego]
- Deprecated options listed: WithCorsMiddleware, WithGlobalResponseTypes, WithXML [https://pkg.go.dev/github.com/go-fuego/fuego]
- Hot reload with air (.air.toml) [https://go-fuego.dev/docs/tutorials/hot-reload]
- Code generator: fuego controller books, --with-service for in-memory store [https://go-fuego.dev/docs/tutorials/crud]
- Architecture: Engine (OpenAPI, error handler), Server (routes, groups, middleware), Adaptors, Context [https://go-fuego.dev/docs/internals/architecture]

## 2.2 Routing and controllers
- Method functions: Get, Post, Put, Patch, Delete, Options, Head, All [https://go-fuego.dev/docs/guides/routing]
- Std variants: GetStd, PostStd, etc. for plain net/http handlers, declared automatically [https://go-fuego.dev/docs/guides/routing] [https://pkg.go.dev/github.com/go-fuego/fuego]
- Handle, NewRoute, Registers for low-level registration [https://pkg.go.dev/github.com/go-fuego/fuego]
- fuego.Group(s, prefix, options...) with nesting; options inherited [https://go-fuego.dev/docs/guides/routing]
- Path params {name} via c.PathParam; wildcard {param...} [https://go-fuego.dev/docs/guides/routing]
- Typed controllers: func(fuego.ContextWithBody[T]) (Response, error), ContextNoBody, []byte raw body receiver [https://go-fuego.dev/docs/guides/controllers]
- Context methods: Body, MustBody, Params, MustParams, PathParam(Int), QueryParam, QueryParamInt, QueryParamBool, QueryParamArr, QueryParams, Header, SetHeader, Cookie, SetCookie, SetStatus, Redirect, Render, Context, Request, Response, MainLang, MainLocale [https://pkg.go.dev/github.com/go-fuego/fuego]
- ContextWithParams and typed params types; ContextFlowable [https://pkg.go.dev/github.com/go-fuego/fuego] [https://go-fuego.dev/docs/guides/controllers]
- Request body formats by Content-Type: JSON, XML, YAML, form-urlencoded, multipart/form-data, octet-stream [https://go-fuego.dev/docs/guides/controllers]
- Transformation: InTransformer (after deserialisation, can reject) and OutTransformer (before serialisation, masking and derived fields); not recursive [https://go-fuego.dev/docs/guides/transformation]

## 2.3 Validation
- Struct tags via go-playground/validator: validate:"required,gte=0,lte=130,email" [https://go-fuego.dev/docs/guides/validation]
- Custom validation via InTransform returning error [https://go-fuego.dev/docs/guides/validation]

## 2.4 Serialisation and content negotiation
- Accept-header negotiation: JSON (default), XML, YAML, HTML, plain text [https://go-fuego.dev/docs/guides/serialization]
- Bypass: write to the ResponseWriter or fuego.SendJSON; return nil, nil [https://go-fuego.dev/docs/guides/serialization]
- WithContentTypeSerDes: custom SerDes (Serialize, Deserialize) per media type, route/group/server scoped [https://go-fuego.dev/docs/guides/serialization]
- DataOrHTML / DataOrTemplate: data for JSON, rendered HTML for text/html [https://go-fuego.dev/docs/guides/serialization] [https://pkg.go.dev/github.com/go-fuego/fuego]
- H (map[string]any) and HTML marker type [https://pkg.go.dev/github.com/go-fuego/fuego]

## 2.5 OpenAPI and route options
- Option package: Summary, Description, OverrideDescription, Tags, TagInfo, Deprecated, Hide, Show, OperationID, StripTrailingSlash, Middleware, Group [https://pkg.go.dev/github.com/go-fuego/fuego] [https://go-fuego.dev/docs/guides/openapi]
- Parameters: Query, QueryInt, QueryBool, QueryArray, Header, Cookie, Path (option.* / OptionQuery etc.) [https://pkg.go.dev/github.com/go-fuego/fuego] [https://go-fuego.dev/docs/guides/options]
- param modifiers: Required, Default, Example (also ParamDescription, ParamDefault in mock tests) [https://go-fuego.dev/docs/guides/options] [https://go-fuego.dev/docs/guides/testing]
- Responses: AddResponse, DefaultResponse, DefaultStatusCode, ResponseHeader, AddError (deprecated) [https://pkg.go.dev/github.com/go-fuego/fuego]
- Request: RequestBody, RequestContentType [https://pkg.go.dev/github.com/go-fuego/fuego]
- Security: option.Security per route [https://pkg.go.dev/github.com/go-fuego/fuego]
- Custom option: func(r *fuego.BaseRoute) with direct route access [https://go-fuego.dev/docs/guides/options]
- OpenAPIConfig: Disabled, DisableSwaggerUI, DisableLocalSave, DisableMessages, PrettyFormatJSON, SwaggerURL (/swagger), SpecURL (/swagger/openapi.json), JSONFilePath (doc/openapi.json), UIHandler [https://go-fuego.dev/docs/guides/openapi]
- UI choices: Stoplight Elements (default), Swagger UI (offline, +10 MB), none, custom [https://go-fuego.dev/docs/guides/openapi]
- s.OutputOpenAPISpec(): generate spec without running server [https://go-fuego.dev/docs/guides/openapi]

## 2.6 Middleware
- net/http middleware (chi, gorilla, rs/cors, chi Compress) at route (option.Middleware), group, server (fuego.Use) and global levels [https://go-fuego.dev/docs/guides/middlewares] [https://github.com/go-fuego/fuego]
- No built-in middlewares described on the middleware page [https://go-fuego.dev/docs/guides/middlewares]

## 2.7 Errors (RFC 9457)
- Error types: BadRequestError 400, UnauthorizedError 401, ForbiddenError 403, NotFoundError 404, NotAcceptableError 406, ConflictError 409, InternalServerError 500, HTTPError (Title, Detail, Status; JSON or XML by Accept) [https://go-fuego.dev/docs/guides/errors]
- Interfaces: ErrorWithStatus, ErrorWithDetail, ErrorWithTitle, ErrorItem, ErrorSender; PathParamInvalidTypeError, PathParamNotFoundError [https://go-fuego.dev/docs/guides/errors] [https://pkg.go.dev/github.com/go-fuego/fuego]
- Err field wrapping; WithErrorHandler chaining; unserialisable errors become logged 500 [https://go-fuego.dev/docs/guides/errors]

## 2.8 Rendering
- html/template: fuego.HTML / CtxRenderer, c.Render(name, data), partials [https://go-fuego.dev/docs/tutorials/rendering/std]
- Templ: fuego.Templ alias of CtxRenderer (Render(ctx, w)) [https://go-fuego.dev/docs/tutorials/rendering/templ]
- Gomponents: fuego.Gomponent alias of Renderer (Render(w)) [https://go-fuego.dev/docs/tutorials/rendering/gomponents]

## 2.9 Adaptors
- Gin: fuegogin.Get/Post..., fuegogin.GetGin wrapping existing routes, incremental migration [https://go-fuego.dev/docs/guides/alternative-routers-support/gin]
- Echo: fuegoecho.Get/Post..., fuegoecho.GetEcho [https://go-fuego.dev/docs/guides/alternative-routers-support/echo]

## 2.10 Testing
- MockContext, NewMockContext(body), NewMockContextNoBody, SetQueryParamInt, WithQueryParamInt, SetQueryParam, SetQueryParamBool, SetRequest [https://go-fuego.dev/docs/guides/testing] [https://pkg.go.dev/github.com/go-fuego/fuego]
- Recommended: table-driven, interface mocking, fuzz tests [https://go-fuego.dev/docs/guides/testing]
