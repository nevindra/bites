# Feature inventory: Go microservice frameworks (Kratos, go-zero, go-micro, Encore)

Read on 2026-11-04 from each project's official documentation site and, where the docs were thin or silent, from its GitHub repository (directory listings and a few source files on the default branch, no commit pinned). Every answer closes with the URL it came from; "not found in the docs" means the pages listed under Gaps did not say, not that the feature is absent.

Method and limits: pages were fetched with a tool that returns a model-written summary of each page, so quoted phrases are the summary's quotation of the page, not a byte-exact copy. Repository listings came from the GitHub contents API. GitHub code search was used for keyword checks on Kratos and go-zero and hit its rate limit partway through the go-micro checks, so go-micro keyword absences (i18n, cron, kafka, xml, yaml, msgpack) rest on the docs pages alone. Several go-micro pages (`/docs/interfaces/*`, `/docs/store.html`, `/docs/observability.html`, `/docs/client-server.html`, `/docs/examples/realworld/*`) are marked by the site itself as older documentation and point to the quickstart; they are cited as such.

# 1. Kratos (go-kratos/kratos, v3)

## 1.1 Service registration and discovery

Built in as interfaces in the core `registry` package: `Registrar`, `Discovery`, `Watcher`, `ServiceInstance`, with methods `Register`, `Deregister`, `Fetch`, `Watch`, `Stop`. A server registers with `kratos.Registrar(registrar)` in `kratos.New(...)`; clients discover with `grpc.WithDiscovery(discovery)` and `http.WithDiscovery(discovery)`. Registries are independent contrib modules: Consul, etcd, Eureka, Kubernetes, Nacos, Polaris, ServiceComb, ZooKeeper and a generic discovery adapter (the repo lists `contrib/registry/{consul,discovery,etcd,eureka,kubernetes,nacos,polaris,servicecomb,zookeeper}`). No mDNS found. `RegistrarTimeout` bounds the registration call.
- https://go-kratos.dev/docs/component/registry/
- https://go-kratos.dev/docs/component/application/
- https://github.com/go-kratos/kratos/tree/main/contrib/registry (listing read through the GitHub API)

## 1.2 Configuration

Built in: `github.com/go-kratos/kratos/v3/config` with `config.New`, `config.WithSource`, `c.Load()`, `c.Scan(&v)` ("decodes the whole configuration tree into a struct, protobuf message, or another compatible Go value"), `config.Get[T](c, "key")`, `c.Watch(key, observer)`. Core sources: `file.NewSource("configs")` and `env.NewSource("KRATOS")` (prefix stripped). Remote sources are contrib modules: Consul, Etcd, Nacos, Apollo, Kubernetes, Polaris (repo `contrib/config/{apollo,consul,etcd,kubernetes,nacos,polaris}`). Later sources override earlier ones. A flag source: not found in the docs (the repo `config/` has only `env` and `file` sub-packages; the quickstart passes the config directory with `-conf ./configs`).
- https://go-kratos.dev/docs/component/config/
- https://go-kratos.dev/docs/getting-started/start

## 1.3 gRPC

Served beside HTTP: yes. `transport.Server` is implemented by `transport/http` and `transport/grpc`; both are passed to `kratos.New` with `kratos.Server`, and the quickstart listens on HTTP 8000 and gRPC 9000. One definition: a `.proto` with `google.api.http` options (`google/api/annotations.proto`) yields `*.pb.go`, `*_grpc.pb.go` and `*_http.pb.go`, with `RegisterTodoServiceServer` / `RegisterTodoServiceHTTPServer` and `NewTodoServiceClient` / `NewTodoServiceHTTPClient`. HTTP transcoding is described as optional. Code generation: the documented workflow depends on it (`make api`, `buf.gen.yaml`); the page does not use the word "required". The gRPC server registers the standard health service and reflection by default.
- https://go-kratos.dev/docs/component/transport/overview/
- https://go-kratos.dev/docs/guide/api-protobuf/
- https://go-kratos.dev/docs/component/transport/grpc/

## 1.4 Pub/sub and message-queue consumers

Not found in the docs read. The repo `ROADMAP.md` ticks "Event: Pub/Sub, Kafka" and leaves "Nats" unticked, but the repository root and `contrib/` listings show no event, broker or queue package (contrib holds `config encoding errortracker middleware opensergo otel polaris registry transport`).
- https://github.com/go-kratos/kratos/blob/main/ROADMAP.md
- https://github.com/go-kratos/kratos (root listing)

## 1.5 Background jobs, cron, scheduled tasks

Not found in the docs. App lifecycle hooks exist (`BeforeStart`, `AfterStart`, `BeforeStop`, `AfterStop`) but the page says not to start a second server from a hook and describes no scheduler.
- https://go-kratos.dev/docs/component/application/

## 1.6 i18n or localised error messages

Not found. The errors page says the message is "a single string at construction" with a stable `reason`; it describes no locale mechanism.
- https://go-kratos.dev/docs/component/errors/

## 1.7 Bodies other than JSON, and negotiation

Codec registry in `encoding` (`encoding.RegisterCodec`, `encoding.GetCodec(name)`); the repo has `encoding/{form,json,proto,protojson,xml,yaml}` and `contrib/encoding/{json,msgpack}`. The docs page names form, JSON, protobuf binary, protobuf JSON, XML and YAML as built in and says it "does not mention msgpack" (the repo lists a `contrib/encoding/msgpack` directory). Request decoding picks the codec from `Content-Type` (unregistered type gives 400 with reason `CODEC`); response and error encoding picks from `Accept` and falls back to `json`. `google.api.HttpBody` bypasses the codec.
- https://go-kratos.dev/docs/component/encoding/
- https://go-kratos.dev/docs/component/transport/http/

## 1.8 HTTP/3 or QUIC

Not found in the docs (the HTTP and gRPC transport pages do not mention it; code search for `http3` returned nothing; `quic` matched only the word "quickstart").
- https://go-kratos.dev/docs/component/transport/http/

## 1.9 Database layer or code generation for models

No framework database layer. "Kratos does not require an ORM"; the project template uses Ent, with models generated from handwritten schemas (`//go:generate go run -mod=mod entgo.io/ent/cmd/ent generate ./schema`), and the page says Ent can be replaced by `database/sql`, GORM or another store. The roadmap lists Ent ticked and Gorm unticked.
- https://go-kratos.dev/docs/guide/ent/
- https://github.com/go-kratos/kratos/blob/main/ROADMAP.md

## 1.10 Health, shutdown, tracing, metrics

- Health: gRPC standard health service on by default (`CustomHealth` to replace it; client `grpc.WithHealthCheck(false)`); no built-in HTTP health endpoint, the HTTP page shows a handwritten `/healthz` route. https://go-kratos.dev/docs/component/transport/grpc/ and https://go-kratos.dev/docs/component/transport/http/
- Graceful shutdown: SIGTERM, SIGQUIT, SIGINT by default (`Signal` option); `Stop` runs `BeforeStop`, deregisters, cancels; `StopTimeout` bounds transport shutdown. https://go-kratos.dev/docs/component/application/
- Tracing: OpenTelemetry in contrib module `github.com/go-kratos/kratos/contrib/otel/v3/tracing` (`tracing.Server`, `tracing.Client`), propagation of Kratos metadata, W3C baggage and W3C trace context. https://go-kratos.dev/docs/component/middleware/tracing/
- Metrics: no core facade; contrib `contrib/otel/v3/metrics` records request count and duration (`server_requests_code_total`, `server_requests_seconds`), exporter chosen by the application. https://go-kratos.dev/docs/component/metrics/
- Also in the middleware sidebar: auth, circuitbreaker, logging, ratelimit, recovery, validate. https://go-kratos.dev/docs/getting-started/start

# 2. go-zero (zeromicro/go-zero)

## 2.1 Service registration and discovery

Built in for `zrpc`. The page names three modes: etcd ("Recommended for Production": server `Etcd: {Hosts, Key}`, client the same block), Kubernetes (client `Target: dns:///order-rpc-svc.default.svc.cluster.local:8080`, or `Endpoints`), and static `Endpoints` for dev and CI. Clients are built with `zrpc.MustNewClient(c.OrderRpc)`. The README also gives a `k8s://<namespace>/<service>:<port>` target that lists and watches `endpointslices`. Consul and Nacos: not found in the docs ("pluggable service discovery backends" is stated without a list).
- https://go-zero.dev/guides/microservice/service-discovery/
- https://github.com/zeromicro/go-zero (README)

## 2.2 Configuration

Built in: `conf.MustLoad(path, &c)` and `conf.Load` into a struct; YAML, JSON, TOML and JSON5 (since v1.10.1) chosen by file suffix; `LoadFromYamlBytes` and siblings. Environment: `conf.UseEnv()` expands `${SERVER_NAME}` in values, and a tag `json:",env=SERVER_NAME"` reads a variable (priority env, then file, then tag default). Flags: only the config path, through the standard `flag` package in the examples (`-f`). Struct tags on the `json` tag: `optional`, `default=`, `options`, `range=[min:max]`, `env=`; a field with no tag is required. Remote config centre: package `core/configcenter` with `configcenter.MustNewConfigCenter[T](configcenter.Config{...}, ss)`, `GetConfig()`, `AddListener`; built-in subscriber `subscriber.MustNewEtcdSubscriber(subscriber.EtcdConf{...})`, other systems (Consul, Nacos, Apollo named as examples) by implementing `Subscriber`; json, yaml, toml.
- https://go-zero.dev/reference/configuration/overview/
- https://go-zero.dev/guides/microservice/config-center/

## 2.3 gRPC

Both exist as separate server kinds (`rest` and `zrpc` packages in the repo), generated by `goctl` from different definitions: `.api` for HTTP (types, `@server`, `service` blocks with `@handler` and routes) and `.proto` for RPC (`goctl rpc protoc greeter.proto --go_out=. --go-grpc_out=. --zrpc_out=.`). The API DSL page does not mention gRPC. Whether one process can serve both: not found in the docs (the repo has a `core/service` `servicegroup.go`; not read for this question). HTTP-to-gRPC from one `.proto`: the gateway (`gateway/` in the repo) maps HTTP routes to RPC methods with `Grpc: {Target, Timeout}`, `ProtoSets: [order.pb]` and `Mappings: [{Method, Path, RpcPath}]`; the page uses descriptor sets and explicit mappings, not `google.api.http`, and does not say how `order.pb` is produced. Code generation is the documented path for both kinds (`goctl`).
- https://go-zero.dev/reference/api-dsl/signature/
- https://go-zero.dev/guides/grpc/server/
- https://go-zero.dev/guides/gateway/http/

## 2.4 Pub/sub and message-queue consumers

Kafka: package `kq` in the separate module `github.com/zeromicro/go-queue` (wraps `segmentio/kafka-go`); producer `kq.NewPusher(brokers, topic)` with `Push(data)`; consumer `kq.MustNewQueue(conf, handler)` returning a `service.Service`, config `kq.KqConf` (`Brokers`, `Group`, `Topic`, `Offset`, `Conns`, `Consumers`, `Processors`, `MinBytes`, `MaxBytes`, `Username`, `Password`). The handler is not typed: `Consume(key, val string) error`. Beanstalkd delay queue: `dq.NewProducer`, `Delay`, `At`, `dq.NewConsumer(...).Consume`, handler `func(body []byte)`. RabbitMQ: the docs page shows raw `amqp091-go` and names a community plugin `github.com/zeromicro/zero-contrib/zrpc/rabbitmq`; no built-in listener API on that page. NATS and Redis streams: not found in the docs read.
- https://go-zero.dev/guides/queue/kafka/
- https://go-zero.dev/guides/queue/beanstalkd/
- https://go-zero.dev/components/queue/rabbitmq/
- https://go-zero.dev/components/queue/

## 2.5 Background jobs, cron, scheduled tasks

The guide documents only deploying a service as a Kubernetes CronJob (`goctl` also ships a `kube/job.tpl` with `kind: CronJob`); no in-process scheduler API found. Delayed jobs exist through the Beanstalkd `dq` package above.
- https://go-zero.dev/guides/cron-job/
- https://go-zero.dev/guides/cron-job/k8s/
- https://go-zero.dev/guides/queue/beanstalkd/

## 2.6 i18n or localised error messages

Not found. The error page shows `httpx.SetErrorHandler` with `*errors.CodeMsg` and plain-string messages; code search for `i18n` in the repository returned nothing.
- https://go-zero.dev/guides/http/server/error/

## 2.7 Bodies other than JSON, and negotiation

Request parsing is by struct tag through `httpx.Parse(r, &req)`: `json` (body, `application/json`), `form` (query and `application/x-www-form-urlencoded`), `path`, `header`. XML, MessagePack, protobuf, YAML request bodies: not found in the docs. Responses: the guide shows `httpx.OkJson(w, resp)` and points to the extension module `zeromicro/x` (`http/responses.go`) for "additional response formats (e.g. XML)"; protobuf and content negotiation not mentioned. A file-upload guide exists (`/guides/http/file-upload/`, not read).
- https://go-zero.dev/guides/http/server/request-body/
- https://go-zero.dev/guides/http/server/response-body/

## 2.8 HTTP/3 or QUIC

Not found (code search for `http3` returned nothing; `quic` matched only the word "quickstart").
- https://github.com/zeromicro/go-zero

## 2.9 Database layer or code generation for models

Shipped: `core/stores` with sub-packages `sqlx`, `sqlc`, `postgres`, `mon` and `monc` (MongoDB), `redis`, `kv`, `cache`, `builder`. The MySQL guide names `sqlx.NewMysql`, `sqlx.NewBulkInserter`, `sqlx.ErrNotFound`, `sqlx.Session`, and says it is not described as an ORM. `goctl model` generates model code: `goctl model mysql ddl --src`, `goctl model mysql datasource --url ... --table ...`, `goctl model pg datasource ...`, `goctl model mongo --type User` (field list filled in by hand), with optional cache (`-c` / `--cache`) over Redis.
- https://go-zero.dev/reference/cli-guide/model/
- https://go-zero.dev/guides/database/mysql/

## 2.10 Health, shutdown, tracing, metrics

- Health: the dev server (`internal/devserver`, config `DevServer`, since v1.4.3) serves `HealthPath` default `/healthz` (response `OK`) and `MetricsPath` default `/metrics` on port 6060 by default, plus pprof. Read from source; the docs configuration pages do not describe health. https://go-zero.dev/reference/configuration/service-config/ (lists `DevServer`) and https://github.com/zeromicro/go-zero/blob/master/internal/devserver/config.go
- Graceful shutdown: in source, `core/proc` has `ShutdownConf{WrapUpTime default 1s, WaitTime default 5.5s}`, `AddShutdownListener`, `AddWrapUpListener`; the architecture page does not cover shutdown. https://github.com/zeromicro/go-zero/blob/master/core/proc/shutdown.go
- Tracing: OpenTelemetry, `Telemetry: {Name, Endpoint, Sampler, Batcher}`, batchers `otlpgrpc` (default), `otlphttp`, `zipkin`, `file`; the `jaeger` batcher was removed in v1.10.0; W3C `traceparent` propagation; spans for HTTP, zrpc, SQL, Redis. https://go-zero.dev/components/observability/tracing/
- Metrics: Prometheus scrape endpoint under `Prometheus: {Host, Port, Path}` with `http_server_requests_total`, `http_server_duration_ms`, `rpc_server_requests_total`, `rpc_server_duration_ms`; custom metrics through `core/metric` (`metric.NewCounterVec`, `metric.NewHistogramVec`). https://go-zero.dev/components/observability/metrics/
- Also: circuit breaker, rate limiter, adaptive load shedding, timeout control (README). https://github.com/zeromicro/go-zero

# 3. go-micro (micro/go-micro, module go-micro.dev/v6)

## 3.1 Service registration and discovery

Built in: package `registry`; default is multicast DNS (mDNS); NATS is named as the alternative where multicast is blocked. Docs list MDNS (default), Consul, Etcd, NATS with `micro.Registry(reg)`, `consul.NewRegistry()`, and env vars `MICRO_REGISTRY` (`mdns`, `consul`, `etcd`, `nats`) and `MICRO_REGISTRY_ADDRESS`; import paths `go-micro.dev/v6/registry/{consul,etcd,nats,mdns}`. The repo `registry/` also holds `memory.go` and `cache`. Kubernetes registry: not found (a `deploy/kubernetes` directory exists; its contents were not read). A `health.RegistryCheck` verifies the registry is reachable.
- https://go-micro.dev/docs/interfaces/registry.html (older documentation)
- https://go-micro.dev/docs/services.html
- https://go-micro.dev/docs/plugins.html
- https://github.com/micro/go-micro/tree/master/registry (listing)

## 3.2 Configuration

Two layers. Service wiring by environment variables and options: `MICRO_REGISTRY`, `MICRO_BROKER`, `MICRO_STORE_ADDRESS`, equivalents `micro.Registry()`, `micro.Broker()`, `micro.Transport()`; "explicit code options always win", then env, then defaults; the page marks files, vaults and remote services as a future plugin level. Application config package `config` ("an interface for dynamic configuration"): `Config` with `Load(source ...source.Source)` and `Watch(path ...string)`, embedding `reader.Values`, which has `Scan(v interface{}) error`; sources in the repo `config/source/{cli,env,file,flag,memory,nats}`; one encoder, `config/encoder/json`; secrets under `config/secrets/{box,secretbox}`. The docs config page does not cover sources, scanning or watchers (this paragraph on the package is from the repo tree and source, not from the docs).
- https://go-micro.dev/docs/config.html (older documentation)
- https://github.com/micro/go-micro/blob/master/config/config.go
- https://github.com/micro/go-micro/blob/master/config/reader/reader.go
- https://github.com/micro/go-micro/tree/master/config (listings)

## 3.3 gRPC

Native gRPC server and client exist as replaceable components: `go-micro.dev/v6/server/grpc` and `go-micro.dev/v6/client/grpc`, enabled with `micro.Server(grpcServer.NewServer(...))`, `micro.Client(grpcClient.NewClient())` or `MICRO_SERVER=grpc`, `MICRO_CLIENT=grpc`; `grpcServer.Reflection()` for tooling. A separate `transport/grpc` uses gRPC only as a transport. Served beside HTTP in one service: not found in the docs ("does not say"). The `web` package (`web.Service`, `Handle`, `HandleFunc` on `net/http`) is a separate HTTP service with discovery. HTTP and gRPC routes from one IDL: not found; `protoc-gen-micro` is run with `--micro_out` alongside `--go_out` and `--go-grpc_out` and generates the RPC handler and client code, no HTTP routes are described. A `micro gateway` command is an "Authenticated HTTP-to-RPC proxy at `/api/{service}/{method}`". Code generation is not required: the quickstart uses plain Go structs with `json` tags and `service.Handle(new(Notes))`, calls addressed as `Notes.List`.
- https://go-micro.dev/docs/guides/grpc-compatibility.html
- https://go-micro.dev/docs/quickstart.html
- https://go-micro.dev/docs/server.html
- https://github.com/micro/go-micro/blob/master/web/web.go

## 3.4 Pub/sub and message-queue consumers

Built in as the `broker` package: `broker.Publish("topic", &broker.Message{Body: ...})` and `broker.Subscribe("topic", handler)` with handler `func(p broker.Event) error` reading `p.Message().Body` (bytes, not typed). Implementations in the docs: HTTP (default), NATS (`broker/nats`), RabbitMQ (`broker/rabbitmq`), memory (`broker/memory`); Kafka is not listed. Typed handlers exist on the server side: `server.NewSubscriber(topic, handler)` and `Subscribe`, where the handler is a function (or an object with methods) of signature `func(context.Context, interface{}) error`, validated by reflection at registration (exported argument type, one `error` return); this paragraph is from `server/subscriber.go`, not from a docs page. Also an `events` package (`Stream.Publish`, `Stream.Consume`, `Store`) with a NATS JetStream implementation (`events/natsjs`) in the listing.
- https://go-micro.dev/docs/interfaces/broker.html (older documentation)
- https://go-micro.dev/docs/plugins.html
- https://github.com/micro/go-micro/blob/master/server/subscriber.go
- https://github.com/micro/go-micro/blob/master/events/events.go

## 3.5 Background jobs, cron, scheduled tasks

Only in the agent and flow packages, read from source, not from a docs page: `agent.ScheduleTask(ctx, a, message, every time.Duration)` stores a persisted recurring request, and `flow.Scheduled(f, data)` gives a `Schedule` with `Tick` and (per its comment) `RunEvery`, where "callers own the clock". The agents-and-workflows guide does not document scheduling; it documents broker-triggered flows (`micro.FlowTrigger("events.user.created")`) with checkpointed resume (`f.Pending`, `f.Resume`). A general cron facility for handlers: not found.
- https://github.com/micro/go-micro/blob/master/agent/schedule.go
- https://github.com/micro/go-micro/blob/master/flow/schedule.go
- https://go-micro.dev/docs/guides/agents-and-workflows.html

## 3.6 i18n or localised error messages

Not found in the docs (an error-handling guide exists at `/docs/guides/error-handling.html` and was not read; code search for `i18n` returned nothing before the rate limit).
- https://go-micro.dev/docs/guides/index.html (guide list from the sitemap)

## 3.7 Bodies other than JSON, and negotiation

Codecs are chosen by content type in `server/rpc_codec.go`: `application/grpc`, `application/grpc+json`, `application/grpc+proto`, `application/json`, `application/json-rpc`, `application/protobuf`, `application/proto-rpc`, `application/octet-stream`; `DefaultContentType = "application/protobuf"`. The repo `codec/` holds `bytes`, `grpc`, `json`, `jsonrpc`, `proto`, `protorpc`, `text`. This is RPC codec selection by content type; XML, YAML, form, MessagePack: not found. The docs pages read (client-server, services) do not cover codecs.
- https://github.com/micro/go-micro/blob/master/server/rpc_codec.go
- https://github.com/micro/go-micro/tree/master/codec (listing)

## 3.8 HTTP/3 or QUIC

Planned, not built: the repo `ROADMAP.md` lists "HTTP/3 transport" under "Later, exploratory"; the `transport` package comment says "various implementations; http, grpc, quic" but the listing has no quic directory (only `grpc`, `nats`, http files, `memory`).
- https://github.com/micro/go-micro/blob/master/ROADMAP.md
- https://github.com/micro/go-micro/blob/master/transport/transport.go

## 3.9 Database layer or code generation for models

No ORM and no model generation. The `store` package is a key-value interface (`store.Write(&store.Record{Key, Value})`, `store.Read(key)`, `micro.Store(st)`) with memory (default), file, MySQL, Postgres and NATS JetStream KV backends (`go-micro.dev/v6/store/{file,mysql,postgres,nats-js-kv}`), selected by `MICRO_STORE`, `MICRO_STORE_ADDRESS`, `MICRO_STORE_DATABASE`, `MICRO_STORE_TABLE`.
- https://go-micro.dev/docs/store.html (older documentation)

## 3.10 Health, shutdown, tracing, metrics

- Health: package `health`: `health.Register`, `health.RegisterCheck(health.Check{Name, Check, Critical, Timeout})`, `health.RegisterHandlers(mux)` serving `/health`, `/health/live`, `/health/ready` (503 when down); built-in checks `PingCheck`, `TCPCheck`, `HTTPCheck`, `DNSCheck`, `CustomCheck`, `RegistryCheck`. https://go-micro.dev/docs/guides/health.html
- Graceful shutdown: SIGTERM and SIGINT handled by default, cleanup through `micro.BeforeStop(func() error)`; the page says "you need to implement cleanup logic". https://go-micro.dev/docs/examples/realworld/graceful-shutdown.html (older documentation)
- Tracing and metrics: the older observability page says OpenTelemetry helpers and automatic span wrapping are roadmap items and shows a pseudo-code metrics wrapper only; the repo has `wrapper/trace/opentelemetry` and `wrapper/monitoring/prometheus`, not described by any docs page read. https://go-micro.dev/docs/observability.html and https://github.com/micro/go-micro/tree/master/wrapper (listing)

# 4. Encore (encore.dev, Go SDK)

## 4.1 Service registration and discovery

No registry to choose: services are Go packages with at least one API (`//encore:api`), "Encore recognizes this as a service, and uses the package name as the service name", calls are made as plain function calls (`hello.Ping(ctx, &hello.PingParams{...})`) and compiled into API calls by static analysis. How discovery works internally: not found in the docs (the services and api-calls pages say it is not covered). For self-hosting, the infra config file has a `service_discovery` section keyed by service name with `base_url` and optional `auth`, not needed when all services are in one Docker image.
- https://encore.dev/docs/go/primitives/services
- https://encore.dev/docs/go/primitives/api-calls
- https://encore.dev/docs/self-host/configure-infra

## 4.2 Configuration

Two mechanisms. Secrets: an unexported `var secrets struct { ... }` of `string` fields, set with `encore secret set --type <types> <name>` or the dashboard, per environment type (`production`, `development`, `preview`, `local`), local override in `.secrets.local.cue`. Config: `config.Load[*SomeConfigType]()` at package level, typed by wrappers such as `config.String`, `config.Bool`, `config.Int`, values in CUE files (`.cue` in the service directory and below, unified), overridable per environment with metadata such as `#Meta.Environment.Type`. Environment variables, flags and remote config centres: not found in the docs read (secrets reach containers as secret environment variables in a self-owned cloud).
- https://encore.dev/docs/go/primitives/secrets
- https://encore.dev/docs/go/develop/config

## 4.3 gRPC

Not native. The only gRPC-related Go page is "Use Connect for incoming gRPC requests": a `.proto` is compiled outside Encore with `buf generate` (`protoc-gen-go`, `protoc-gen-connect-go`) and mounted behind a raw endpoint (`//encore:api public raw path=/greet.v1.GreetService/*endpoint`) that forwards to a standard `http.Handler`; the page does not claim routes come from one definition (the Encore path is written by hand). Encore API errors use gRPC-identical codes. Code generation for Encore itself is automatic at compile time (boilerplate generated from the annotated functions), not a step the user runs.
- https://encore.dev/docs/go/how-to/grpc-connect
- https://encore.dev/docs/go/primitives/defining-apis
- https://encore.dev/docs/go/primitives/api-errors

## 4.4 Pub/sub and message-queue consumers

Built in and typed: `pubsub.NewTopic[T]("name", pubsub.TopicConfig{...})`, `pubsub.NewSubscription(topic, "name", pubsub.SubscriptionConfig[T]{...})`, handler `func(ctx context.Context, event *T) error`, `Topic.Publish(ctx, event)`, `pubsub.MethodHandler((*Service).Method)`, delivery `pubsub.AtLeastOnce` or `pubsub.ExactlyOnce`, ordering through `OrderingAttribute` and a `pubsub-attr` field tag. Backends: the pub/sub page names AWS SQS and GCP Pub/Sub only through quota notes; the self-host infra config lists `nsq`, `gcp_pubsub`, `aws_sns_sqs` (and states no `nats` or `kafka` type appears). Kafka, NATS, RabbitMQ, Redis streams: not found. A "Transactional Pub/Sub outbox" how-to exists (not read).
- https://encore.dev/docs/go/primitives/pubsub
- https://encore.dev/docs/self-host/configure-infra

## 4.5 Background jobs, cron, scheduled tasks

Built in: `cron.NewJob("welcome-email", cron.JobConfig{Title, Every | Schedule, Endpoint})` from `encore.dev/cron`; `Every` must divide 24 hours evenly (checked at compile time), `Schedule` takes a cron expression, all times UTC; the endpoint must take no parameters (`func(context.Context) error` or `func(context.Context) (*T, error)`). Jobs do not run in local development or preview environments; the free tier runs at most hourly. Delayed or queued jobs beyond that: a Temporal how-to is listed (not read).
- https://encore.dev/docs/go/primitives/cron-jobs

## 4.6 i18n or localised error messages

Not found. `errs.Error` has `Code`, `Message`, `Details`, `Meta`; the validation page says `Validate() error` results become `InvalidArgument` (HTTP 400) unless already an `*errs.Error`, and neither page mentions locales.
- https://encore.dev/docs/go/primitives/api-errors
- https://encore.dev/docs/go/develop/validation

## 4.7 Bodies other than JSON, and negotiation

Typed endpoints read and write JSON ("default to reading request parameters from the body (as JSON)"), with path (`:name`, `*name`), query (default for GET, HEAD, DELETE) and `header` field tags. Form, XML, protobuf, MessagePack, content negotiation: not found; raw endpoints (`//encore:api public raw`, `func(w http.ResponseWriter, req *http.Request)`) leave any other body to the handler.
- https://encore.dev/docs/go/primitives/defining-apis
- https://encore.dev/docs/go/primitives/raw-endpoints

## 4.8 HTTP/3 or QUIC

Not found in the docs (no Go page in the index mentions it).
- https://encore.dev/llms.txt (page index)

## 4.9 Database layer or code generation for models

Shipped: PostgreSQL through `encore.dev/storage/sqldb` (`sqldb.NewDatabase`, `sqldb.DatabaseConfig` with a `Migrations` directory of `.up.sql` files, `Exec`, `QueryRow`, `Scan`, `sqldb.ErrNoRows`). The page does not mention an ORM or model generation. How-to guides exist for Atlas + GORM and for ent + Atlas (not read). Object storage and caching are separate primitives in the index.
- https://encore.dev/docs/go/primitives/databases
- https://encore.dev/llms.txt (how-to list)

## 4.10 Health, shutdown, tracing, metrics

- Health: not found in the Go docs. https://encore.dev/llms.txt
- Graceful shutdown: a service struct (`//encore:service`) may define `func (s *Service) Shutdown(force context.Context)`; Encore waits indefinitely for it to return; the infra config has a `graceful_shutdown` section (`total`, `shutdown_hooks`, `handlers`, seconds). https://encore.dev/docs/go/primitives/service-structs and https://encore.dev/docs/self-host/configure-infra
- Tracing: automatic for the whole application in all environments, viewed in the local and platform dashboards; the page does not mention OpenTelemetry or W3C trace context. https://encore.dev/docs/go/observability/tracing
- Metrics: custom `metrics.NewCounter`, `metrics.NewGauge`, `metrics.NewCounterGroup`, `metrics.NewGaugeGroup` with struct labels; the infra config exports to `prometheus` (`remote_write_url`), `datadog`, `gcp_cloud_monitoring`, `aws_cloudwatch`. https://encore.dev/docs/go/observability/metrics and https://encore.dev/docs/self-host/configure-infra
- Logging: `encore.dev/rlog`. https://encore.dev/docs/go/observability/logging

# 5. Comparison

| Topic | Kratos | go-zero | go-micro | Encore |
|---|---|---|---|---|
| 1. Registration and discovery | Yes: `registry.Registrar` / `Discovery`; Consul, etcd, Eureka, Kubernetes, Nacos, Polaris, ServiceComb, ZooKeeper | Yes: `zrpc` with etcd, Kubernetes DNS, static endpoints | Yes: `registry`; mDNS (default), Consul, etcd, NATS | Implicit: services are packages, calls compiled; no registry choice found; `service_discovery` base URLs for self-host |
| 2. Configuration | Yes: `config.New`, `Scan` into struct; file, env, remote (Consul, etcd, Nacos, Apollo, Kubernetes, Polaris); flag not found | Yes: `conf.MustLoad` into struct; yaml, json, toml; env tag and `UseEnv`; etcd config centre | Partly: `config` package with `Scan`, sources cli/env/file/flag/nats, json encoder (repo, not docs); service wiring by `MICRO_*` env vars | Yes: `config.Load[T]` from CUE files, `secrets` struct; env and flags not found |
| 3. gRPC, one IDL, codegen | Yes: HTTP and gRPC from one `.proto` (`google.api.http`); codegen is the documented path | Both, from separate `.api` and `.proto`; gateway maps HTTP to gRPC via `ProtoSets`; goctl | Yes: `server/grpc`, `client/grpc`; no HTTP from IDL found; codegen not required | No: gRPC only via Connect behind a raw endpoint; generation outside Encore |
| 4. Pub/sub consumers | Not found (roadmap ticks Kafka; no package) | Yes: `kq` Kafka, `dq` Beanstalkd; handlers untyped (`Consume(key, val string)`, `[]byte`); RabbitMQ by plugin | Yes: `broker` (HTTP, NATS, RabbitMQ, memory); server subscribers take a typed argument by reflection | Yes: `pubsub.NewTopic[T]` / `NewSubscription`, typed handlers; cloud and self-host backends GCP, SNS/SQS, NSQ |
| 5. Jobs, cron | Not found | Kubernetes CronJob guide only; Beanstalkd delay queue | Only `agent.ScheduleTask` and `flow.Scheduled` in source | Yes: `cron.NewJob` |
| 6. i18n | Not found | Not found | Not found | Not found |
| 7. Non-JSON bodies, negotiation | Yes: codecs json, protojson, proto, xml, yaml, form (msgpack in contrib listing); `Content-Type` and `Accept` | Partly: `form`, `json`, `path`, `header` tags; XML response via `zeromicro/x`; no negotiation found | Partly: RPC codecs by content type (protobuf, json, json-rpc, grpc, octet-stream); no XML, YAML, form found | No: JSON typed endpoints; raw endpoints for the rest |
| 8. HTTP/3, QUIC | Not found | Not found | No: roadmap "HTTP/3 transport" (later, exploratory) | Not found |
| 9. Database layer, model codegen | No layer; Ent in the template, schema-driven generation | Yes: `core/stores` (`sqlx`, `mon`, `redis`); `goctl model` for MySQL, PostgreSQL, MongoDB | No ORM; `store` key-value (memory, file, MySQL, Postgres, JetStream KV) | Yes: `sqldb` (PostgreSQL) with migrations; no ORM or codegen on the page |
| 10. Health, shutdown, tracing, metrics | gRPC health service; signal shutdown with `StopTimeout`; OTel tracing and metrics in contrib | `/healthz` on the dev server; `proc.ShutdownConf`; OTel tracing; Prometheus | `health` package with `/health`, `/health/live`, `/health/ready`; `BeforeStop`; OTel and Prometheus wrappers exist in repo, docs say roadmap | Not found (health); `Shutdown(force)` method; automatic tracing; `metrics.NewCounter` |

# 6. Gaps

- Kratos: the `/docs/component/transport/http/` page was summarised only for the topics asked; the middleware pages other than tracing were not read; `https://go-kratos.dev/en/docs/` returned 404 (the site is under `/docs/`). Pub/sub is absent from the docs and from the repo listings; only the ROADMAP ticks it.
- go-zero: `https://go-zero.dev/en/docs/introduction` returned 404 (the site is at the root). The `/guides/http/file-upload/`, `/guides/mcp/*`, `/guides/gateway/grpc/`, `/guides/microservice/load-balancing/` and `/examples/microservice-system/` pages were not read. Health and shutdown come from source files, not docs pages.
- go-micro: the docs site marks `/docs/interfaces/*`, `/docs/store.html`, `/docs/observability.html`, `/docs/server.html`, `/docs/client-server.html` and the real-world examples as older documentation. `/docs/reference.html` lists packages only. `/docs/guides/error-handling.html`, `/docs/guides/durability.html` and `/docs/interfaces/transport.html` were not read. Keyword checks on the repository were cut short by the GitHub rate limit.
- Encore: no Go page for health, i18n, HTTP/3, WebSockets or streaming appears in the page index; the pub/sub page does not say which backend runs locally; the Encore services and api-calls pages do not explain discovery or the transport of inter-service calls; the Atlas/GORM, ent, Temporal and outbox how-tos were not read.
