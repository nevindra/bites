# Tags another framework reads: ignored before, refused after

Taken on 2026-11-03, Go 1.27.3 linux/amd64, AMD Ryzen 7 9700X. The before is commit `062ddaa`, built in a temporary worktree in the same session; the after is the tree that adds `tags.go` (ADR 0021, ADR 0011). This is a behaviour check, not a measurement, so the machine does not matter.

## What was run

`probe/probe_test.go` registers `POST /users/{id}` with an Input whose fields carry the tags a Gin, Fiber or Huma user writes: `path:"id" validate:"min=1"` on the path field, `binding:"required,min=3" validate:"required,min=3,max=40"` on `Body.Name`, `validate:"required,email"` on `Body.Email`. If Registration accepts it, the probe sends `POST /users/0` with `{"name":"a","email":"not-an-email"}` through `nilotest`. Run with `go test -v -count=1 .` in `probe/`; its `go.mod` points at the repository root, so for the before, point the `replace` at a worktree of `062ddaa`.

## Result

| Tree | Registration | The request |
|---|---|---|
| `062ddaa` (before) | accepted | `201`, body `{"email":"not-an-email","name":"a"}` |
| with `tags.go` (after) | refused: `POST /users/{id}: CreateUser.Path.ID has a validate tag, which nilo-go does not read, so its rules would never run: give the field a type of your own with a rule method (Length, Range, Items, Pattern, Format, Check), or run your validator in CreateUser.Validate` | not sent |

Every rule the tags state was broken by the request (`id` 0, a one-character name, an address with no `@`) and the handler ran with it. After, the first foreign tag the walk meets is named, Path before Body, and the route never serves.
