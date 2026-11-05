# brotli

**The module that gives response compression the `br` content coding, at level 1 of the matchfinder writer.**

**Guide:** [responses](../guide/responses.md) · **Design:** [responses](../design/responses.md)

`github.com/nevindra/bites/modules/brotli` is a module of its own ([ADR 0070](../adr/0070-optional-features-are-modules-under-modules-each-with-its-own-go-mod.md)), so the core takes none of its dependency, `github.com/andybalholm/brotli`. Its names are not checked by the documentation tests, which read the root, `bitetest` and `stdhttp`; its own tests are run with `cd modules/brotli && go test ./...`. The decision is [ADR 0061](../adr/0061-compression-chooses-among-the-apps-encoders-by-the-clients-weights-then-the-apps-order.md), its measurements in [`docs/research/encoders/`](../research/encoders/).

```go
app := bites.New(bites.WithCompression(bites.Compression{
	Encoders: []bites.Encoder{brotli.Encoder(), zstd.Encoder(), bites.GzipEncoder()},
}))
```

### `brotli.Encoder`

```go
func Encoder() bites.Encoder
```

Encoder is the brotli [`Encoder`](./app.md#encoder) for [`Compression`](./app.md#compression): level 1 of the matchfinder writer, one whole stream per answer. Its coding is `br`. Its writers are pooled and never reach user code ([ADR 0008](../adr/0008-no-pooled-object-reaches-the-user.md)), and a pooled writer keeps no reference to the caller's buffer; about GOMAXPROCS of them are live at once, and the garbage collector drops the ones left idle. Every call returns the same Encoder, so every app in the process shares one pool.

### `brotli.Level`

```go
const Level = 1
```

Level is the brotli level: 1, of the matchfinder writer (`NewWriterV2`). It took 1.05 to 1.14 times the CPU of gzip at `BestSpeed` and made answers 14 to 42% smaller than gzip's on the JSON of [`docs/research/encoders/`](../research/encoders/); the classic level 4 took 4 to 7 times gzip's CPU. An app that wants another level writes its own `Encoder`.
