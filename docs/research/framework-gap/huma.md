# Huma v2 feature inventory

Sources read: huma.rocks pages (features, how-to, tutorial, why) and pkg.go.dev for huma/v2 and its subpackages (v2.40.0 shown). Short URL keys: R = https://huma.rocks, P = https://pkg.go.dev/github.com/danielgtaylor/huma/v2. Note: the pkg.go.dev root page was read in three 100k-char slices; Config.CreateHooks, ContentTypeFilter and per-method Context docs were not in the text returned, so only names are listed where noted.

## 1. Overview and goals
- Declarative layer over a chosen router: operation/model docs, path/query/header params, bodies, responses, errors, response headers [R/features/]
- Incremental adoption: bring your own router, middleware, logging/metrics; document existing routes [R/features/]
- Guard rails, docs that stay current, generated tooling (mock servers API Sprout/Prism, SDK generators OpenAPI Generator/oapi-codegen, Restish CLI) [R/features/]
- Skeleton-first workflow: register operations without handler implementations so API can be reviewed first [R/why/what-about-design-first/]
- Go 1.26+ required per install page [R/tutorial/installation/]
- v1 migration: humachi.NewV4 for chi v4, huma.Register, handler signature, return errors/outputs; GraphQL from v1 removed (suggests openapi-to-graphql) [R/how-to/migrate/]

## 2. Router adapters (Bring your own router)
- humabunrouter: BunRouter [R/features/bring-your-own-router/]
- humachi: chi (`humachi.New(router, config)`); humachi.NewV4 for chi v4 [R/features/bring-your-own-router/]
- humaecho: Echo [R/features/bring-your-own-router/]
- humafiber: Fiber [R/features/bring-your-own-router/]
- humagin: gin [R/features/bring-your-own-router/]
- humago: Go 1.22+ http.ServeMux (needs go 1.22+ in go.mod) [R/features/bring-your-own-router/]
- humamux: gorilla/mux [R/features/bring-your-own-router/]
- humahttprouter: httprouter [R/features/bring-your-own-router/]
- humaflow: flow router adapter; New, NewAdapter, NewContext, NewWithPrefix(m, prefix, config), Unwrap, Mux interface (HandleFunc + ServeHTTP), MultipartMaxMemory var (8 KB default) [P/adapters/humaflow, P index]
- huma.Adapter interface (Handle(op, handler), ServeHTTP(w, r)): implement to support any router; adapters convert router request into router-agnostic huma.Context [R/features/bring-your-own-router/, P]
- huma.NewAPI(config, adapter): creates API (called by adapters) [R/features/bring-your-own-router/, P]
- Route groups and base URLs: set config.Servers to include group prefix so OpenAPI, docs, schemas, openapi.yaml are served/prefixed correctly; three scenarios (no prefix, server base /api with gateway stripping, server base /api with route group /api) [R/features/bring-your-own-router/]
- Per-adapter Unwrap(ctx) returns underlying router request/response (humachi.Unwrap, humago.Unwrap, humaflow.Unwrap) [R/features/middleware/]
- Fiber: decodes compressed request bodies by default; with StreamRequestBody enabled raw stream passes undecoded [R/features/request-limits/]
- PRs for new built-in adapters accepted [R/features/bring-your-own-router/]

## 3. Operations
- huma.Register(api, huma.Operation{...}, handler) [R/features/operations/]
- Convenience: huma.Get, Post, Put, Patch, Delete (and Head per pkg index); generate OperationID and summary from method+path (e.g. `get-things-by-thing-id`, "Get things by id") [R/features/operations/, P]
- Overridable generators: huma.GenerateOperationID(method, path, response), huma.GenerateSummary(method, path, response) [R/features/operations/]
- OperationID: unique, case-sensitive; spaces become hyphens; drives Restish command names and SDK method names [R/features/operations/, P]
- Handler signature `func(context.Context, *Input) (*Output, error)`; inputs/outputs are always structs; `*struct{}` for none; `nil, nil` with `*struct{}` output gives 204 [R/features/operations/]
- Request flow: inputs -> unmarshal -> validate -> resolve -> handler -> transform -> marshal -> outputs; raw body input bypasses unmarshal/validate/resolve; raw body output bypasses transform/marshal [R/features/operations/]
- huma.AutoRegister: calls all `Register...` methods on a value [P]
- huma.OperationTags(...): sets operation tags [P]
- Markdown (CommonMark) operation descriptions [R/tutorial/your-first-api/, P]

### Operation struct fields (pkg.go.dev) [P]
- Method: HTTP method
- Path: URL path
- DefaultStatus (int): default status; 200 or 204 depending on body
- MaxBodyBytes (int64): default 1MB, -1 unlimited, 413 if exceeded, multipart has no default
- BodyReadTimeout (time.Duration): default 5s, -1 unlimited, 408 on timeout, overrides server read timeout, multipart no default
- Errors ([]int): status codes handler may return; each expands to a response using the huma.NewError() schema
- SkipValidateParams (bool): disables path/query/header validation
- SkipValidateBody (bool): disables request body validation
- RejectUnknownQueryParameters (bool): per-operation reject unknown query params
- Hidden (bool): omit from OpenAPI doc; schemas only used by it also omitted from components but kept for validation
- Metadata (map[string]any): arbitrary data, e.g. for operation-generating functions (autopatch reads "autopatch": false)
- Middlewares (Middlewares): per-operation middleware
- Tags ([]string)
- Summary
- Description (CommonMark)
- ExternalDocs (*ExternalDocs)
- OperationID
- Parameters ([]*Param): operation-level override of path-level params
- RequestBody (*RequestBody): custom request body definition (media type -> *MediaType with Schema)
- Responses (map[string]*Response): keyed by status code
- Callbacks (map[string]map[string]*PathItem)
- Deprecated (bool)
- Security ([]map[string][]string): overrides top-level; empty array removes
- Servers ([]*Server): alternative servers for operation
- Extensions (map[string]any): x- properties, marshaled as siblings
- Operation.MarshalJSON method

## 4. Request inputs [R/features/request-inputs/]
- Struct tags `path:"name"`, `query:"name"`, `header:"name"`, `cookie:"name"`
- `required:"true"` for cookie/header/query (discouraged); params optional by default; path always required; body object fields required unless `omitempty`/`omitzero`
- Param types: bool, [u]int and 8/16/32/64 variants, float32/64, string, time.Time, slices (comma-separated `1,2,3`)
- `query:"tags,explode"`: repeated params `?tags=a&tags=b` (comma-separated form faster/recommended)
- Cookie param can be typed http.Cookie for full cookie
- Custom wrapper types: ParamWrapper (Receiver() reflect.Value) and optional ParamReactor (OnParamSet(isSet bool, parsed any)); wrapper provides Schema(r Registry); example OptionalParam[T]
- Body field: any type, struct or slice; pointer Body = optional; doc/validation tags allowed on Body
- Body tags: `contentType`, `nameHint`, `required:"true"`
- RawBody []byte alongside Body: bytes used for validation/parsing; without Body: raw bytes, no parsing/validation; with `contentType:"text/plain"` accepts plain text
- Special body types: time.Time (date-time), url.URL (uri), net.IP (ipv4), netip.Addr (ip), json.RawMessage (any), uuid.UUID (uuid; Go 1.27+); overridable with schema customization / format tag
- Multipart: `RawBody multipart.Form` (RawBody.File, RawBody.Value)
- huma.MultipartFormFiles[T] as RawBody: `.Form` (*multipart.Form), `.Data()` (*T), `.Decode(opMediaType, formValueParser)`
- huma.FormFile (embeds multipart.File; fields ContentType, IsSet, Size, Filename); []huma.FormFile for multiple files
- Form tags: `form:"name"`, `contentType` (default application/octet-stream), `required:"true"` on files; non-file fields validated with tags; `contentType:"application/json"` on non-file fields unmarshals+validates JSON incl. structs and slices
- Input composition via embedded structs (AuthParam, PaginationParams example)
- Param struct (OpenAPI parameter): Ref, Name, In, Description, Required, Deprecated, AllowEmptyValue, Style (default form for query/cookie, simple for path/header), Explode (*bool), AllowReserved, Schema, Example, Examples, Extensions [P]
- huma.ReadCookie(ctx, name), huma.ReadCookies(ctx) [R/features/middleware/, P]
- queryparam package: `Get(query, name string) string`, zero-allocation query lookup, no multi-value support [P/queryparam]
- Unknown query params ignored by default; reject via Config.RejectUnknownQueryParameters or Operation.RejectUnknownQueryParameters (422); SkipValidateParams skips check [R/features/request-validation/]

## 5. Validation tags and rules [R/features/request-validation/]
- doc, format, enum (comma list), const, default (also applied automatically server-side), minimum, exclusiveMinimum, maximum, exclusiveMaximum, multipleOf, minLength, maxLength, pattern, patternDescription (used in error messages), minItems, maxItems, uniqueItems, minProperties, maxProperties, example, readOnly, writeOnly, deprecated, hidden, dependentRequired
- Also: `json` (rename, `json:"-"` exclude, omitempty/omitzero optional), `required:"true|false"`, `nullable:"true|false"`, `additionalProperties:"true"` on a `_` dummy field, `nullable:"true"` on `_` field makes whole struct nullable (on object field panics)
- Field-level `examples` appears as Schema.Examples ([]any) [P]; Schema fields include Title, Description, Ref, ContentEncoding, Default, Examples, Const, Enum, Items, AdditionalProperties, Properties, Min/Max*, Pattern, PatternDescription, UniqueItems, Required, DependentRequired, ReadOnly, WriteOnly, Deprecated, OneOf, AnyOf, AllOf, Not, Discriminator, Extensions [P]
- Built-in string formats: date-time, date-time-http, date, time, duration, email, idn-email, hostname, ip, ipv4, ipv6, uri, iri, uri-reference, iri-reference, uri-template, json-pointer, relative-json-pointer, regex, uuid
- Validator respects schema keywords not, oneOf, anyOf, allOf (custom schemas)
- Config flags: FieldsOptionalByDefault, DefaultArrayNullable (var also exists in pkg), AllowAdditionalPropertiesByDefault, RejectUnknownQueryParameters
- Required/nullable defaults: pointers don't affect required; pointers to scalars nullable unless omitempty; pointers to objects not nullable; slices nullable by default; nullable types emit type arrays like ["string","null"]
- Integer ranges: int8/int16/uint8/uint16 get automatic min/max; uint gets minimum 0 [R/how-to/custom-validation/]; out-of-range minimum/maximum/default panics at registration
- readOnly/writeOnly are documentation only (but ValidateMode ModeReadFromServer / ModeWriteToServer exist) [R, P]
- Objects reject unknown fields by default (additionalProperties:false); ValidateStrictCasing var (case-sensitive field validation) [P]
- Conditional required fields: `dependentRequired` tag or Schema.DependentRequired map[string][]string [R/how-to/conditional-fields/]
- Exhaustive validation errors (all failures in one response) [R/how-to/custom-validation/]
- ValidateResult (Errors; Add, Addf, Reset), huma.Validate(...), PathBuffer, NewPathBuffer [P]
- validation package: exported built-in message templates (MsgUnexpectedProperty, MsgExpectedRFC3339DateTime, ..., MsgExpectedMinLength, MsgExpectedRequiredProperty, MsgExpectedDependentRequiredProperty, etc.) [P/validation]
- Model validation outside HTTP: huma.NewModelValidator(), validator.Validate(reflect.Type, value) (not goroutine-safe); alternative huma.Validate with own registry/PathBuffer/ValidateResult [R/features/model-validation/]
- MimeTypeValidator, NewMimeTypeValidator (exported in pkg index) [P]

## 6. Resolvers and custom validation
- huma.Resolver: `Resolve(ctx huma.Context) []error`; may be on embedded structs; can read host, transform fields after binding [R/features/request-resolvers/]
- huma.ResolverWithPath: `Resolve(ctx, prefix *huma.PathBuffer) []error` with prefix.With("foo") for full field paths [R/features/request-resolvers/]
- Errors as huma.ErrorDetail (Message, Location, Value); returning a huma.StatusError sets status (e.g. huma.Error403Forbidden); last error determines outcome [R/features/request-resolvers/]
- Compile-time check `var _ huma.Resolver = (*MyInput)(nil)` [R/features/request-resolvers/]
- Type-level resolvers apply wherever the type is used (path, query, header, body); struct-level cross-field rules; nested body resolvers [R/how-to/custom-validation/]
- Resolver errors yield 400-level response before handler [R/features/request-resolvers/]
- conditional.Params implements Resolve(ctx) [P/conditional]

## 7. Request limits [R/features/request-limits/]
- Default body limit 1 MiB per operation; Operation.MaxBodyBytes override; 413 on exceed; multipart no default; body fully read into memory
- BodyReadTimeout (5s default); ctx.SetReadDeadline in resolvers; huma.SetReadDeadline helper [P]
- Server timeouts via http.Server (ReadTimeout, WriteTimeout, IdleTimeout, ReadHeaderTimeout); context.Context timeouts for dependencies
- Request Content-Encoding not decoded (use router middleware e.g. Echo middleware.Decompress)

## 8. Responses
- Default status 200 with body, 204 without; Operation.DefaultStatus; dynamic `Status` field on output struct (discouraged) [R/features/response-outputs/]
- Response headers via `header:"Name"` fields; `timeFormat` tag for time.Time; fmt.Stringer support; slice field appends instead of replacing [R/features/response-outputs/]
- Cookies: http.Cookie field or []http.Cookie with Set-Cookie header [R/features/response-outputs/]
- Body field (any type/struct/slice); default Content-Type by negotiation; `nameHint`; []byte body skips serialization [R/features/response-outputs/]
- Binary/image responses: []byte body + Responses map media type at registration + Content-Type header field [R/how-to/image-response/]
- HTML responses: []byte body with text/html; Templ (buffer or streaming via StreamResponse), Gomponents; Accept-based JSON/HTML negotiation; examples/html [R/how-to/html-response/]
- Streaming: Body as func(huma.Context) / huma.StreamResponse{Body func(ctx Context)}; ctx.SetHeader, ctx.BodyWriter(), SetWriteDeadline, http.Flusher, http.ResponseController [R/features/response-streaming/, P]
- SSE package: sse.Register(api, op, eventTypeMap, handler func(ctx, input *I, send sse.Sender)); eventTypeMap name -> struct documents events in OpenAPI; event types must be unique Go types (`type X Y` to reuse); send(Message) and send.Data(any); send.Comment(string) heartbeat (v2.39.0); Message fields IDString, ID (deprecated), Data (JSON), Retry (ms), Comment; sse.WriteTimeout var (5s); auto flush if BodyWriter is http.Flusher [R/features/server-sent-events-sse/, P/sse]
- Response Transformers: huma.Transformer `func(ctx, status string, v any) (any, error)`; registered in Config.Transformers; run after handler before marshal; FieldSelectTransform example using shorthand.GetPath [R/features/response-transformers/]
- huma.SchemaLinkTransformer (NewSchemaLinkTransformer): adds `$schema` property to object responses and `Link: <...>; rel="describedby"` header [R/features/response-transformers/, R/features/json-schema-registry/]
- Operation responses override via Responses map [R/how-to/image-response/]

## 9. Content negotiation and formats [R/features/response-serialization/]
- Config.Formats map (content type or suffix -> huma.Format {Marshal func(io.Writer, any) error, Unmarshal func([]byte, any) error})
- JSON always: application/json and +json (huma.DefaultJSONFormat); DefaultFormats var
- CBOR optional via blank import of formats/cbor (cbor.DefaultCBORFormat; application/cbor and +cbor)
- Custom formats (e.g. Protobuf) by implementing huma.Format
- Request body chosen by Content-Type; response by Accept; default JSON; YAML example input with application/yaml via Restish
- Config.DefaultFormat; Config.NoFormatFallback (406 for unknown instead of JSON fallback) [P]
- API.Negotiate(accept), API.Marshal, API.Unmarshal, API.Transform [P]
- ErrUnknownAcceptContentType, ErrUnknownContentType vars [P]
- negotiation package: SelectQValue(header, allowed), SelectQValueFast (no allocations) [P/negotiation]
- yaml package: yaml.Convert(w io.Writer, r io.Reader) JSON to YAML [P/yaml]
- casing package: Camel, LowerCamel, Snake, Kebab, Join, Split, Initialism, Identity, MergeNumbers, TransformFunc [P/casing]

## 10. Error model [R/features/response-errors/]
- Helpers `huma.Error{code}{Name}(msg, errs...)` for all 4xx/5xx (Error400BadRequest ... Error511NetworkAuthenticationRequired); Status304NotModified [P]
- Non-status errors become 500
- RFC 9457 application/problem+json: $schema, status, title, detail, errors[] (location, message, value); Link describedBy header
- huma.ErrorModel, huma.ErrorDetail, huma.ErrorDetailer (`ErrorDetail() *ErrorDetail`), huma.StatusError, huma.HeadersError, huma.ContentTypeFilter
- Status precedence: panic 500 > too large 413 > read timeout 408 > unparseable 400 > validation 422 > other 400
- huma.ErrorWithHeaders(err, http.Header) (appends, safe repeated)
- Customize: implement StatusError, override huma.NewError (also NewErrorWithContext, ErrorFormatter vars [P]), implement ContentTypeFilter
- Operation.Errors lists status codes documented in OpenAPI
- huma.WriteErr(api, ctx, status, msg, errs...) for middleware [R/features/middleware/]

## 11. Schema, registry, customization
- Schema registry: each Go type generated once, named references; default map registry (huma.NewMapRegistry(prefix, namer)); recursive schemas supported [R/features/json-schema-registry/, P]
- huma.DefaultSchemaNamer (name = type name without package); custom namer; name conflicts need distinct types or custom namer [R/features/json-schema-registry/]
- huma.Registry interface: Schema(t, allowRef, hint), SchemaFromRef, TypeFromRef, Map, RegisterTypeAlias [P]
- Custom registry assigned to config.OpenAPI.Components.Schemas [R/features/json-schema-registry/]
- Schemas served at config.SchemasPath [R/features/json-schema-registry/]
- Disable $schema and Link: `config.CreateHooks = nil`; or manage Transformers/OnAddOperation manually [R/features/json-schema-registry/]
- Operation-level schema override via RequestBody/Responses with huma.MediaType, huma.Schema{Type, Properties, Extensions}; types huma.TypeObject/String/etc. [R/features/schema-customization/]
- huma.SchemaProvider (`Schema(r Registry) *Schema`) per-type override; example OmittableNullable[T] (omitted vs null vs value) [R/features/schema-customization/]
- huma.SchemaTransformer (`TransformSchema(r Registry, s *Schema) *Schema`) [P]
- huma.SchemaFromType / SchemaFromField; Schema.PrecomputeMessages [P]
- Discriminator type; oneOf/anyOf/allOf/not on Schema [P]
- Tag type for operation tags (Name, Description, ExternalDocs, Extensions) [P]
- Operation/Config Extensions flattened as siblings of standard fields [R/features/openapi-generation/]

## 12. OpenAPI document and docs UI
- OpenAPI 3.1 JSON/YAML at /openapi.json, /openapi.yaml; 3.0.3 at /openapi-3.0.json, /openapi-3.0.yaml [R/features/openapi-generation/]
- Config paths: OpenAPIPath (/openapi), DocsPath (/docs), SchemasPath (/schemas); blank disables [R/features/openapi-generation/]
- huma.DefaultConfig(title, version); config.Components.SecuritySchemes (Type, Scheme, BearerFormat); Operation.Security; config.Extensions; wrapping huma.Register for org-wide settings [R/features/openapi-generation/]
- api.OpenAPI() editable until server starts; OpenAPI.YAML(), DowngradeYAML() [R/features/cli/, P]
- Types: OpenAPI, Info, Contact, License, Server, ServerVariable, Components, PathItem, Response, Header, Link, Example, Encoding, ExternalDocs, SecurityScheme, OAuthFlows, OAuthFlow, RequestBody, MediaType, ProtoVersion [P]
- Docs renderers: Config.DocsRenderer = DocsRendererStoplightElements (default), DocsRendererScalar, DocsRendererSwaggerUI; DocsRendererConfig (JSON into HTML; Scalar and SwaggerUI only) [R/features/api-docs/]
- Scalar options: theme, hideModels; SwaggerUI options: defaultModelsExpandDepth, tryItOutEnabled, persistAuthorization [R/features/api-docs/]
- Disable built-in docs (DocsPath empty) and register custom route; chi middleware.URLFormat conflict with .json/.yaml [R/features/api-docs/]
- CLI AutoConfig: o.Extensions["x-cli-config"] = huma.AutoConfig{Security, Params}; AutoConfigVar prompts [R/features/cli-auto-config/, P]
- OAuth2/JWT how-to: oauth2 and http bearer SecuritySchemes, per-operation Security scopes, auth middleware with JWKS cache, 401/403 via WriteErr, gateway validation [R/how-to/oauth2-jwt/]
- OperationDocumenter / Operation.Hidden interplay [R/features/groups/]

## 13. Middleware
- Router-specific (applied to router before humaXxx.New) vs router-agnostic Huma middleware (`func(ctx huma.Context, next func(huma.Context))`), api.UseMiddleware, API.Middlewares(), huma.Middlewares type [R/features/middleware/, P]
- Huma middleware only runs for matched operations; register before operations
- Per-operation Operation.Middlewares; Operation.Metadata-driven logic; path checks in global middleware [R/features/middleware/]
- ctx.Context(), huma.WithValue(ctx, k, v), huma.WithContext(ctx, c) [R/features/middleware/]
- Set-Cookie via ctx.AppendHeader [R/features/middleware/]
- Early return without next; huma.WriteErr [R/features/middleware/]
- huma.Context methods: Operation, Context, TLS, Version, Method, Host, RemoteAddr, URL, Param, Query, Header, EachHeader, BodyReader, GetMultipartForm, SetReadDeadline, SetStatus, Status, SetHeader, AppendHeader, BodyWriter [P]
- huma.API interface: Adapter, OpenAPI, Negotiate, Transform, Marshal, Unmarshal, UseMiddleware, Middlewares [P]
- Config.CreateHooks (functions that modify config before API creation) [P]

## 14. Groups [R/features/groups/]
- huma.NewGroup(api, prefixes...); works with Register/Get/Post...
- Multiple prefixes; huma.PrefixModifier adjusts operation IDs and tags
- Group: UseMiddleware, UseModifier(func(op, next)), UseSimpleModifier(func(op)), UseTransformer; ModifyOperation, DocumentOperation, Adapter, Config, Middlewares, Transform [R/features/groups/, P]
- huma.OperationDocumenter for custom doc generation; default skips Hidden; nested groups
- Manual group.DocumentOperation when not using huma.Register

## 15. Extra packages
- autopatch: AutoPatch(api) generates PATCH when GET+PUT exist and no PATCH; formats JSON Merge Patch (application/merge-patch+json, RFC 7386), Shorthand Merge Patch (application/merge-patch+shorthand), JSON Patch (application/json-patch+json, RFC 6902); default Merge Patch; uses ETag/Last-Modified from GET for conditional PUT; metadata `"autopatch": false` on GET/PUT skips; PatchResource(api, pathItem); RegisterNullabilityExtension(api, stringRepresentationOfNull); MergePatchNullabilitySettings{Enabled, StringRepresentationOfNull}; const MergePatchNullabilityExtension "x-merge-patch-nullability" [R/features/auto-patch/, P/autopatch]
- conditional: Params embed (IfMatch []string, IfNoneMatch, IfModifiedSince, IfUnmodifiedSince); HasConditionalParams(); PreconditionFailed(etag, modified) returns StatusError (304 reads / 412 writes); Resolve; document 304/412 [R/features/conditional-requests/, P/conditional]
- sse: see section 8
- humatest: New(tb, configs...), Wrap(tb, api), TestAPI with Do/Get/Post/Put/Patch/Delete and Ctx variants (strings = headers, io.Reader = body, other = JSON), returns *httptest.ResponseRecorder; NewAdapter, NewContext, DumpRequest/DumpResponse, PrintRequest/PrintResponse; TB interface [R/features/test-utilities/, R/tutorial/writing-tests/, P/humatest]
- Wrap avoids Unwrap panics with humatest.New [R/features/test-utilities/]

## 16. humacli [R/features/cli/, P/humacli]
- humacli.New(func(hooks Hooks, opts *Options)), cli.Run(), cli.Root() (cobra.Command)
- Hooks.OnStart, Hooks.OnStop (graceful shutdown how-to with timeout context) [R/how-to/graceful-shutdown/]
- Options struct to flags: --kebab-case, `name`, `short`, `default`, `doc` tags; env vars prefixed SERVICE_ (CLI arg wins)
- Types: bool, int, int64, string, time.Duration
- Custom commands via Root().AddCommand; huma.WithOptions/humacli.WithOptions wrapper; `openapi` command via api.OpenAPI().YAML() / DowngradeYAML()
- App name (cmd.Use) and version (cmd.Version); -h/--help, -v/--version

## 17. Clients and tooling
- Restish: api connect, operation commands from OperationID, completion, `edit` using $schema [R/tutorial/cli-client/, R/features/json-schema-registry/]
- SDK generation with oapi-codegen / OpenAPI Generator; openapi.tools, tools.openapis.org [R/tutorial/client-sdks/]
- JSON Schema per resource via describedby Link headers and $schema (editor completion in VSCode) [R/features/json-schema-registry/]
- $schema property ignored on input so responses can be sent back unchanged [R/features/json-schema-registry/]
- Tutorial items: huma.Get greeting example, CBOR import, /docs, Link header + $schema auto-added, 201 default status example, omitempty for optional fields [R/tutorial/your-first-api/, R/tutorial/sending-data/]
