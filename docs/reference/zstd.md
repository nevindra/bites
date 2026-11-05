# zstd

**The module that gives response compression the `zstd` content coding, at the fastest level with a 1 MiB window.**

**Guide:** [responses](../guide/responses.md) · **Design:** [responses](../design/responses.md)

`github.com/nevindra/bites/modules/zstd` is a module of its own ([ADR 0070](../adr/0070-optional-features-are-modules-under-modules-each-with-its-own-go-mod.md)), so the core takes none of its dependency, `github.com/klauspost/compress/zstd`. Its names are not checked by the documentation tests, which read the root, `bitetest` and `stdhttp`; its own tests are run with `cd modules/zstd && go test ./...`. The decision is [ADR 0061](../adr/0061-compression-chooses-among-the-apps-encoders-by-the-clients-weights-then-the-apps-order.md), its measurements in [`docs/research/encoders/`](../research/encoders/).

```go
app := bites.New(bites.WithCompression(bites.Compression{
	Encoders: []bites.Encoder{brotli.Encoder(), zstd.Encoder(), bites.GzipEncoder()},
}))
```

### `zstd.Encoder`

```go
func Encoder() bites.Encoder
```

Encoder is the zstd [`Encoder`](./app.md#encoder) for [`Compression`](./app.md#compression): `SpeedFastest`, a 1 MiB window, a content checksum, and one frame per answer. Its coding is `zstd`. Its states are pooled and never reach user code ([ADR 0008](../adr/0008-no-pooled-object-reaches-the-user.md)); about GOMAXPROCS of them are live at once, each about 1.56 MB after a 64 KiB answer, and the garbage collector drops the ones left idle. Every call returns the same Encoder, so every app in the process shares one pool.

### `zstd.Window`

```go
const Window = 1 << 20
```

Window is the window of every frame: 1 MiB. RFC 9659 lets a zstd content coding use at most 8 MiB; the smaller window keeps a state at about 1.5 MB instead of 9 MB. An answer longer than the window is still compressed, its matches reaching back at most 1 MiB.
