# What other Go frameworks have that nilo-go does not

Taken on 2026-11-03. Each framework's features were read from its own documentation, not recalled: one inventory a framework, every line closing with the URL of the page it came from `[docs]`. nilo-go's side was read from this repository on the same day (`docs/reference`, `docs/guide`, the ADRs and `go doc`) `[code]`.

| file | framework | version the docs showed |
|---|---|---|
| [`fiber.md`](fiber.md) | Fiber, gofiber/contrib, gofiber/storage | v3 |
| [`gin.md`](gin.md) | Gin and gin-contrib | v1.12.0 |
| [`echo.md`](echo.md) | Echo and echo-contrib | v5.4.2 on the site, v4.16.3 on pkg.go.dev |
| [`chi-stdlib.md`](chi-stdlib.md) | chi v5 and the go-chi repositories, net/http server side | Go 1.22 to 1.26 release notes, pkg.go.dev at Go 1.27.3 |
| [`huma.md`](huma.md) | Huma and its packages | v2.40.0 |
| [`hertz-fuego.md`](hertz-fuego.md) | Hertz and hertz-contrib, Fuego | current docs |
| [`microservices.md`](microservices.md) | Kratos, go-zero, go-micro, Encore, read on 2026-11-04 for what a service among several needs | current docs and default branches, no commit pinned |

## What the inventories could not say

- Some pages were truncated when read, so a few methods are listed by name only: Fiber's `api/ctx` (read from the Markdown the site is built from), Huma's `Config.CreateHooks` and `ContentTypeFilter`, the end of Fuego's pkg.go.dev page, some Echo v4 symbols (`Host`, `Reverse`, `Routes`).
- Three gin-contrib READMEs (csrf, openmetrics, jwt) returned 404 and are listed only as Gin's own docs mention them.
- Hertz has no usage page for HTTP/3, and its `hertz-contrib/http3` repository is archived.

## The result

Each gap found is a line in [the roadmap](../../roadmap.md), naming the frameworks that have it and the ADR it touches, under Important, Fairly important or Nice to have (each with its trigger; the tiers were Now, Later and Nice to have until 2026-11-04), by how many applications need it ([ADR 0002](../../adr/0002-the-reference-workload.md)); a feature left out on purpose is under [Not goals](../../roadmap.md#not-goals) with the ADR that decided it.
