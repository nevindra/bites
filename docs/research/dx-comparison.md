# nilo-go dibanding framework Go lain: sintaks dan DX, per skenario

Ditulis 2026-11-02, di commit `3460aab`. Yang dibandingkan adalah versi yang sudah dibaca dan diprobe di folder ini: Gin v1.12.0 dan Echo v5.4.4 ([`gin-echo/`](gin-echo/report.md)), Fiber v3.6.1 ([`fiber/`](fiber/report.md)), net/http Go 1.27, chi v5.5.0, Huma v2.40.0 dan Fuego v0.21.0 ([`std-huma/`](std-huma/report.md)).

**Sesudah dokumen ini ditulis** (masih 2026-11-02): `Bounded[R]` diganti method `Length`/`Range`/`Check` di tipe bernama, `OneOf2..4` menggantikan `any` + `Returns`, `Events[T]` menambah SSE bertipe, dan `Upload.Each` memendekkan upload. Skenario 2, 7, 10 dan 11 di bawah menggambarkan keadaan di `3460aab`, sebelum perubahan itu.

**Asal setiap klaim.** Kode nilo-go diambil dari `docs/guide/`, yang blok kodenya dikompilasi oleh `docs_test.go`. Perilaku framework lain yang ditandai **[probe]** berasal dari program yang dijalankan di laporan-laporan di atas. Potongan kode framework lain ditulis dari dokumentasi masing-masing dan **tidak dikompilasi di sini**, jadi bentuknya bisa sedikit meleset dari API persisnya. Penilaian "enak" atau "tidak enak" adalah pendapat, bukan hasil studi pengguna.

## Ringkasan

| # | Skenario | Paling enak | Posisi nilo-go |
|---|---|---|---|
| 1 | Path dan query param bertipe | nilo-go | unggul: tanpa tag, tanpa parse manual |
| 2 | Body JSON, validasi, 201 + Location | nilo-go ≈ Huma | setara; nilo-go lebih ringkas, Huma punya validasi format yang lebih kaya |
| 3 | Apa yang client terima saat input salah | nilo-go ≈ Huma | unggul tipis: satu status (400), nama wire, nilai tidak dipantulkan |
| 4 | Route dan tipe tidak cocok | nilo-go | unggul jelas: satu-satunya yang menolak sebelum request pertama |
| 5 | Error bisnis (404, 409) dan error tak dikenal | nilo-go ≈ Huma ≈ Echo | setara |
| 6 | Validasi lintas field | seri | setara; Gin bisa lewat tag `gtfield` |
| 7 | Header, cookie, beberapa bentuk jawaban | Huma | kalah tipis: harus `any` + `Returns[...]` |
| 8 | Auth dan "user yang sedang login" | nilo-go | unggul: Provider bertipe, dicek saat Registration, masuk OpenAPI |
| 9 | Grup route dan middleware | seri | setara; middleware nilo-go tidak bisa menulis dua kali |
| 10 | Upload file | Gin (ringkas) / nilo-go (aman) | lebih panjang, tapi streaming dan batasnya wajib |
| 11 | Server-Sent Events | Huma | setara; nilo-go `Data` masih string |
| 12 | WebSocket | nilo-go | unggul jelas: satu-satunya yang punya bawaan dan bertipe |
| 13 | Dokumen OpenAPI | nilo-go ≈ Huma | setara; keduanya dari tipe |
| 14 | Testing | nilo-go | unggul: in-memory, jam palsu, WebSocket, satu test menangkap semua refusal |
| 15 | Data yang dibaca dari goroutine | nilo-go ≈ Huma ≈ net/http | Gin, Echo dan Fiber bisa membaca data request lain **[probe]** |
| 16 | Ekosistem dan interop `net/http` | Gin / chi / Huma | kalah jelas |
| 17 | Kematangan dan syarat | Gin / chi | kalah jelas: Go 1.27, v0.1.0 |

Kesimpulan singkat: untuk API JSON bertipe, nilo-go paling sedikit kode dan paling cepat menangkap kesalahan. Kekalahannya ada di ekosistem, kematangan, dan beberapa tempat di mana sistem tipe Go memaksa bentuk yang kaku (skenario 7 dan 10, serta `Bounded[R]` di skenario 2).

---

## 1. Path dan query param bertipe

`GET /users/{id}?expand=true`, `id` int64, `expand` bool opsional.

**Gin** (Echo dan Fiber sebentuk):

```go
r.GET("/users/:id", func(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(400, gin.H{"error": "id must be an integer"})
		return
	}
	expand, _ := strconv.ParseBool(c.DefaultQuery("expand", "false"))
	user := find(id, expand)
	c.JSON(200, user)
})
```

**net/http** sama saja, dengan `r.PathValue("id")` dan `json.NewEncoder(w).Encode(user)`.

**Huma**:

```go
type GetUserInput struct {
	ID     int64 `path:"id"`
	Expand bool  `query:"expand"`
}
type GetUserOutput struct{ Body User }

huma.Get(api, "/users/{id}", func(ctx context.Context, in *GetUserInput) (*GetUserOutput, error) {
	return &GetUserOutput{Body: find(in.ID, in.Expand)}, nil
})
```

**nilo-go**:

```go
type GetUser struct {
	Path  struct{ ID int64 }
	Query struct{ Expand nilogo.Opt[bool] }
}

func getUser(ctx context.Context, in GetUser) (User, error) {
	return find(in.Path.ID, in.Query.Expand.Or(false)), nil
}

app.Get("/users/{id}", getUser)
```

**Catatan.**
- Echo v5 punya `echo.PathParam[int64](c, "id")` dan Fiber v3 punya `fiber.Params[int64](c, "id")`, tapi itu cuma helper parse per nama string. Di Fiber, kalau parse gagal hasilnya diam-diam jadi nol **[code, fiber/report.md §3]**.
- Fuego: `c.PathParamIntErr("id")`. Varian tanpa `Err` mengembalikan 0 untuk `abc` dan tetap menjawab 200 **[probe]**.
- Di nilo-go tidak ada tag sama sekali. Asal nilai diambil dari nama Section, dan nama wire-nya (`id`, `expand`) dari satu kebijakan penamaan untuk seluruh app (default snake_case). Di Huma, tag `path:"id"` adalah string yang tidak dicek terhadap pattern (lihat skenario 4).
- Harga di nilo-go: `in.Path.ID` lebih panjang satu tingkat daripada `in.ID` di Huma, dan field opsional dibaca lewat `.Or(...)` atau `.Get()`.

## 2. Body JSON, validasi, 201 dengan Location

`POST /users` dengan `name` wajib dan tidak kosong serta `email` opsional.

**Gin**:

```go
type CreateUserReq struct {
	Name  string `json:"name"  binding:"required"`
	Email string `json:"email" binding:"omitempty,email"`
}

r.POST("/users", func(c *gin.Context) {
	var req CreateUserReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	u := store.Create(req.Name, req.Email)
	c.Header("Location", fmt.Sprintf("/users/%d", u.ID))
	c.JSON(201, u)
})
```

**Huma**:

```go
type CreateUserInput struct {
	Body struct {
		Name  string `json:"name" minLength:"1"`
		Email string `json:"email,omitempty" format:"email"`
	}
}
type CreateUserOutput struct {
	Location string `header:"Location"`
	Body     User
}

huma.Register(api, huma.Operation{
	Method: http.MethodPost, Path: "/users", DefaultStatus: http.StatusCreated,
}, func(ctx context.Context, in *CreateUserInput) (*CreateUserOutput, error) {
	u := store.Create(in.Body.Name, in.Body.Email)
	return &CreateUserOutput{Location: fmt.Sprintf("/users/%d", u.ID), Body: u}, nil
})
```

**nilo-go**:

```go
type CreateUser struct {
	Body struct {
		Name  nilogo.NonEmpty
		Email nilogo.Opt[string]
	}
}

func (s *Users) Create(ctx context.Context, in CreateUser) (nilogo.Created[User], error) {
	u := s.store.Create(string(in.Body.Name), in.Body.Email.Or(""))
	return nilogo.Created[User]{Body: u, Location: fmt.Sprintf("/users/%d", u.ID)}, nil
}

app.Post("/users", users.Create)
```

**Catatan.**
- Wajib atau opsional di nilo-go adalah tipe: field biasa wajib, `Opt[T]` boleh tidak ada, `Null[T]` boleh `null`. Di Gin itu tag `binding:"required"`. Di Huma, field body wajib kecuali ada `omitempty` atau `required:"false"`.
- Field yang tidak dikenal ditolak secara default di nilo-go (`AcceptUnknownMembers()` untuk melonggarkan) dan di Huma **[probe]**. Gin menerimanya.
- **Di sini nilo-go kalah:** constraint bawaannya cuma `NonEmpty`, `PositiveInt`, `PositiveInt64` dan `Bounded[R]`. Tidak ada `format:"email"`, `pattern`, atau `enum` dalam bentuk tag. Aturan seperti itu ditulis di method `Validate()`. Batas panjang juga harus lewat tipe kosong:

```go
type RoomName struct{}
func (RoomName) Range() (min, max int) { return 3, 40 }

Body struct{ Room nilogo.Bounded[RoomName] }   // vs Huma: `minLength:"3" maxLength:"40"`
```

  Cara ini lebih aman dari typo dan nilainya dicek saat Registration, tapi jelas lebih bertele-tele daripada satu tag.

## 3. Apa yang client terima saat input salah

Ini tidak perlu ditulis di handler. Yang diukur adalah jawaban default framework untuk `GET /users/abc` dan body dengan tipe salah (`{"age":"old"}`).

| Framework | Status | Bentuk jawaban |
|---|---|---|
| Gin | terserah handler | teks error mentah dari library: `strconv.ParseInt: parsing "abc": invalid syntax` (tanpa nama field), `Key: 'J.Name' Error:Field validation for 'Name' failed on the 'required' tag` **[probe]** |
| Echo v5 | 400 | `{"message":"Bad Request"}`; field yang salah disembunyikan **[probe]** |
| Fiber v3 | **500** secara default | `bind "id" from uri: schema: error converting value for "id"`; 400 teks polos hanya dengan `WithAutoHandling()` **[probe]** |
| Fuego | 422 / 400 | membocorkan detail Go: `json: cannot unmarshal string into Go struct field FIn.age of type int` **[probe]** |
| Huma | 422 (400 untuk JSON rusak) | problem+json, semua error sekaligus, `location: "path.id"`, dan memantulkan `value` yang dikirim client **[probe]** |
| nilo-go | 400 | problem+json, semua error sekaligus, `location` dengan nama wire, dan nilai kiriman client tidak pernah dipantulkan |

```http
GET /notes/abc?shout=maybe HTTP/1.1

HTTP/1.1 400 Bad Request
Content-Type: application/problem+json

{"title":"Bad Request","status":400,"errors":[{"location":"path.id","message":"expected an integer"},{"location":"query.shout","message":"expected true or false"}]}
```

**Catatan.** Huma dan nilo-go sama-sama bagus di sini. Bedanya: nilo-go memakai satu status untuk semua input salah (Huma memakai dua, 400 dan 422), dan tidak memantulkan nilai yang dikirim client.

## 4. Route dan tipe tidak cocok

Pattern-nya `/users/{id}`, tapi struct input lupa field-nya, atau namanya salah.

| Framework | Yang terjadi |
|---|---|
| Gin / Echo | `c.Param("idd")` mengembalikan string kosong, lalu parse gagal atau nilainya nol saat runtime |
| Fiber | `fiber.Params[int](c, "idd")` mengembalikan nol atau nilai default |
| Fuego | `c.PathParamInt("idd")` mengembalikan 0 dan menjawab 200 **[probe]** |
| Huma | tidak panic; handler jalan dengan `ID` = 0 dan menjawab 200, termasuk kalau tagnya lupa ditulis **[probe]** |
| nilo-go | **panic saat Registration**, menyebut route, field, dan file:line |

```text
panic: GET /notes/{id}: the pattern has {id}, but GetNote.Path has no field for it
	registered at main.go:14
```

Cukup satu test yang membangun app, maka setiap kesalahan seperti ini gagal di `go test`:

```go
func TestTheAppRegisters(t *testing.T) { newApp() }
```

Hal yang sama berlaku untuk field pointer, `Body` di route `GET`, dua pattern yang bentrok, `Upload` tanpa `MaxBody`, Provider yang tidak ada, dan dua field dengan nama wire yang sama. **Ini pembeda terbesar nilo-go.** Nol yang kebetulan juga nilai valid adalah kegagalan terburuk, dan semua framework lain di tabel membiarkannya lolos ke production.

## 5. Error bisnis dan error tak dikenal

**Gin**:

```go
u, err := store.Find(id)
if errors.Is(err, ErrNoRow) {
	c.AbortWithStatusJSON(404, gin.H{"error": "no such user"})
	return // lupa return: kode setelahnya tetap jalan dan tetap menulis [probe]
}
```

**Echo**: `return echo.NewHTTPError(404, "no such user")`. Error tak dikenal jadi 500 tanpa pesan **[probe]**.

**Fiber**: `return fiber.NewError(404, "no such user")`. Error tak dikenal jadi 500 **dengan `err.Error()` di body** (contohnya `"boom secret"`) **[probe]**.

**Huma**: `return nil, huma.Error404NotFound("no such user")`.

**nilo-go**:

```go
if errors.Is(err, errNoRow) {
	return User{}, nilogo.NotFound("no such user")
}

// tambah member di problem+json
return nilogo.NoContent{}, nilogo.Conflict("this order is already paid").With("code", "order_paid")

// dibungkus %w tetap ditemukan; teks "load order 7" tidak sampai ke client
return nilogo.NoContent{}, fmt.Errorf("load order %d: %w", id, nilogo.NotFound("no such order"))
```

**Catatan.** Echo, Huma dan nilo-go setara di sini: error dikembalikan sebagai nilai, dan error tak dikenal jadi 500 yang aman. Gin yang paling rawan, karena `c.Error(err)` hanya dibaca kalau middleware Logger terpasang **[probe]**. Di nilo-go handler tidak pernah memegang writer, jadi kesalahan "lupa `return` lalu menulis dua kali" tidak bisa terjadi.

## 6. Validasi lintas field

"`end` harus setelah `start`".

**Gin**: `End time.Time `binding:"gtfield=Start"``, atau dicek manual di handler.

**Huma**:

```go
func (b *BookingInput) Resolve(ctx huma.Context) []error {
	if !b.Body.End.After(b.Body.Start) {
		return []error{&huma.ErrorDetail{Location: "body.end", Message: "must be after start", Value: b.Body.End}}
	}
	return nil
}
```

**nilo-go**:

```go
func (b Booking) Validate() error {
	var errs []error
	if !b.Body.End.After(b.Body.Start) {
		errs = append(errs, nilogo.Invalid("body.end", "must be after body.start"))
	}
	return errors.Join(errs...)
}
```

**Catatan.** Huma dan nilo-go hampir sama. Error dari `Validate` masuk ke 400 yang sama dengan error decoding. Tag `gtfield` di Gin lebih ringkas, tapi berbentuk string dan pesannya mentah (lihat skenario 3).

## 7. Header, cookie, dan beberapa bentuk jawaban

`GET /docs/{id}` dengan `If-None-Match`: jawab 304 kalau ETag sama, kalau tidak 200 dengan `ETag` dan `Cache-Control`.

**Gin**:

```go
if c.GetHeader("If-None-Match") == etag {
	c.Status(304)
	return
}
c.Header("ETag", etag)
c.Header("Cache-Control", "max-age=60")
c.JSON(200, doc)
c.SetCookie("sid", "abc123", 86400, "/", "", true, true) // 7 argumen posisional; ini header, dan header yang di-set setelah body diam-diam hilang [probe, untuk c.Header]
```

**Huma** (satu tipe output dengan field `Status`):

```go
type GetDocOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         *Doc
}
```

**nilo-go**:

```go
func getDoc(ctx context.Context, in GetDoc) (any, error) {
	const etag = `"v1"`
	if in.Header.IfNoneMatch.Or("") == etag {
		return nilogo.NotModified{Header: nilogo.Header{"ETag": {etag}}}, nil
	}
	h := nilogo.Header{}
	h.Set("ETag", etag)
	h.Set("Cache-Control", "max-age=60")
	return nilogo.OK[Doc]{Body: Doc{ID: in.Path.ID, Title: "Plan"}, Header: h}, nil
}

app.Get("/docs/{id}", getDoc,
	nilogo.Returns[nilogo.OK[Doc]](),
	nilogo.Returns[nilogo.NotModified](),
)
```

Cookie di nilo-go berupa struct dengan default yang aman (`HttpOnly`, `Secure`, `SameSite=Lax`, `Path=/`). Untuk melonggarkannya harus ditulis eksplisit:

```go
return nilogo.SeeOther{Location: "/home", Cookies: []nilogo.Cookie{
	{Name: "sid", Value: "abc123", MaxAge: 24 * time.Hour},
	{Name: "flash", Value: "welcome", MaxAge: 5 * time.Second, NotHttpOnly: true},
}}, nil
```

**Catatan.** Header input di nilo-go juga tanpa tag: `Header struct{ IfNoneMatch nilogo.Opt[string] }` membaca `If-None-Match`. **Di sini nilo-go kalah tipis:** handler yang punya lebih dari satu bentuk jawaban harus mengembalikan `any` dan mendaftarkan setiap bentuk dengan `Returns[...]`, sehingga compiler tidak lagi menjaga tipe output-nya. Model Huma (satu struct output dengan `Status` dan field header) lebih sederhana untuk kasus ini.

## 8. Auth dan "user yang sedang login"

**Gin**:

```go
func Auth(store Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		u, ok := store.ByToken(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
		if !ok {
			c.AbortWithStatusJSON(401, gin.H{"error": "unauthorized"})
			return
		}
		c.Set("user", u)
		c.Next()
	}
}

func me(c *gin.Context) {
	u := c.MustGet("user").(User) // panic saat runtime kalau middleware lupa dipasang
	c.JSON(200, u)
}
```

**net/http / chi / Huma**: polanya sama, dengan `context.WithValue` (Huma: `huma.WithValue`) dan type assertion di handler. Di Huma, security scheme di OpenAPI juga dideklarasikan terpisah.

**nilo-go**:

```go
func bearerUsers(accounts Accounts) func(context.Context, nilogo.Request, struct{}) (User, error) {
	return nilogo.BearerAuth(func(ctx context.Context, token string) (User, error) {
		u, ok, err := accounts.ByToken(ctx, token)
		switch {
		case err != nil:
			return User{}, err // 500
		case !ok:
			return User{}, nilogo.ErrInvalidCredentials // 401, error="invalid_token"
		}
		return u, nil
	})
}

type Profile struct{ User User }

func profile(ctx context.Context, in Profile) (User, error) { return in.User, nil }

app.Provide(bearerUsers(accounts))
app.Get("/me", profile)
```

**Catatan.**
- "Handler ini butuh user" tertulis di tipe input. Kalau Provider untuk `User` tidak ada, atau graf Provider melingkar, app panic saat Registration, bukan menghasilkan nil user di production.
- Auth opsional cukup dengan `Reader nilogo.Opt[User]`. Beberapa skema sekaligus dengan `nilogo.AnyAuth(bearer, apiKey)`. JWT tersedia di `modules/jwt`.
- Dokumen OpenAPI menurunkan `securitySchemes`, `security` per operasi, dan respons 401 dari Provider yang dipakai route. Tidak ada yang dideklarasikan dua kali.
- Harga: signature Provider yang ditulis sendiri cukup panjang. Contohnya `func(context.Context, nilogo.Request, struct{ Credential Credential }) (User, error)`, sering dibungkus closure untuk membawa dependensi.

## 9. Grup route dan middleware

**Gin**: `api := r.Group("/api", audit)`, dengan middleware `func(c *gin.Context)`. `c.Abort()` tidak menghentikan sisa kode di handler yang sama **[probe]**.

**chi**: `r.Route("/api", func(r chi.Router) { r.Use(audit); ... })`, dengan middleware `func(http.Handler) http.Handler`.

**Huma**: `grp := huma.NewGroup(api, "/api")`, dengan middleware `func(ctx huma.Context, next func(huma.Context))`.

**nilo-go**:

```go
api := app.Group("/api", nilogo.MaxBody(64<<10))
admin := api.Group("/admin", nilogo.Use(audit), nilogo.MaxBody(1<<20), nilogo.Tag("admin"))
admin.Get("/users", listUsers)

func requireToken(token string) nilogo.Middleware {
	return func(ctx context.Context, req nilogo.Request, next nilogo.Next) (nilogo.Response, error) {
		if req.Header("Authorization") != "Bearer "+token {
			return nilogo.Problem(401, "missing or wrong token"), nil
		}
		return next.Call(ctx), nil
	}
}
```

**Catatan.** Secara ringkas semuanya setara. Bedanya, middleware nilo-go melihat request dalam bentuk read-only dan mengembalikan `Response` sebagai nilai, jadi tidak ada "header ditambah setelah body sudah terkirim" atau "menulis dua kali". Batas body, tag OpenAPI, dan middleware dipasang dengan opsi yang sama di grup maupun route. Harga: bentuk middleware ini khas nilo-go, sehingga middleware `net/http` yang sudah ada tidak bisa dipakai langsung (skenario 16).

## 10. Upload file

**Gin** (ringkas, tapi file di-buffer ke memori sampai 32 MiB lalu ke disk sementara, dan batas body harus dipasang manual):

```go
fh, err := c.FormFile("avatar")
if err != nil { c.JSON(400, gin.H{"error": err.Error()}); return }
c.SaveUploadedFile(fh, dst)
```

**Huma**: `RawBody huma.MultipartFormFiles[struct{ Avatar huma.FormFile `form:"avatar"` }]`.

**nilo-go** (streaming, part demi part):

```go
type SetAvatar struct {
	Path struct{ ID int64 }
	Body nilogo.Upload
}

func setAvatar(ctx context.Context, in SetAvatar) (nilogo.NoContent, error) {
	for {
		part, err := in.Body.Next()
		if err == io.EOF {
			return nilogo.NoContent{}, nil
		}
		if err != nil {
			return nilogo.NoContent{}, err // sudah berupa 400/408/413/431
		}
		if part.Name != "avatar" {
			continue
		}
		if _, err := io.Copy(dst, part); err != nil {
			return nilogo.NoContent{}, err
		}
	}
}

upload := app.Group("/uploads", nilogo.MaxBody(2<<20)) // tanpa MaxBody: ditolak saat Registration
upload.Post("/users/{id}/avatar", setAvatar, nilogo.MaxParts(4), nilogo.MaxInFlight(16))
```

**Catatan.** Untuk satu file kecil, Gin jauh lebih ringkas. nilo-go menukar keringkasan itu dengan streaming tanpa file sementara, batas yang wajib ditulis, dan error yang sudah berstatus. Untuk form HTML biasa tanpa file, nilo-go ringkas: `Body nilogo.Form[SignupForm]`.

## 11. Server-Sent Events

**Gin**:

```go
c.Stream(func(w io.Writer) bool {
	c.SSEvent("tick", Tick{N: n})
	return true
})
```

**Huma** (event bertipe, dan masuk ke OpenAPI):

```go
sse.Register(api, huma.Operation{Method: http.MethodGet, Path: "/events"},
	map[string]any{"tick": Tick{}},
	func(ctx context.Context, in *struct{}, send sse.Sender) {
		send.Data(Tick{N: 1})
	})
```

**nilo-go**:

```go
func feed(ctx context.Context, in Feed) (nilogo.SSE, error) {
	return nilogo.SSE{Func: func(ctx context.Context, e nilogo.EventWriter) error {
		body, _ := json.Marshal(Tick{N: 1})
		return e.Send(nilogo.Event{ID: "1", Name: "tick", Data: string(body)})
	}}, nil
}
```

**Catatan.** Bentuknya konsisten dengan sisa nilo-go (jawaban tetap datang dari nilai kembalian), dan context-nya dibatalkan saat client pergi atau server shutdown. Tapi `Data` masih string yang di-marshal sendiri. Huma lebih enak untuk event bertipe.

## 12. WebSocket

**Gin / Echo / chi / Huma**: tidak ada WebSocket bawaan. Pakai gorilla/websocket atau coder/websocket, lalu tulis sendiri upgrade, read loop di goroutine, mutex untuk tulis, ping/pong, cek Origin, dan broadcast.

**nilo-go**:

```go
type Chat struct{ room nilogo.Topic[Line] }

func (c *Chat) Join(ctx context.Context, in JoinChat, s *nilogo.Socket[Line]) (nilogo.Inbox[Said], error) {
	c.room.Subscribe(s)
	return nilogo.Inbox[Said]{
		OnMessage: func(ctx context.Context, m Said) error {
			return c.room.Publish(Line{From: in.User.Name, Text: m.Text})
		},
	}, nil
}

app.Provide(userFromHeader)
app.WebSocket("/chat", chat.Join)
```

**Catatan.** Pesan masuk dan keluar bertipe, broadcast lewat `Topic`, heartbeat dan close 1001 saat shutdown sudah ditangani, Origin dicek, dan Provider (misalnya user) bekerja sama seperti di route biasa. Dari semua skenario, di sinilah selisih jumlah kodenya paling besar.

## 13. Dokumen OpenAPI

| Framework | Cara |
|---|---|
| Gin / Echo | swaggo: komentar `// @Param id path int true "User ID"` lalu `swag init`, menghasilkan Swagger 2.0; komentar bisa basi relatif ke kode **[docs]** |
| Fiber | tidak ada first-party; contrib berbasis anotasi **[code]** |
| Fuego | dari tipe, tapi path param ditulis `string` walaupun handler mem-parse int, dan nama fungsi Go masuk ke description **[probe]** |
| Huma | dari tipe, OpenAPI 3.1, deskripsi lewat tag `doc:"..."`, `example:"..."`; menambah `$schema` ke setiap body secara default **[probe]** |
| nilo-go | dari tipe; ringkasan lewat opsi route, deskripsi field lewat method `Descriptions()` atau komentar Go via `go:generate` |

```go
app.OpenAPI("/openapi.json", nilogo.Info{Title: "Users", Version: "1.0.0"})
app.Get("/users/{id}", getUser, nilogo.Summary("Get a user"), nilogo.Tag("users"))

//go:generate go run github.com/nevindra/nilo-go/cmd/nilogo-doc
// ListQuery filters and pages a list of posts.
type ListQuery struct {
	// Page counts from 1.
	Page int
}
```

**Catatan.** Huma dan nilo-go sama-sama menurunkan dokumen dari tipe. Kelebihan nilo-go: security, 401, 429 dan 403 dari Provider dan middleware ikut tercatat otomatis, dan deskripsi bisa ditulis sebagai komentar Go biasa. Kelebihan Huma: tag `example:`, `enum:` dan `pattern:` lebih kaya.

## 14. Testing

**Gin / chi / Echo / net/http**: `httptest.NewRecorder()` lalu `r.ServeHTTP(w, req)`. Ini standar dan sudah cukup baik.

**Huma**: `_, api := humatest.New(t)` lalu `api.Get("/users/1")`.

**nilo-go**:

```go
func TestTheAppRegisters(t *testing.T) { newApp() } // semua refusal tertangkap di sini

func TestAPathIdThatIsNotANumberIsA400(t *testing.T) {
	srv := nilotest.Start(t, newApp()) // in-memory, tanpa port, lewat engine yang sama
	resp := srv.Do(nilotest.Request{Method: "GET", Target: "/users/abc"})
	if resp.Status != 400 { t.Fatal(resp.Text()) }
}

// waktu bisa dimajukan: timeout, rate limit, heartbeat WebSocket
clk := nilotest.NewClock()
srv := nilotest.Start(t, newApp(nilogo.WithClock(clk)))
clk.Advance(3 * time.Second)

// WebSocket juga bisa ditest
ws := srv.DialWS(nilotest.WSRequest{Target: "/echo"})
ws.SendJSON(Echo{Text: "hello"})
```

**Catatan.** Handler juga bisa dipanggil langsung sebagai fungsi biasa dengan sebuah struct input, tanpa membangun request. Harga: app nilo-go bukan `http.Handler`, jadi alat yang mengharapkan `http.Handler` (httptest, httpexpect) tidak bisa dipakai langsung.

## 15. Data yang dibaca dari goroutine

Handler menjalankan goroutine yang membaca param setelah handler selesai.

| Framework | Yang terjadi |
|---|---|
| Gin | goroutine dari request "first" membaca `Param="third"`; harus pakai `c.Copy()` **[probe]** |
| Echo v5 | sama, dan tidak ada `Copy()` **[probe]** |
| Fiber v3 | string yang ditangkap berubah jadi milik request berikutnya kecuali `Immutable`; slice body tetap berubah walaupun `Immutable` **[probe]** |
| net/http / chi / Huma | aman |
| nilo-go | aman: tidak ada objek pool yang sampai ke kode pengguna (ADR 0008). Satu-satunya batas: `Upload` part yang dipakai setelah handler selesai mengembalikan `ErrUploadClosed`, bukan data request lain |

Ini bukan soal sintaks, tapi jenis bug yang tidak terlihat saat menulis kode. Karena itu ia masuk hitungan DX.

## 16. Ekosistem dan interop `net/http`

- Gin, Echo, chi, Huma dan Fuego adalah `http.Handler`. Middleware apa pun dari ekosistem (otelhttp, rs/cors, gorilla/handlers) langsung bisa dipakai.
- nilo-go tidak mengimpor `net/http` di core-nya. Handler `net/http` bisa di-mount (`stdhttp.Mount(app, "/debug/pprof/", http.DefaultServeMux)`), tapi itu jalur lambat, dan middleware `net/http` tidak bisa membungkus route nilo-go.
- Sebagai gantinya nilo-go membawa sendiri: CORS, RequestID, AccessLog, Timeout, SecurityHeaders, CSRF, RateLimiter, Session, auth Bearer/Basic/APIKey, gzip, serta modul brotli, zstd, OTel dan JWT. Untuk kebutuhan di luar daftar itu, middleware harus ditulis sendiri.

## 17. Kematangan dan syarat

| | Syarat Go | Versi | Komunitas |
|---|---|---|---|
| Gin | lama | v1.12 | sangat besar, tapi rilis lambat dan satu orang yang me-merge (#3824) **[gh]** |
| Echo | lama | v5.4 | besar, v5 masih bergolak **[release-notes]** |
| chi | ≥ 4 versi terakhir | v5.5 | besar, stabil |
| Huma | 1.26 | v2.40 | sedang, didominasi satu maintainer **[gh]** |
| nilo-go | **1.27** (generic method) | **v0.1.0** | belum ada; API boleh berubah sebelum 1.0 |

---

## Harga DX nilo-go, dikumpulkan

1. **Go 1.27 wajib**, karena `app.Get` adalah generic method.
2. **Validasi format** (email, pattern, enum) belum berbentuk tipe; ditulis di `Validate()`.
3. **`Bounded[R]`** butuh satu tipe kosong per rentang panjang.
4. **Beberapa bentuk jawaban** berarti mengembalikan `any` dan mendaftarkan `Returns[...]`.
5. **Signature Provider** yang ditulis sendiri panjang dan sering perlu closure.
6. **SSE `Data`** masih string.
7. **Upload** lebih panjang daripada `c.FormFile` untuk kasus satu file kecil.
8. **Bukan `http.Handler`**: middleware dan alat test dari ekosistem tidak bisa dicolok langsung.
9. **Muda**: tidak ada contoh di luar repo, tidak ada jawaban StackOverflow, dan API bisa berubah.

## Yang tidak dimiliki framework lain

1. Input tanpa tag; asal nilai diambil dari nama Section.
2. Kesalahan tipe (pattern dan field, Provider, field pointer, dan seterusnya) ditolak sebelum request pertama, dengan file:line.
3. Satu status 400 problem+json dengan nama wire, dan nilai kiriman client tidak dipantulkan.
4. Handler tidak memegang writer, sehingga kesalahan "tulis dua kali", "header terlambat" dan "lupa return" tidak bisa terjadi.
5. Provider bertipe untuk user/auth yang langsung tercatat di OpenAPI.
6. WebSocket bawaan yang bertipe dan bisa ditest dengan jam palsu.
7. Tidak ada objek pool yang bocor ke goroutine.
