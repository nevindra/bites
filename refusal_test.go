package bites_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"os"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

// The refusal table of ADR 0007: one row per mistake, each asserting the whole message, route and
// file:line included. A row's registration and the call to here() sit on one line, so the line
// the message names is the line the row is on.

var hereLine, firstLine int

// refuseAll is a UseEach function that refuses every route, naming its operation.
func refuseAll(r bites.RouteInfo) (bites.Middleware, error) {
	return nil, fmt.Errorf("no permission for %s", r.OperationID)
}

func hereFirst() { _, _, firstLine, _ = runtime.Caller(1) }

// refusalBroker is a Broker that carries nothing, for the rows that are refused before it is used.
type refusalBroker struct{ reject string }

func (b refusalBroker) Accepts(string) error {
	if b.reject != "" {
		return errors.New(b.reject)
	}
	return nil
}
func (refusalBroker) Publish(context.Context, string, []byte) error { return nil }
func (refusalBroker) Subscribe(string, func([]byte), func()) func() { return func() {} }

func chanOut(context.Context, Empty) (chan int, error) { return nil, nil }

func streamOut(context.Context, Empty) (bites.Stream, error)     { return bites.Stream{}, nil }
func streamPtrOut(context.Context, Empty) (*bites.Stream, error) { return nil, nil }
func ssePtrOut(context.Context, Empty) (*bites.SSE, error)       { return nil, nil }
func okStreamOut(context.Context, Empty) (bites.OK[bites.SSE], error) {
	return bites.OK[bites.SSE]{}, nil
}

func eventsOut[T any](context.Context, Empty) (bites.Events[T], error) {
	return bites.Events[T]{}, nil
}
func eventsPtrOut(context.Context, Empty) (*bites.Events[User], error) { return nil, nil }
func okEventsOut(context.Context, Empty) (bites.OK[bites.Events[User]], error) {
	return bites.OK[bites.Events[User]]{}, nil
}

func fileOut(context.Context, Empty) (bites.File, error)     { return bites.File{}, nil }
func filePtrOut(context.Context, Empty) (*bites.File, error) { return nil, nil }
func okPageOut(context.Context, Empty) (bites.OK[bites.Page], error) {
	return bites.OK[bites.Page]{}, nil
}

func okFileOut(context.Context, Empty) (bites.OK[bites.File], error) {
	return bites.OK[bites.File]{}, nil
}

// The Inputs of the foreign tag rows (ADR 0021): tags another framework reads and bites does not.
type (
	tagValidate struct {
		Body struct {
			Name string `json:"name" validate:"required,min=3"`
		}
	}
	tagBindingDeep struct {
		Body struct {
			Lines []struct {
				Qty int `binding:"min=1"`
			}
		}
	}
	tagPath struct {
		Path struct {
			ID int64 `path:"id"`
		}
	}
	tagMinLength struct {
		Query struct {
			Q string `minLength:"3"`
		}
	}
	tagRequired struct {
		Body struct {
			Note bites.Opt[string] `required:"true"`
		}
	}
	tagJSONQuery struct {
		Query struct {
			PageSize int `json:"pageSize"`
		}
	}
)

// The Inputs of the Defaults rows (ADR 0041).
type (
	defTypoQuery struct{ Limit dfltPage }
	DefTypoIn    struct{ Query defTypoQuery }

	defOptQuery struct{ Limit bites.Opt[int] }
	DefOptIn    struct{ Query defOptQuery }

	defListQuery struct{ Tags []string }
	DefListIn    struct{ Query defListQuery }

	defCSVQuery struct{ Stages bites.CSV[int] }
	DefCSVIn    struct{ Query defCSVQuery }

	defPathParams struct{ ID int }
	DefPathIn     struct{ Path defPathParams }

	defRangeQuery struct{ Limit dfltPage }
	DefRangeIn    struct{ Query defRangeQuery }

	defEnumQuery struct{ Sort dfltOrder }
	DefEnumIn    struct{ Query defEnumQuery }

	defIntQuery struct{ N int }
	DefIntIn    struct{ Query defIntQuery }

	defBodyType struct{ Name string }
	DefBodyIn   struct{ Body defBodyType }

	defInnerBody struct{ Name string }
	defOuterBody struct{ Inner defInnerBody }
	DefNestedIn  struct{ Body defOuterBody }

	defEmbQuery struct{ dfltPaging }
	DefEmbIn    struct{ Query defEmbQuery }
)

func (defTypoQuery) Defaults() map[string]string  { return map[string]string{"Lmit": "5"} }
func (defOptQuery) Defaults() map[string]string   { return map[string]string{"Limit": "5"} }
func (defListQuery) Defaults() map[string]string  { return map[string]string{"Tags": "a"} }
func (defCSVQuery) Defaults() map[string]string   { return map[string]string{"Stages": "1,2"} }
func (defPathParams) Defaults() map[string]string { return map[string]string{"ID": "1"} }
func (defRangeQuery) Defaults() map[string]string { return map[string]string{"Limit": "500"} }
func (defEnumQuery) Defaults() map[string]string  { return map[string]string{"Sort": "up"} }
func (defIntQuery) Defaults() map[string]string   { return map[string]string{"N": "ten"} }
func (defBodyType) Defaults() map[string]string   { return map[string]string{"Name": "x"} }
func (defInnerBody) Defaults() map[string]string  { return map[string]string{"Name": "x"} }
func (defEmbQuery) Defaults() map[string]string   { return map[string]string{"Offst": "0"} }

// The Inputs of the Upload rows.
type (
	UploadIn     struct{ Body bites.Upload }
	UploadInJSON struct {
		Body struct {
			Name string
			File bites.Upload
		}
	}
	BodyReaderIn     struct{ Body bites.BodyReader }
	BodyReaderInJSON struct{ Body struct{ File bites.BodyReader } }
	UploadInList  struct{ Body []bites.Upload }
	UploadInQuery struct{ Query struct{ File bites.Upload } }
)

func customWho(context.Context, bites.Request, struct{}) (Principal, error) {
	return Principal{}, nil
}

// BothWho asks for the caller and for the caller as optional.
type BothWho struct {
	P Principal
	Q bites.Opt[Principal]
}

func basicNobody(context.Context, string, string) (Principal, error) {
	return Principal{}, bites.ErrInvalidCredentials
}

func passthru(ctx context.Context, _ bites.Request, next bites.Next) (bites.Response, error) {
	return next.Call(ctx), nil
}

func here() { _, _, hereLine, _ = runtime.Caller(1) }

// at is here() for a row gofmt keeps on several lines: it records the line it is called from,
// which is the line of the constructor call that takes it as an argument.
func at[T any](v T) T {
	_, _, hereLine, _ = runtime.Caller(1)
	return v
}

// atBoth is at for a row whose message names two places that are the same line.
func atBoth[T any](v T) T {
	_, _, hereLine, _ = runtime.Caller(1)
	firstLine = hereLine
	return v
}

// atFirst is at for the earlier of two registrations a row compares.
func atFirst[T any](v T) T {
	_, _, firstLine, _ = runtime.Caller(1)
	return v
}

// The types of the Provider rows: PA needs PB in needsB, and PB needs PA in needsA.
type (
	PA        struct{}
	PB        struct{}
	WithA     struct{ A PA }
	Pn[T any] struct{}
	Many      struct {
		F0  Pn[[0]int]
		F1  Pn[[1]int]
		F2  Pn[[2]int]
		F3  Pn[[3]int]
		F4  Pn[[4]int]
		F5  Pn[[5]int]
		F6  Pn[[6]int]
		F7  Pn[[7]int]
		F8  Pn[[8]int]
		F9  Pn[[9]int]
		F10 Pn[[10]int]
		F11 Pn[[11]int]
		F12 Pn[[12]int]
		F13 Pn[[13]int]
		F14 Pn[[14]int]
		F15 Pn[[15]int]
		F16 Pn[[16]int]
	}
)

func provA(context.Context, bites.Request, struct{}) (PA, error)        { return PA{}, nil }
func needsB(context.Context, bites.Request, struct{ B PB }) (PA, error) { return PA{}, nil }
func needsA(context.Context, bites.Request, struct{ A PA }) (PB, error) { return PB{}, nil }
func selfA(context.Context, bites.Request, struct{ A PA }) (PA, error)  { return PA{}, nil }
func intDeps(context.Context, bites.Request, int) (PA, error)           { return PA{}, nil }
func queryDep(context.Context, bites.Request, struct{ Query struct{ Tenant string } }) (PA, error) {
	return PA{}, nil
}

func pathDep(context.Context, bites.Request, struct{ Path struct{ ID int64 } }) (PA, error) {
	return PA{}, nil
}

func stringHeaderDep(context.Context, bites.Request, struct{ Header string }) (PA, error) {
	return PA{}, nil
}

// streamOK is a handler whose answer is a Stream, which the document describes as any type.
func streamOK(context.Context, struct{}) (bites.Stream, error) { return bites.Stream{}, nil }

// corsMW is a CORS middleware for the rows that give it where a preflight never reaches it.
var corsMW = bites.CORS(bites.CORSOptions{Origins: []string{"https://a.io"}})

func hiddenDep(context.Context, bites.Request, struct{ b PB }) (PA, error) {
	return PA{}, nil
}

func providePn[T any](a *bites.App) {
	a.Provide(func(context.Context, bites.Request, struct{}) (Pn[T], error) { return Pn[T]{}, nil })
}

func provideMany(a *bites.App) {
	providePn[[0]int](a)
	providePn[[1]int](a)
	providePn[[2]int](a)
	providePn[[3]int](a)
	providePn[[4]int](a)
	providePn[[5]int](a)
	providePn[[6]int](a)
	providePn[[7]int](a)
	providePn[[8]int](a)
	providePn[[9]int](a)
	providePn[[10]int](a)
	providePn[[11]int](a)
	providePn[[12]int](a)
	providePn[[13]int](a)
	providePn[[14]int](a)
	providePn[[15]int](a)
	providePn[[16]int](a)
}

func ok[In any](context.Context, In) (struct{}, error) { return struct{}{}, nil }

// okPage is ok for a route whose Input needs a Page answer.
func okPage[In any](context.Context, In) (bites.Page, error) { return bites.Page{}, nil }

// wsOK is the usual WebSocket handler of a row, short so gofmt keeps the row on one line.
var wsOK = wsHandler[Empty, wsOut, wsMsg]

// wsHandler is a WebSocket handler of the types a row names, for a refusal that never runs it.
func wsHandler[In, Out, Msg any](context.Context, In, *bites.Socket[Out]) (bites.Inbox[Msg], error) {
	return bites.Inbox[Msg]{}, nil
}

type (
	Empty       struct{}
	PathNoField struct{ Path struct{ Name string } }
	ExtraPath   struct {
		Path struct {
			ID  int64
			Org string
		}
	}
	BodyOnGet  struct{ Body struct{ Name string } }
	WithAuth   struct{ Auth string }
	BadSection struct{ Query string }
	PtrSection struct{ Query *struct{ Page int } }
	PtrQuery   struct{ Query struct{ Page *int } }
	NullQuery  struct {
		Query struct{ Page bites.Null[int] }
	}
	OptNullQuery struct {
		Query struct{ Page bites.Opt[bites.Null[int]] }
	}
	MapQuery struct {
		Query struct{ Filter map[string]string }
	}
	StructQuery struct {
		Query struct{ Range struct{ From, To int } }
	}
	OptPath struct {
		Path struct{ ID bites.Opt[int64] }
	}
	ListPath struct{ Path struct{ ID []int64 } }
	CSVPath   struct{ Path struct{ ID bites.CSV[int64] } }
	CSVCookie struct{ Cookie struct{ Ids bites.CSV[int64] } }
	CSVForm   struct{ Body bites.Form[struct{ Ids bites.CSV[int64] }] }
	CSVBody   struct{ Body struct{ Ids bites.CSV[int64] } }
	CSVOfOpt  struct{ Query struct{ Ids bites.CSV[bites.Opt[int64]] } }
	CSVOfCSV  struct{ Query struct{ Ids bites.CSV[bites.CSV[int64]] } }
	DupQuery struct {
		Query struct{ PageSize, Page_Size int }
	}
	HiddenQuery   struct{ Query struct{ page int } }
	MixedEmbedIn  struct {
		mixedSections
	}
	TwiceEmbedIn struct {
		idPathSection
		Path struct{ ID int }
	}
	HiddenBody    struct{ Body struct{ Name string; secret int } }
	HiddenNested  struct{ Body struct{ Lines []hiddenLine } }
	EmbedPtrQuery struct{ Query struct{ *Paging } }
	Paging        struct{ Page int }
	DupEmbed      struct {
		Query struct {
			Paging
			Page int
		}
	}
	ListOfOpt struct {
		Query struct{ Tags []bites.Opt[string] }
	}
	BodyPtr    struct{ Body struct{ Note *string } }
	BodyString struct{ Body string }
	BodyOpt    struct {
		Body bites.Opt[string]
	}
	BodyOptRaw struct {
		Body bites.Opt[bites.Raw[struct{ N int }]]
	}
	BodyAny    struct{ Body struct{ Extra any } }
	BodyStamp  struct{ Body struct{ At stamp } }
	BodyStamps struct{ Body struct{ Ats []stamp } }
	StampOut   struct{ At stamp }
	StampsOut  struct{ By map[string][]stamp }
	StaleNames struct{ Query struct{ Page int } }
	ManyLists  struct {
		Query struct{ A, B, C, D, E, F, G, H, I []string }
	}
	BadEnum struct{ Query struct{ Kind emptyKind } }

	FormOfInt  struct{ Body bites.Form[int] }
	SubmittedOfInt struct{ Body bites.Submitted[int] }
	SubmittedForm  struct{ Body bites.Submitted[struct{ N string }] }
	FormOfOpt  struct{ Body bites.Form[bites.Opt[int]] }
	FormNested struct {
		Body bites.Form[struct{ Inner struct{ A string } }]
	}
	FormNull struct {
		Body bites.Form[struct{ N bites.Null[string] }]
	}
	FormPtr struct {
		Body *bites.Form[struct{ N string }]
	}
)

type (
	WrongValidate struct{ Query struct{ Page int } }
	PtrValidate   struct{ Query struct{ Page int } }
	BodyValidate  struct{ Body bodyWithValidate }
	BadLengthBody struct {
		Body struct{ Name badLength }
	}
	CountLengthBody struct {
		Body struct{ Name lengthIsACount }
	}
	RangeOnStringQuery struct {
		Query struct{ Name rangeOnString }
	}
	LengthOnIntBody struct {
		Body struct{ N lengthOnInt }
	}
	CheckBoolBody struct {
		Body struct{ Mail checkReturnsBool }
	}
	KeyedBody struct {
		Body struct{ ByName map[bites.NonEmpty]int }
	}
	BadLengthQuery struct {
		Query struct{ Name badLength }
	}
	BadOwnRangeBody struct {
		Body struct{ N badOwnRange }
	}
	NarrowRangeBody struct {
		Body struct{ N narrowRange }
	}
	WholeRangeBody struct {
		Body struct{ N wholeRange }
	}
	WholeLengthBody struct {
		Body struct{ S wholeLength }
	}
	PtrCheckBody struct {
		Body struct{ Mail ptrCheck }
	}
	BadItemsBody struct {
		Body struct{ L badItems }
	}
	ZeroMaxItemsBody struct {
		Body struct{ L zeroMaxItems }
	}
	CountItemsBody struct {
		Body struct{ L itemsIsACount }
	}
	ItemsOnStringQuery struct {
		Query struct{ S itemsOnString }
	}
	ItemsOnBytesBody struct {
		Body struct{ B itemsOnBytes }
	}
	BadItemsQuery struct {
		Query struct{ L badItems }
	}
	RawJSONAnyBody struct {
		Body struct{ Style bites.RawJSON[any] }
	}
	RawJSONPtrBody struct {
		Body struct{ Style bites.RawJSON[*int] }
	}
	RawJSONFreeBody struct {
		Body struct{ Style bites.RawJSON[json.RawMessage] }
	}
	EmptyFormatBody struct {
		Body struct{ S emptyFormat }
	}
	FormatLayoutBody struct {
		Body struct{ S formatLayout }
	}
	FormatOnIntBody struct {
		Body struct{ N formatOnInt }
	}
	EmptyFormatOut   struct{ S emptyFormat }
	EmptyPatternBody struct {
		Body struct{ S emptyPattern }
	}
	BrokenPatternBody struct {
		Body struct{ S brokenPattern }
	}
	DotPatternBody struct {
		Body struct{ S dotPattern }
	}
	SpacePatternBody struct {
		Body struct{ S spacePattern }
	}
	NamedPatternBody struct {
		Body struct{ S namedPattern }
	}
	FlagPatternBody struct {
		Body struct{ S flagPattern }
	}
	TextAnchorPatternBody struct {
		Body struct{ S textAnchorPattern }
	}
	PosixPatternBody struct {
		Body struct{ S posixPattern }
	}
	ScriptPatternBody struct {
		Body struct{ S scriptPattern }
	}
	BracePatternBody struct {
		Body struct{ S bracePattern }
	}
	ClassEscapeRangePatternBody struct {
		Body struct{ S classEscapeRangePattern }
	}
	AnchorRepeatPatternBody struct {
		Body struct{ S anchorRepeatPattern }
	}
	IntPatternBody struct {
		Body struct{ S intPattern }
	}
	PatternIsALayoutBody struct {
		Body struct{ S patternIsALayout }
	}
	EnumPatternBody struct {
		Body struct{ S enumPattern }
	}
	TextPatternBody struct {
		Body struct{ S textPattern }
	}
	NaNRangeBody struct {
		Body struct{ F nanRange }
	}
	InfRangeBody struct {
		Body struct{ F infRange }
	}
	BackwardsFloatRangeBody struct {
		Body struct{ F backwardsFloatRange }
	}
	WholeFloatRangeBody struct {
		Body struct{ F wholeFloatRange }
	}
	WholeFloat32RangeBody struct {
		Body struct{ F wholeFloat32Range }
	}
	WideFloat32RangeBody struct {
		Body struct{ F wideFloat32Range }
	}
	IntRangeOnFloatBody struct {
		Body struct{ F intRangeOnFloat }
	}
	FloatRangeOnIntBody struct {
		Body struct{ N floatRangeOnInt }
	}
	FloatRangeIsACountBody struct {
		Body struct{ F floatRangeIsACount }
	}
	CheckedFormatOnListBody struct {
		Body struct{ L checkedFormatOnList }
	}
	CheckedFormatOnEnumBody struct {
		Body struct{ S checkedFormatOnEnum }
	}
	CheckedFormatOnTextBody struct {
		Body struct{ S checkedFormatOnText }
	}
	CheckedFormatOnListOut struct{ L checkedFormatOnList }
)

type (
	badItems                []string
	zeroMaxItems            []string
	itemsIsACount           []string
	itemsOnString           string
	itemsOnBytes            []byte
	emptyFormat             string
	formatLayout            string
	formatOnInt             int32
	emptyPattern            string
	brokenPattern           string
	dotPattern              string
	spacePattern            string
	namedPattern            string
	flagPattern             string
	textAnchorPattern       string
	posixPattern            string
	scriptPattern           string
	bracePattern            string
	classEscapeRangePattern string
	anchorRepeatPattern     string
	intPattern              int32
	patternIsALayout        string
	enumPattern             string
	textPattern             string
)

func (badItems) Items() (int, int)        { return 4, 2 }
func (zeroMaxItems) Items() (int, int)    { return 0, 0 }
func (itemsIsACount) Items() int          { return 0 }
func (itemsOnString) Items() (int, int)   { return 1, 2 }
func (itemsOnBytes) Items() (int, int)    { return 1, 2 }
func (emptyFormat) Format() string        { return "" }
func (formatLayout) Format(string) string { return "x" }
func (formatOnInt) Format() string        { return "port" }

func (emptyPattern) Pattern() string            { return "" }
func (brokenPattern) Pattern() string           { return `^[a-$` }
func (dotPattern) Pattern() string              { return `^.+$` }
func (spacePattern) Pattern() string            { return `^\s*$` }
func (namedPattern) Pattern() string            { return `(?P<year>\d{4})` }
func (flagPattern) Pattern() string             { return `(?i)abc` }
func (textAnchorPattern) Pattern() string       { return `\Aabc\z` }
func (posixPattern) Pattern() string            { return `^[[:alpha:]]+$` }
func (scriptPattern) Pattern() string           { return `^\pL+$` }
func (bracePattern) Pattern() string            { return `a{` }
func (classEscapeRangePattern) Pattern() string { return `[\d-x]` }
func (anchorRepeatPattern) Pattern() string     { return `^*a` }
func (intPattern) Pattern() string              { return `^a$` }
func (patternIsALayout) Pattern(string) string  { return `^a$` }
func (enumPattern) Pattern() string             { return `^a$` }
func (enumPattern) Values() []enumPattern       { return []enumPattern{"a"} }
func (*textPattern) Pattern() string            { return `^a$` }
func (*textPattern) UnmarshalText([]byte) error { return nil }

type (
	nanRange            float64
	infRange            float64
	backwardsFloatRange float64
	wholeFloatRange     float64
	wholeFloat32Range   float32
	wideFloat32Range    float32
	intRangeOnFloat     float64
	floatRangeOnInt     int64
	floatRangeIsACount  float64
	checkedFormatOnList []string
	checkedFormatOnEnum string
	checkedFormatOnText string
)

func (nanRange) Range() (float64, float64)                { return math.NaN(), 1 }
func (infRange) Range() (float64, float64)                { return 0, math.Inf(1) }
func (backwardsFloatRange) Range() (float64, float64)     { return 5, 1.5 }
func (wholeFloatRange) Range() (float64, float64)         { return -math.MaxFloat64, math.MaxFloat64 }
func (wholeFloat32Range) Range() (float64, float64)       { return -math.MaxFloat32, math.MaxFloat32 }
func (wideFloat32Range) Range() (float64, float64)        { return 0, 1e39 }
func (intRangeOnFloat) Range() (int64, int64)             { return 1, 9 }
func (floatRangeOnInt) Range() (float64, float64)         { return 0.5, 9 }
func (floatRangeIsACount) Range() float64                 { return 1 }
func (checkedFormatOnList) Format() string                { return "email" }
func (checkedFormatOnEnum) Format() string                { return "uuid" }
func (checkedFormatOnEnum) Values() []checkedFormatOnEnum { return []checkedFormatOnEnum{"a"} }
func (*checkedFormatOnText) Format() string               { return "hostname" }
func (*checkedFormatOnText) UnmarshalText([]byte) error   { return nil }

type ptrCheck string

func (*ptrCheck) Check() error { return nil }

type badLength string

func (badLength) Length() (int, int) { return 5, 2 }

type badOwnRange int

func (badOwnRange) Range() (int64, int64) { return 9, 1 }

type wholeRange int64

func (wholeRange) Range() (int64, int64) { return math.MinInt64, math.MaxInt64 }

type wholeLength string

func (wholeLength) Length() (int, int) { return 0, math.MaxInt }

type narrowRange int32

func (narrowRange) Range() (int64, int64) { return 1, 9 }

func (WrongValidate) Validate() string { return "" }
func (*PtrValidate) Validate() error   { return nil }

type bodyWithValidate struct{ Name string }

func (bodyWithValidate) Validate() error { return nil }

type (
	lengthIsACount   string
	rangeOnString    string
	lengthOnInt      int
	checkReturnsBool string
)

func (lengthIsACount) Length() int      { return 0 }
func (rangeOnString) Range() (int, int) { return 1, 64 }
func (lengthOnInt) Length() (int, int)  { return 1, 9 }
func (checkReturnsBool) Check() bool    { return true }

type emptyKind string

func (emptyKind) Values() []emptyKind { return nil }

type withNames struct{ Page int }

func (withNames) WireNames() map[string]string { return map[string]string{"Page": "p", "Gone": "x"} }

type StaleNamesIn struct{ Query withNames }

type staleEmbeddedNames struct{ Page int }

func (staleEmbeddedNames) WireNames() map[string]string { return map[string]string{"Gone": "x"} }

type StaleEmbeddedNamesIn struct {
	Query struct {
		staleEmbeddedNames
		withoutNames
	}
}

type withoutNames struct{ Q string }

// The types of the API document and Returns rows (ADR 0013, ADR 0020).
type namer interface{ Name() string }

func anyOut(context.Context, Empty) (any, error)     { return nil, nil }
func namerOut(context.Context, Empty) (namer, error) { return nil, nil }

// The OneOf rows (ADR 0013): outOf is a handler that returns T.
func outOf[T any](context.Context, Empty) (T, error) {
	var v T
	return v, nil
}

type (
	oneOfHolder struct{ Answer bites.OneOf2[User, Empty] }
	OneOfBodyIn struct{ Body []bites.OneOf2[User, Empty] }
	nestedOneOf = bites.OneOf2[User, bites.OneOf2[Empty, bites.NoContent]]
)

type (
	typoBody    struct{ Name string }
	typoOut     struct{ Total int }
	typoPath    struct{ ID int64 }
	TypoBodyIn  struct{ Body typoBody }
	TypoOutIn   struct{}
	TypoQueryIn struct{ Query typoQuery }
	typoQuery   struct{ Page int }
)

func (typoBody) Descriptions() map[string]string  { return map[string]string{"Nmae": "x", "Name": "ok"} }
func (typoOut) Descriptions() map[string]string   { return map[string]string{"Totl": "x"} }
func (typoQuery) Descriptions() map[string]string { return map[string]string{"Pag": "x"} }

type EmbeddedTypoIn struct{ Query struct{ typoQuery } }

func typoOutOut(context.Context, Empty) (typoOut, error) { return typoOut{}, nil }

// registerTwins registers two routes whose outputs are different types with one name and one
// package path, which no stable component name can tell apart.
func registerTwins(a *bites.App) {
	{
		type Twin struct{ A int }
		a.Get("/a", func(context.Context, Empty) (Twin, error) { return Twin{}, nil })
	}
	{
		type Twin struct{ B int }
		a.Get("/b", func(context.Context, Empty) (Twin, error) { return Twin{}, nil })
	}
}

func TestEveryMistakeIsRefusedAtRegistrationWithTheMessageWrittenForIt(t *testing.T) {
	rows := []struct {
		name string
		reg  func(a *bites.App)
		want string // without the "\n\tregistered at file:line" tail, or the whole message when full
		full bool
	}{
		{"a wildcard with no Path section", func(a *bites.App) { here(); a.Get("/users/{id}", ok[Empty]) },
			"GET /users/{id}: the pattern has {id}, but Empty has no Path section", false},
		{"a wildcard with no field for it", func(a *bites.App) { here(); a.Get("/users/{id}", ok[PathNoField]) },
			"GET /users/{id}: the pattern has {id}, but PathNoField.Path has no field for it", false},
		{"a Path field with no wildcard", func(a *bites.App) { here(); a.Get("/users/{id}", ok[ExtraPath]) },
			"GET /users/{id}: ExtraPath.Path.Org has no {org} in the pattern", false},
		{"a wildcard of the group's prefix with no field", func(a *bites.App) { here(); a.Group("/orgs").Get("/{id}/{org}", ok[GetUser]) },
			"GET /orgs/{id}/{org}: the pattern has {org}, but GetUser.Path has no field for it", false},
		{"a Body on GET", func(a *bites.App) { here(); a.Get("/x", ok[BodyOnGet]) },
			"GET /x: BodyOnGet.Body is a Body section, but a GET route has no request body", false},
		{"a Body on DELETE", func(a *bites.App) { here(); a.Delete("/x", ok[BodyOnGet]) },
			"DELETE /x: BodyOnGet.Body is a Body section, but a DELETE route has no request body", false},
		{"a Body on HEAD", func(a *bites.App) { here(); a.Head("/x", ok[BodyOnGet]) },
			"HEAD /x: BodyOnGet.Body is a Body section, but a HEAD route has no request body", false},
		{"a field that is not a section", func(a *bites.App) { here(); a.Get("/x", ok[WithAuth]) },
			"GET /x: WithAuth.Auth is not a section (Path, Query, Header, Cookie or Body), and no Provider of string covers this route", false},
		{"an Input that is not a struct", func(a *bites.App) { here(); a.Get("/x", ok[int]) },
			"GET /x: the handler's input must be a struct whose fields are the sections Path, Query, Header, Cookie and Body, not int", false},
		{"an Input that is a pointer", func(a *bites.App) { here(); a.Get("/x", ok[*GetUser]) },
			"GET /x: the handler takes *bites_test.GetUser, a pointer; an Input is taken by value, because a pointer cannot say whether nil meant absent or null (use Opt[T] or Null[T] for a field)", false},
		{"a validate tag on a Body field, with no Validate to read it", func(a *bites.App) { here(); a.Post("/x", ok[tagValidate]) },
			"POST /x: tagValidate.Body.Name has a validate tag, which bites does not read, so its rules would never run: give the field a type of your own with a rule method (Length, Range, Items, Pattern, Format, Check), or run your validator in tagValidate.Validate", false},
		{"a binding tag deep in a Body list", func(a *bites.App) { here(); a.Post("/x", ok[tagBindingDeep]) },
			"POST /x: tagBindingDeep.Body.Lines.Qty has a binding tag, which bites does not read, so its rules would never run: give the field a type of your own with a rule method (Length, Range, Items, Pattern, Format, Check), or run your validator in tagBindingDeep.Validate", false},
		{"a path tag on a Path field", func(a *bites.App) { here(); a.Get("/x/{id}", ok[tagPath]) },
			"GET /x/{id}: tagPath.Path.ID has a path tag, which bites does not read, so it would not say where the value comes from: the section the field sits in says that, and its wire name comes from the naming policy or a WireNames method", false},
		{"a minLength tag on a Query field", func(a *bites.App) { here(); a.Get("/x", ok[tagMinLength]) },
			"GET /x: tagMinLength.Query.Q has a minLength tag, which bites does not read, so the bound would never be checked: a length is a Length method on the field's type", false},
		{"a required tag on a Body field", func(a *bites.App) { here(); a.Post("/x", ok[tagRequired]) },
			"POST /x: tagRequired.Body.Note has a required tag, which bites does not read, so it would not change whether the field may be missing: a plain field is required and an Opt[T] may be absent", false},
		{"a json tag that renames a Query field", func(a *bites.App) { here(); a.Get("/x", ok[tagJSONQuery]) },
			`GET /x: tagJSONQuery.Query.PageSize has a json tag naming it "pageSize", but its wire name is "page_size": a json tag is read in a JSON Body only, so name it with a WireNames method`, false},
		{"a section that is not a struct", func(a *bites.App) { here(); a.Get("/x", ok[BadSection]) },
			"GET /x: BadSection.Query must be a struct, not string", false},
		{"a section that is a pointer", func(a *bites.App) { here(); a.Get("/x", ok[PtrSection]) },
			"GET /x: PtrSection.Query is a pointer; a section is a struct taken by value, and a pointer cannot say whether nil meant absent or null", false},
		{"a pointer in Query", func(a *bites.App) { here(); a.Get("/x", ok[PtrQuery]) },
			"GET /x: PtrQuery.Query.Page: *int is a pointer; a pointer cannot say whether nil meant absent or null, use Opt[T] or Null[T]", false},
		{"a Null in Query", func(a *bites.App) { here(); a.Get("/x", ok[NullQuery]) },
			"GET /x: NullQuery.Query.Page is a Null; text has no null, so the type would promise a state no request can produce: use Opt[T] for a value that may be missing", false},
		{"an Opt of Null in Query", func(a *bites.App) { here(); a.Get("/x", ok[OptNullQuery]) },
			"GET /x: OptNullQuery.Query.Page: an Opt of Null[int] is not allowed in a text section; text has no null", false},
		{"a map in Query", func(a *bites.App) { here(); a.Get("/x", ok[MapQuery]) },
			"GET /x: MapQuery.Query.Filter: map[string]string is a map; text holds one scalar, give it an UnmarshalText method or use a type of its own", false},
		{"a struct in Query", func(a *bites.App) { here(); a.Get("/x", ok[StructQuery]) },
			"GET /x: StructQuery.Query.Range: struct { From int; To int } is a struct; text holds one scalar, give it an UnmarshalText method or use a type of its own", false},
		{"an Opt in Path", func(a *bites.App) { here(); a.Get("/x/{id}", ok[OptPath]) },
			"GET /x/{id}: OptPath.Path.ID is an Opt, but a path parameter is always present", false},
		{"a list in Path", func(a *bites.App) { here(); a.Get("/x/{id}", ok[ListPath]) },
			"GET /x/{id}: ListPath.Path.ID is a list, but a path parameter holds one value", false},
		{"a CSV in Path", func(a *bites.App) { here(); a.Get("/x/{id}", ok[CSVPath]) },
			"GET /x/{id}: CSVPath.Path.ID is a comma-separated list, which only a Query or a Header section reads; a Path holds one value, and a repeated field is a plain []int64", false},
		{"a CSV in Cookie", func(a *bites.App) { here(); a.Get("/x", ok[CSVCookie]) },
			"GET /x: CSVCookie.Cookie.Ids is a comma-separated list, which only a Query or a Header section reads; a Cookie holds one value, and a repeated field is a plain []int64", false},
		{"a CSV in a Form", func(a *bites.App) { here(); a.Post("/x", ok[CSVForm]) },
			"POST /x: CSVForm.Body.Ids is a comma-separated list, which only a Query or a Header section reads; a Form holds one value, and a repeated field is a plain []int64", false},
		{"a CSV in a JSON Body", func(a *bites.App) { here(); a.Post("/x", ok[CSVBody]) },
			"POST /x: CSVBody.Body.Ids: bites.CSV[int64] is a comma-separated list, which only a Query or a Header section reads; in JSON a list is an array, so use []int64", false},
		{"a CSV of Opt", func(a *bites.App) { here(); a.Get("/x", ok[CSVOfOpt]) },
			"GET /x: CSVOfOpt.Query.Ids: a list element cannot be Opt[int64], absence means nothing there", false},
		{"a CSV of CSV", func(a *bites.App) { here(); a.Get("/x", ok[CSVOfCSV]) },
			"GET /x: CSVOfCSV.Query.Ids: bites.CSV[int64] is a slice; a slice is filled from repeated keys by the section, so its element type is what decodes from text", false},
		{"two fields with one wire name", func(a *bites.App) { here(); a.Get("/x", ok[DupQuery]) },
			`GET /x: DupQuery.Query.PageSize and DupQuery.Query.Page_Size both have the wire name "page_size"`, false},
		{"an unexported field in Query", func(a *bites.App) { here(); a.Get("/x", ok[HiddenQuery]) },
			"GET /x: HiddenQuery.Query.page is unexported, so nothing can fill it", false},
		{"an unexported field in a JSON Body", func(a *bites.App) { here(); a.Post("/x", ok[HiddenBody]) },
			"POST /x: HiddenBody.Body.secret: it is unexported, so nothing can fill it: give it an exported name, or tag it json:\"-\" to leave it out on purpose", false},
		{"an unexported field in a struct nested in a JSON Body", func(a *bites.App) { here(); a.Post("/x", ok[HiddenNested]) },
			"POST /x: hiddenLine.note: it is unexported, so nothing can fill it: give it an exported name, or tag it json:\"-\" to leave it out on purpose", false},
		{"an embedded struct of sections that also holds another field", func(a *bites.App) { here(); a.Get("/x/{id}", ok[MixedEmbedIn]) },
			"GET /x/{id}: MixedEmbedIn embeds mixedSections, which holds sections and also Me, which is not one: an embedded struct of an Input holds only sections (Path, Query, Header, Cookie, Body); give Me to MixedEmbedIn itself", false},
		{"a section an embedded struct and the Input both give", func(a *bites.App) { here(); a.Get("/x/{id}", ok[TwiceEmbedIn]) },
			"GET /x/{id}: TwiceEmbedIn gets a Path section from both idPathSection and TwiceEmbedIn, and Go would read one and hide the other: keep one", false},
		{"an embedded pointer in Query", func(a *bites.App) { here(); a.Get("/x", ok[EmbedPtrQuery]) },
			"GET /x: EmbedPtrQuery.Query.Paging is an embedded pointer, which cannot be inlined; embed the struct by value", false},
		{"an embedded struct that repeats a wire name", func(a *bites.App) { here(); a.Get("/x", ok[DupEmbed]) },
			`GET /x: DupEmbed.Query.Paging.Page and DupEmbed.Query.Page both have the wire name "page"`, false},
		{"a WireNames entry that names no field", func(a *bites.App) { here(); a.Get("/x", ok[StaleNamesIn]) },
			"GET /x: StaleNamesIn.Query.WireNames names Gone, which is not a field", false},
		{"an embedded struct's WireNames entry that names no field of its own", func(a *bites.App) { here(); a.Get("/x", ok[StaleEmbeddedNamesIn]) },
			"GET /x: StaleEmbeddedNamesIn.Query.staleEmbeddedNames.WireNames names Gone, which is not a field", false},
		{"a list of Opt", func(a *bites.App) { here(); a.Get("/x", ok[ListOfOpt]) },
			"GET /x: ListOfOpt.Query.Tags: a list element cannot be Opt[string], absence means nothing there", false},
		{"more lists than a section holds", func(a *bites.App) { here(); a.Get("/x", ok[ManyLists]) },
			"GET /x: ManyLists.Query: a section can hold at most 8 lists", false},
		{"an enum that lists nothing", func(a *bites.App) { here(); a.Get("/x", ok[BadEnum]) },
			"GET /x: BadEnum.Query.Kind: bites_test.emptyKind: Values returned nothing, so no request could ever be valid", false},
		{"a Validate that does not return an error", func(a *bites.App) { here(); a.Get("/x", ok[WrongValidate]) },
			"GET /x: WrongValidate has a Validate method of type func() string; it must be func() error, so that it runs after decoding and reports its errors with Invalid", false},
		{"a Validate with a pointer receiver", func(a *bites.App) { here(); a.Get("/x", ok[PtrValidate]) },
			"GET /x: PtrValidate.Validate has a pointer receiver, but an Input is taken by value, so the method would never be found: declare it on PtrValidate", false},
		{"a Validate on a section instead of the Input", func(a *bites.App) { here(); a.Post("/x", ok[BodyValidate]) },
			"POST /x: BodyValidate.Body has a Validate method, but only the Input's Validate runs: move the checks to BodyValidate.Validate", false},
		{"a named string type whose Length is not a length, in Body", func(a *bites.App) { here(); a.Post("/x", ok[BadLengthBody]) },
			"POST /x: BadLengthBody.Body.Name: bites_test.badLength.Length returns (5, 2), which is not a length: it must be 0 <= min <= max with max at least 1", false},
		{"a Length method that means something else", func(a *bites.App) { here(); a.Post("/x", ok[CountLengthBody]) },
			"POST /x: CountLengthBody.Body.Name: bites_test.lengthIsACount has a Length method, so it carries a bound, but Length must be func() (min, max int): rename the method if it means something else", false},
		{"a Range on a string type", func(a *bites.App) { here(); a.Get("/x", ok[RangeOnStringQuery]) },
			"GET /x: RangeOnStringQuery.Query.Name: bites_test.rangeOnString has a Range method, but a string type's bound is its length: name it Length, func() (min, max int), in characters", false},
		{"a Length on an integer type", func(a *bites.App) { here(); a.Post("/x", ok[LengthOnIntBody]) },
			"POST /x: LengthOnIntBody.Body.N: bites_test.lengthOnInt has a Length method, but an integer type's bound is its value: name it Range, func() (min, max int64)", false},
		{"a Check method that is not func() error", func(a *bites.App) { here(); a.Post("/x", ok[CheckBoolBody]) },
			"POST /x: CheckBoolBody.Body.Mail: bites_test.checkReturnsBool has a Check method, so it checks its value, but Check must be func() error: rename the method if it means something else", false},
		{"a named string type whose Length is not a length", func(a *bites.App) { here(); a.Get("/x", ok[BadLengthQuery]) },
			"GET /x: BadLengthQuery.Query.Name: bites_test.badLength.Length returns (5, 2), which is not a length: it must be 0 <= min <= max with max at least 1", false},
		{"a named integer type whose Range has max below min", func(a *bites.App) { here(); a.Post("/x", ok[BadOwnRangeBody]) },
			"POST /x: BadOwnRangeBody.Body.N: bites_test.badOwnRange.Range returns (9, 1), and max is below min, so no value could be valid", false},
		{"a Range on an integer type other than int and int64", func(a *bites.App) { here(); a.Post("/x", ok[NarrowRangeBody]) },
			"POST /x: NarrowRangeBody.Body.N: bites_test.narrowRange has a Range, which is read on an int or int64 type, and bites_test.narrowRange is a int32", false},
		{"a Check with a pointer receiver", func(a *bites.App) { here(); a.Post("/x", ok[PtrCheckBody]) },
			"POST /x: PtrCheckBody.Body.Mail: bites_test.ptrCheck.Check has a pointer receiver, but a value is checked as it was decoded and must not be kept: declare it on ptrCheck", false},
		{"an Items whose max is below its min", func(a *bites.App) { here(); a.Post("/x", ok[BadItemsBody]) },
			"POST /x: BadItemsBody.Body.L: bites_test.badItems.Items returns (4, 2), which is not a count of items: it must be 0 <= min <= max with max at least 1", false},
		{"an Items whose max is below one", func(a *bites.App) { here(); a.Post("/x", ok[ZeroMaxItemsBody]) },
			"POST /x: ZeroMaxItemsBody.Body.L: bites_test.zeroMaxItems.Items returns (0, 0), which is not a count of items: it must be 0 <= min <= max with max at least 1", false},
		{"an Items method that means something else", func(a *bites.App) { here(); a.Post("/x", ok[CountItemsBody]) },
			"POST /x: CountItemsBody.Body.L: bites_test.itemsIsACount has a Items method, so it carries a bound, but Items must be func() (min, max int): rename the method if it means something else", false},
		{"an Items on a string type", func(a *bites.App) { here(); a.Get("/x", ok[ItemsOnStringQuery]) },
			"GET /x: ItemsOnStringQuery.Query.S: bites_test.itemsOnString has an Items method, but Items bounds a list: it is read on a named slice type, and bites_test.itemsOnString is a string", false},
		{"an Items on a byte slice type", func(a *bites.App) { here(); a.Post("/x", ok[ItemsOnBytesBody]) },
			"POST /x: ItemsOnBytesBody.Body.B: bites_test.itemsOnBytes has an Items method, but it decodes itself, so its items are not counted", false},
		{"a RawJSON of an interface", func(a *bites.App) { here(); a.Post("/x", ok[RawJSONAnyBody]) },
			"POST /x: RawJSONAnyBody.Body.Style: bites.RawJSON[interface {}] names no shape: an interface has none, so use json.RawMessage for any JSON", false},
		{"a RawJSON of a pointer", func(a *bites.App) { here(); a.Post("/x", ok[RawJSONPtrBody]) },
			"POST /x: RawJSONPtrBody.Body.Style: bites.RawJSON[*int]: null is already accepted, so name the type itself, RawJSON[int]", false},
		{"a RawJSON of free-form JSON", func(a *bites.App) { here(); a.Post("/x", ok[RawJSONFreeBody]) },
			"POST /x: RawJSONFreeBody.Body.Style: bites.RawJSON[encoding/json/jsontext.Value]: T is itself free-form JSON; use json.RawMessage, or name the shape the value has", false},
		{"a list type whose Items is not a count, in Query", func(a *bites.App) { here(); a.Get("/x", ok[BadItemsQuery]) },
			"GET /x: BadItemsQuery.Query.L: bites_test.badItems.Items returns (4, 2), which is not a count of items: it must be 0 <= min <= max with max at least 1", false},
		{"a Format that returns an empty string", func(a *bites.App) { here(); a.Post("/x", ok[EmptyFormatBody]) },
			"POST /x: EmptyFormatBody.Body.S: bites_test.emptyFormat.Format returns an empty string, which names no format: remove the method or name one", false},
		{"a Format that returns an empty string, in a response", func(a *bites.App) { here(); a.Get("/x", outOf[EmptyFormatOut]) },
			"GET /x: the output type cannot be written as JSON: EmptyFormatOut.S: bites_test.emptyFormat.Format returns an empty string, which names no format: remove the method or name one", false},
		{"a Format method that means something else", func(a *bites.App) { here(); a.Post("/x", ok[FormatLayoutBody]) },
			"POST /x: FormatLayoutBody.Body.S: bites_test.formatLayout has a Format method, so it names its format in the API document, but Format must be func() string: rename the method if it means something else", false},
		{"a Format on an integer type, which has a format of its own", func(a *bites.App) { here(); a.Post("/x", ok[FormatOnIntBody]) },
			"POST /x: FormatOnIntBody.Body.N: bites_test.formatOnInt has a Format method, but Format is read on a string type: the API document already gives a int32 its own format", false},
		{"an ExclusiveMin on an integer type", func(a *bites.App) { here(); a.Post("/x", ok[ExMinOnIntBody]) },
			"POST /x: ExMinOnIntBody.Body.N: bites_test.exMinOnInt has an ExclusiveMin method, but an integer's bound is stated once, as Range, func() (min, max int64), both included: for a value above n write Range(n+1, ...) and for a value below n write Range(..., n-1)", false},
		{"an ExclusiveMin beside a Range that bounds the minimum", func(a *bites.App) { here(); a.Post("/x", ok[ExBothMinBody]) },
			"POST /x: ExBothMinBody.Body.N: bites_test.exBothMin has ExclusiveMin and a Range that already bounds the minimum (1): a side is stated once, so leave Range's min at -math.MaxFloat64 and keep the exclusive bound, or drop ExclusiveMin", false},
		{"an ExclusiveMax beside a Range that bounds the maximum", func(a *bites.App) { here(); a.Post("/x", ok[ExBothMaxBody]) },
			"POST /x: ExBothMaxBody.Body.N: bites_test.exBothMax has ExclusiveMax and a Range that already bounds the maximum (10): a side is stated once, so leave Range's max at math.MaxFloat64 and keep the exclusive bound, or drop ExclusiveMax", false},
		{"exclusive bounds that leave no value", func(a *bites.App) { here(); a.Post("/x", ok[ExEmptyBody]) },
			"POST /x: ExEmptyBody.Body.N: bites_test.exEmpty states a minimum of 5 and a maximum of 5, and an exclusive bound leaves no value between them", false},
		{"an ExclusiveMin with another signature", func(a *bites.App) { here(); a.Post("/x", ok[ExWrongSigBody]) },
			"POST /x: ExWrongSigBody.Body.N: bites_test.exclWrongSig has a ExclusiveMin method, so it carries a rule, but ExclusiveMin must be func() float64: rename the method if it means something else", false},
		{"an ExclusiveMax on a string type", func(a *bites.App) { here(); a.Post("/x", ok[ExOnStringBody]) },
			"POST /x: ExOnStringBody.Body.N: bites_test.exOnString has a ExclusiveMax method, but ExclusiveMax is read on a number type and bites_test.exOnString is a string: a string's bound is its length, a Length method, func() (min, max int), in characters", false},
		{"an ExclusiveMin at the limit of its type", func(a *bites.App) { here(); a.Post("/x", ok[ExAtLimitBody]) },
			"POST /x: ExAtLimitBody.Body.N: bites_test.exAtLimit.ExclusiveMin returns -1.7976931348623157e+308, which is the limit of float64 and not a bound: drop the method, or give a number inside the type's range", false},
		{"an ExclusiveMax that is NaN", func(a *bites.App) { here(); a.Post("/x", ok[ExNaNBody]) },
			"POST /x: ExNaNBody.Body.N: bites_test.exNaN.ExclusiveMax returns NaN, and a bound must be a finite number", false},
		{"a MultipleOf of zero on an integer type", func(a *bites.App) { here(); a.Post("/x", ok[MulZeroIntBody]) },
			"POST /x: MulZeroIntBody.Body.N: bites_test.mulZeroInt.MultipleOf returns 0, and a step must be at least 1", false},
		{"a MultipleOf of one on an integer type", func(a *bites.App) { here(); a.Post("/x", ok[MulOneIntBody]) },
			"POST /x: MulOneIntBody.Body.N: bites_test.mulOneInt.MultipleOf returns 1, and every integer is a multiple of 1, so it bounds nothing: drop the method", false},
		{"a MultipleOf below zero on a float type", func(a *bites.App) { here(); a.Post("/x", ok[MulNegFloatBody]) },
			"POST /x: MulNegFloatBody.Body.N: bites_test.mulNegFloat.MultipleOf returns -0.5, and a step must be greater than 0", false},
		{"a MultipleOf on a string type", func(a *bites.App) { here(); a.Post("/x", ok[MulOnStringBody]) },
			"POST /x: MulOnStringBody.Body.N: bites_test.mulOnString has a MultipleOf method, but MultipleOf is read on a number type and bites_test.mulOnString is a string: a string's bound is its length, a Length method, func() (min, max int), in characters", false},
		{"a MultipleOf on an integer type other than int and int64", func(a *bites.App) { here(); a.Post("/x", ok[MulOnInt32Body]) },
			"POST /x: MulOnInt32Body.Body.N: bites_test.mulOnInt32 has a MultipleOf, which is read on an int or int64 type, and bites_test.mulOnInt32 is a int32", false},
		{"a MultipleOf on a bool type", func(a *bites.App) { here(); a.Post("/x", ok[MulOnBoolBody]) },
			"POST /x: MulOnBoolBody.Body.N: bites_test.mulOnBool has a MultipleOf method, but MultipleOf is read on an int, int64, float32 or float64 type, and bites_test.mulOnBool is a bool", false},
		{"an integer MultipleOf with a Range that holds no multiple", func(a *bites.App) { here(); a.Post("/x", ok[MulNoneInRangeBody]) },
			"POST /x: MulNoneInRangeBody.Body.N: bites_test.mulNoneInRange.Range returns (6, 9) and MultipleOf returns 5, and no integer between the two is a multiple of 5, so no value could be valid", false},
		{"a MultipleOf with another signature", func(a *bites.App) { here(); a.Post("/x", ok[MulWrongSigBody]) },
			"POST /x: MulWrongSigBody.Body.N: bites_test.mulWrongSig has a MultipleOf method, so it carries a rule, but MultipleOf must be func() int64: rename the method if it means something else", false},
		{"a float MultipleOf with a range that holds no multiple", func(a *bites.App) { here(); a.Post("/x", ok[MulFloatNoneInBody]) },
			"POST /x: MulFloatNoneInBody.Body.N: bites_test.mulFloatNoneIn states a minimum of 0.011, a maximum of 0.019 and MultipleOf 0.01, and no number between the two is a multiple of it, so no value could be valid", false},
		{"a MultipleOf on a slice type", func(a *bites.App) { here(); a.Post("/x", ok[MulOnSliceBody]) },
			"POST /x: MulOnSliceBody.Body.N: bites_test.mulOnSlice has a MultipleOf method, but MultipleOf is read on an int, int64, float32 or float64 type, and bites_test.mulOnSlice is a slice", false},
		{"a Unique that returns false", func(a *bites.App) { here(); a.Post("/x", ok[UniqueFalseBody]) },
			"POST /x: UniqueFalseBody.Body.L: bites_test.uniqueFalse.Unique returns false, which states nothing: drop the method, since a list may repeat an item unless Unique returns true", false},
		{"a Unique on a map type", func(a *bites.App) { here(); a.Post("/x", ok[UniqueOnMapBody]) },
			"POST /x: UniqueOnMapBody.Body.M: bites_test.uniqueOnMap has a Unique method, but Unique says a list holds no item twice: it is read on a named slice type, and bites_test.uniqueOnMap is a map", false},
		{"a Unique on a string type", func(a *bites.App) { here(); a.Post("/x", ok[UniqueOnStringBody]) },
			"POST /x: UniqueOnStringBody.Body.S: bites_test.uniqueOnString has a Unique method, but Unique says a list holds no item twice: it is read on a named slice type, and bites_test.uniqueOnString is a string", false},
		{"a Unique on a list of structs", func(a *bites.App) { here(); a.Post("/x", ok[UniqueOfStructsBody]) },
			"POST /x: UniqueOfStructsBody.Body.L: bites_test.uniqueOfStructs.Unique cannot be checked: its item type struct { A string } is a struct, and only a string, boolean, number or UUID has an equality a client can be told: state which part of the item must not repeat in the Input's Validate", false},
		{"a Unique with another signature", func(a *bites.App) { here(); a.Post("/x", ok[UniqueWrongSigBody]) },
			"POST /x: UniqueWrongSigBody.Body.L: bites_test.uniqueWrongSig has a Unique method, so it says its items are distinct, but Unique must be func() bool: rename the method if it means something else", false},
		{"a Unique on a byte slice type", func(a *bites.App) { here(); a.Post("/x", ok[UniqueOnBytesBody]) },
			"POST /x: UniqueOnBytesBody.Body.B: bites_test.uniqueOnBytes has a Unique method, but it decodes itself, so its items are not counted", false},
		{"a Unique on an integer type", func(a *bites.App) { here(); a.Post("/x", ok[UniqueOnIntBody]) },
			"POST /x: UniqueOnIntBody.Body.N: bites_test.uniqueOnInt has a Unique method, but Unique says a list holds no item twice: it is read on a named slice type, and bites_test.uniqueOnInt is a int", false},
		{"a Unique that returns false, in Query", func(a *bites.App) { here(); a.Get("/x", ok[UniqueFalseQuery]) },
			"GET /x: UniqueFalseQuery.Query.L: bites_test.uniqueFalse.Unique returns false, which states nothing: drop the method, since a list may repeat an item unless Unique returns true", false},
		{"a Members on a list type", func(a *bites.App) { here(); a.Post("/x", ok[MembersOnListBody]) },
			"POST /x: MembersOnListBody.Body.L: bites_test.membersOnList has a Members method, but Members bounds a map and bites_test.membersOnList is a list: a list's length is its Items method, func() (min, max int)", false},
		{"a Members on a string type", func(a *bites.App) { here(); a.Post("/x", ok[MembersOnStringBody]) },
			"POST /x: MembersOnStringBody.Body.S: bites_test.membersOnString has a Members method, but Members bounds a map: it is read on a named map type, and bites_test.membersOnString is a string", false},
		{"a Members that is not a count", func(a *bites.App) { here(); a.Post("/x", ok[MembersBadBody]) },
			"POST /x: MembersBadBody.Body.M: bites_test.membersBad.Members returns (3, 1), which is not a count of members: it must be 0 <= min <= max with max at least 1", false},
		{"an Items on a map type", func(a *bites.App) { here(); a.Post("/x", ok[ItemsOnMapBody]) },
			"POST /x: ItemsOnMapBody.Body.M: bites_test.itemsOnMap has an Items method, but Items bounds a list and bites_test.itemsOnMap is a map: a map's size is its Members method, func() (min, max int)", false},
		{"a Members with another signature", func(a *bites.App) { here(); a.Post("/x", ok[MembersWrongSigBody]) },
			"POST /x: MembersWrongSigBody.Body.M: bites_test.membersWrongSig has a Members method, so it carries a bound, but Members must be func() (min, max int): rename the method if it means something else", false},
		{"a Members on an integer type", func(a *bites.App) { here(); a.Post("/x", ok[MembersOnIntBody]) },
			"POST /x: MembersOnIntBody.Body.N: bites_test.membersOnInt has a Members method, but Members bounds a map: it is read on a named map type, and bites_test.membersOnInt is a int", false},
		{"a multipleOf tag", func(a *bites.App) { here(); a.Post("/x", ok[TagMultipleOf]) },
			"POST /x: TagMultipleOf.Body.N has a multipleOf tag, which bites does not read, so the step would never be checked: a step is a MultipleOf method on the field's type", false},
		{"a uniqueItems tag", func(a *bites.App) { here(); a.Post("/x", ok[TagUniqueItems]) },
			"POST /x: TagUniqueItems.Body.L has a uniqueItems tag, which bites does not read, so the items would never be compared: a list that holds no item twice is a Unique method on the field's type", false},
		{"a minProperties tag", func(a *bites.App) { here(); a.Post("/x", ok[TagMinProperties]) },
			"POST /x: TagMinProperties.Body.M has a minProperties tag, which bites does not read, so the bound would never be checked: a map's bound is a Members method on the field's type", false},
		{"a Pattern that returns an empty string", func(a *bites.App) { here(); a.Post("/x", ok[EmptyPatternBody]) },
			"POST /x: EmptyPatternBody.Body.S: bites_test.emptyPattern.Pattern returns an empty string, which every value matches: remove the method or write a pattern", false},
		{"a Pattern that does not compile", func(a *bites.App) { here(); a.Post("/x", ok[BrokenPatternBody]) },
			"POST /x: BrokenPatternBody.Body.S: bites_test.brokenPattern.Pattern \"^[a-$\" does not compile: error parsing regexp: invalid character class range: `a-$`", false},
		{"a Pattern that uses the dot", func(a *bites.App) { here(); a.Post("/x", ok[DotPatternBody]) },
			"POST /x: DotPatternBody.Body.S: bites_test.dotPattern.Pattern \"^.+$\" uses ., which RE2 reads as any character but \\n and ECMA-262 as any but \\n, \\r, U+2028 and U+2029: write the class, as [^\\n\\r]", false},
		{"a Pattern that uses \\s", func(a *bites.App) { here(); a.Post("/x", ok[SpacePatternBody]) },
			"POST /x: SpacePatternBody.Body.S: bites_test.spacePattern.Pattern \"^\\\\s*$\" uses \\s, which RE2 reads as [\\t\\n\\f\\r ] and ECMA-262 as that and \\v, U+00A0, U+FEFF and the Unicode spaces: write the class, as [ \\t\\n\\r\\f]", false},
		{"a Pattern with a (?P<name>) group", func(a *bites.App) { here(); a.Post("/x", ok[NamedPatternBody]) },
			"POST /x: NamedPatternBody.Body.S: bites_test.namedPattern.Pattern \"(?P<year>\\\\d{4})\" uses a (?P<name>...) group, which ECMA-262 does not have: write (?<name>...) or (...)", false},
		{"a Pattern with an inline flag", func(a *bites.App) { here(); a.Post("/x", ok[FlagPatternBody]) },
			"POST /x: FlagPatternBody.Body.S: bites_test.flagPattern.Pattern \"(?i)abc\" uses a flag or lookaround group (?i, which ECMA-262 does not read as RE2 does: only (?:...) and (?<name>...) are allowed", false},
		{"a Pattern with \\A and \\z", func(a *bites.App) { here(); a.Post("/x", ok[TextAnchorPatternBody]) },
			"POST /x: TextAnchorPatternBody.Body.S: bites_test.textAnchorPattern.Pattern \"\\\\Aabc\\\\z\" uses the escape \\A, which RE2 and ECMA-262 (with the u flag) do not read alike: only \\d \\D \\w \\W \\b \\B \\t \\n \\r \\f \\v \\xHH, \\p{X} and a backslash before ^$\\.*+?()[]{}|/ are allowed", false},
		{"a Pattern with a POSIX class", func(a *bites.App) { here(); a.Post("/x", ok[PosixPatternBody]) },
			"POST /x: PosixPatternBody.Body.S: bites_test.posixPattern.Pattern \"^[[:alpha:]]+$\" uses a POSIX class like [[:alpha:]], which ECMA-262 does not have: write the range, as a-zA-Z", false},
		{"a Pattern with \\p without braces", func(a *bites.App) { here(); a.Post("/x", ok[ScriptPatternBody]) },
			"POST /x: ScriptPatternBody.Body.S: bites_test.scriptPattern.Pattern \"^\\\\pL+$\" uses \\p other than \\p{L} with a general category name: ECMA-262 has no one-letter \\pL and reads scripts as \\p{Script=Greek}", false},
		{"a Pattern with a { that is not a repeat count", func(a *bites.App) { here(); a.Post("/x", ok[BracePatternBody]) },
			"POST /x: BracePatternBody.Body.S: bites_test.bracePattern.Pattern \"a{\" has a { that does not begin a repeat count, which ECMA-262 refuses: write \\{", false},
		{"a Pattern with a class escape as a range start", func(a *bites.App) { here(); a.Post("/x", ok[ClassEscapeRangePatternBody]) },
			"POST /x: ClassEscapeRangePatternBody.Body.S: bites_test.classEscapeRangePattern.Pattern \"[\\\\d-x]\" uses a range with \\d, \\D, \\w or \\W as its start, which ECMA-262 refuses", false},
		{"a Pattern that repeats an anchor", func(a *bites.App) { here(); a.Post("/x", ok[AnchorRepeatPatternBody]) },
			"POST /x: AnchorRepeatPatternBody.Body.S: bites_test.anchorRepeatPattern.Pattern \"^*a\" repeats an anchor, which ECMA-262 refuses", false},
		{"a Pattern on an integer type", func(a *bites.App) { here(); a.Post("/x", ok[IntPatternBody]) },
			"POST /x: IntPatternBody.Body.S: bites_test.intPattern has a Pattern method, but Pattern is read on a string type, and bites_test.intPattern is a int32", false},
		{"a Pattern method that means something else", func(a *bites.App) { here(); a.Post("/x", ok[PatternIsALayoutBody]) },
			"POST /x: PatternIsALayoutBody.Body.S: bites_test.patternIsALayout has a Pattern method, so it carries a pattern, but Pattern must be func() string: rename the method if it means something else", false},
		{"a Pattern on an enum", func(a *bites.App) { here(); a.Post("/x", ok[EnumPatternBody]) },
			"POST /x: EnumPatternBody.Body.S: bites_test.enumPattern has a Pattern method and a Values method, but an enum lists every value it accepts: drop the Pattern", false},
		{"a Pattern on a type that decodes itself", func(a *bites.App) { here(); a.Post("/x", ok[TextPatternBody]) },
			"POST /x: TextPatternBody.Body.S: bites_test.textPattern has a Pattern method, but it decodes itself, so its pattern would not be checked", false},
		{"a Range on a float type with a NaN bound", func(a *bites.App) { here(); a.Post("/x", ok[NaNRangeBody]) }, `POST /x: NaNRangeBody.Body.F: bites_test.nanRange.Range returns (NaN, 1), and a bound must be a finite number: use -math.MaxFloat64 or math.MaxFloat64 for no bound on a side`, false},
		{"a Range on a float type with an infinite bound", func(a *bites.App) { here(); a.Post("/x", ok[InfRangeBody]) }, `POST /x: InfRangeBody.Body.F: bites_test.infRange.Range returns (0, +Inf), and a bound must be a finite number: use -math.MaxFloat64 or math.MaxFloat64 for no bound on a side`, false},
		{"a Range on a float type whose max is below its min", func(a *bites.App) { here(); a.Post("/x", ok[BackwardsFloatRangeBody]) }, `POST /x: BackwardsFloatRangeBody.Body.F: bites_test.backwardsFloatRange.Range returns (5, 1.5), and max is below min, so no value could be valid`, false},
		{"a Range on a float64 type that is the whole of it", func(a *bites.App) { here(); a.Post("/x", ok[WholeFloatRangeBody]) }, `POST /x: WholeFloatRangeBody.Body.F: bites_test.wholeFloatRange.Range returns the whole of float64 and bounds nothing: drop the method`, false},
		{"a Range on a float32 type that is the whole of it", func(a *bites.App) { here(); a.Post("/x", ok[WholeFloat32RangeBody]) }, `POST /x: WholeFloat32RangeBody.Body.F: bites_test.wholeFloat32Range.Range returns the whole of float32 and bounds nothing: drop the method`, false},
		{"a Range on a float32 type with a bound outside float32", func(a *bites.App) { here(); a.Post("/x", ok[WideFloat32RangeBody]) }, `POST /x: WideFloat32RangeBody.Body.F: bites_test.wideFloat32Range.Range returns (0, 1e+39), which is outside the range of a float32 (-3.4028234663852886e+38 to 3.4028234663852886e+38): use -math.MaxFloat32 or math.MaxFloat32 for no bound on a side, or make bites_test.wideFloat32Range a float64 type`, false},
		{"a Range with integer bounds on a float type", func(a *bites.App) { here(); a.Post("/x", ok[IntRangeOnFloatBody]) }, `POST /x: IntRangeOnFloatBody.Body.F: bites_test.intRangeOnFloat has a Range method that returns int64 bounds, but a float type's bound is float64: name it Range, func() (min, max float64), or make bites_test.intRangeOnFloat an int64 type`, false},
		{"a Range with float bounds on an integer type", func(a *bites.App) { here(); a.Post("/x", ok[FloatRangeOnIntBody]) }, `POST /x: FloatRangeOnIntBody.Body.N: bites_test.floatRangeOnInt has a Range method that returns float64 bounds, but an integer type's bound is int64: name it Range, func() (min, max int64), or make bites_test.floatRangeOnInt a float64 type`, false},
		{"a Range on a float type with another signature", func(a *bites.App) { here(); a.Post("/x", ok[FloatRangeIsACountBody]) }, `POST /x: FloatRangeIsACountBody.Body.F: bites_test.floatRangeIsACount has a Range method, so it carries a bound, but Range must be func() (min, max float64): rename the method if it means something else`, false},
		{"a checked Format on a type that is not a string", func(a *bites.App) { here(); a.Post("/x", ok[CheckedFormatOnListBody]) }, `POST /x: CheckedFormatOnListBody.Body.L: bites_test.checkedFormatOnList.Format returns "email", a format the framework checks, but bites_test.checkedFormatOnList is a slice and only a string can be checked: make it a string type, or name another format`, false},
		{"a checked Format on a type that is not a string, in a response", func(a *bites.App) { here(); a.Get("/x", outOf[CheckedFormatOnListOut]) }, `GET /x: the output type cannot be written as JSON: CheckedFormatOnListOut.L: bites_test.checkedFormatOnList.Format returns "email", a format the framework checks, but bites_test.checkedFormatOnList is a slice and only a string can be checked: make it a string type, or name another format`, false},
		{"a checked Format on an enum", func(a *bites.App) { here(); a.Post("/x", ok[CheckedFormatOnEnumBody]) }, `POST /x: CheckedFormatOnEnumBody.Body.S: bites_test.checkedFormatOnEnum has a Format method naming "uuid", which the framework checks, and a Values method, but an enum lists every value it accepts: drop the Format or the Values`, false},
		{"a checked Format on a type that decodes itself", func(a *bites.App) { here(); a.Post("/x", ok[CheckedFormatOnTextBody]) }, `POST /x: CheckedFormatOnTextBody.Body.S: bites_test.checkedFormatOnText has a Format method naming "hostname", which the framework checks, but it decodes itself, so its format would not be checked: drop the Format or check in its UnmarshalText`, false},
		{"a constraint type as a map key", func(a *bites.App) { here(); a.Post("/x", ok[KeyedBody]) },
			"POST /x: KeyedBody.Body.ByName: a map key cannot be wire.NonEmpty: a key is not checked against a constraint, so the type would promise what nothing enforces", false},
		{"a pointer in Body", func(a *bites.App) { here(); a.Post("/x", ok[BodyPtr]) },
			"POST /x: BodyPtr.Body.Note: a pointer cannot say whether nil meant absent or null, use Opt[T] or Null[T]", false},
		{"a Body field that decodes itself and has no registered schema", func(a *bites.App) { here(); a.Post("/x", ok[BodyStamp]) },
			`POST /x: BodyStamp.Body.At: bites_test.stamp decodes itself through its own UnmarshalJSON, so the API document cannot see its shape and would describe it as {}: register it with WithJSONSchema[bites_test.stamp](JSONSchema{Type: ..., Format: ...}), or with JSONSchema{Type: "any"} if it really is free-form JSON`, false},
		{"a Body list whose elements decode themselves and have no registered schema", func(a *bites.App) { here(); a.Post("/x", ok[BodyStamps]) },
			`POST /x: BodyStamps.Body.Ats[]: bites_test.stamp decodes itself through its own UnmarshalJSON, so the API document cannot see its shape and would describe it as {}: register it with WithJSONSchema[bites_test.stamp](JSONSchema{Type: ..., Format: ...}), or with JSONSchema{Type: "any"} if it really is free-form JSON`, false},
		{"an output field that encodes itself and has no registered schema", func(a *bites.App) { here(); a.Get("/x", out[StampOut]) },
			`GET /x: the output type cannot be written as JSON: StampOut.At: bites_test.stamp encodes itself through its own MarshalJSON, so the API document cannot see its shape and would describe it as {}: register it with WithJSONSchema[bites_test.stamp](JSONSchema{Type: ..., Format: ...}), or with JSONSchema{Type: "any"} if it really is free-form JSON`, false},
		{"an output map of lists that encodes itself and has no registered schema", func(a *bites.App) { here(); a.Get("/x", out[StampsOut]) },
			`GET /x: the output type cannot be written as JSON: StampsOut.By[key][]: bites_test.stamp encodes itself through its own MarshalJSON, so the API document cannot see its shape and would describe it as {}: register it with WithJSONSchema[bites_test.stamp](JSONSchema{Type: ..., Format: ...}), or with JSONSchema{Type: "any"} if it really is free-form JSON`, false},
		{"an interface in Body", func(a *bites.App) { here(); a.Post("/x", ok[BodyAny]) },
			"POST /x: BodyAny.Body.Extra: a field of interface type cannot be decoded", false},
		{"a Body that is a string", func(a *bites.App) { here(); a.Post("/x", ok[BodyString]) },
			"POST /x: BodyString.Body is string; a Body is a struct, slice or map (read as JSON), []byte, Raw[T], Form[T], Multipart[T], Submitted[T], Upload or BodyReader", false},
		{"a Submitted of an int", func(a *bites.App) { here(); a.Post("/x", okPage[SubmittedOfInt]) },
			"POST /x: SubmittedOfInt.Body is a Submitted of int; a Submitted wraps a struct whose fields are the form fields", false},
		{"a Submitted form on a route that answers no Page", func(a *bites.App) { here(); a.Post("/x", ok[SubmittedForm]) },
			"POST /x: the Body is a Submitted form, whose mistakes the handler shows itself, but the handler returns struct {} and no Page to show the form in: return a Page (or a OneOf with one, such as OneOf2[SeeOther, Page]), or read the body as Form[T] and let the framework answer a bad form with the 400", false},
		{"a Form of an int", func(a *bites.App) { here(); a.Post("/x", ok[FormOfInt]) },
			"POST /x: FormOfInt.Body is a Form of int; a Form wraps a struct whose fields are the form fields", false},
		{"a Form of an Opt", func(a *bites.App) { here(); a.Post("/x", ok[FormOfOpt]) },
			"POST /x: FormOfOpt.Body is a Form of Opt[int]; a Form wraps a struct whose fields are the form fields", false},
		{"a Form with a struct field", func(a *bites.App) { here(); a.Post("/x", ok[FormNested]) },
			"POST /x: FormNested.Body.Inner: struct { A string } is a struct; text holds one scalar, give it an UnmarshalText method or use a type of its own", false},
		{"a Form with a Null field", func(a *bites.App) { here(); a.Post("/x", ok[FormNull]) },
			"POST /x: FormNull.Body.N is a Null; text has no null, so the type would promise a state no request can produce: use Opt[T] for a value that may be missing", false},
		{"a Form on a GET", func(a *bites.App) { here(); a.Get("/x", ok[FormOfInt]) },
			"GET /x: FormOfInt.Body is a Body section, but a GET route has no request body", false},
		{"a pointer to a Form", func(a *bites.App) { here(); a.Post("/x", ok[FormPtr]) },
			"POST /x: FormPtr.Body is *bites.Form[struct { N string }]; a Body is a struct, slice or map (read as JSON), []byte, Raw[T], Form[T], Multipart[T], Submitted[T], Upload or BodyReader", false},
		{"a Body that is an Opt of a scalar", func(a *bites.App) { here(); a.Post("/x", ok[BodyOpt]) },
			"POST /x: BodyOpt.Body is Opt[string]; an Opt Body is a JSON body that may be absent, so it holds a struct, slice or map", false},
		{"a Body that is an Opt of a Raw", func(a *bites.App) { here(); a.Post("/x", ok[BodyOptRaw]) },
			"POST /x: BodyOptRaw.Body is Opt[Raw[struct { N int }]]; an Opt Body is a JSON body that may be absent, and a Raw[struct { N int }] is a Body of its own: give it without Opt", false},
		{"an output that cannot be written as JSON", func(a *bites.App) { here(); a.Get("/x", chanOut) },
			"GET /x: the output type cannot be written as JSON: chan int: a field of kind chan cannot be encoded", false},
		{"a nil handler", func(a *bites.App) { here(); a.Get("/x", (func(context.Context, Empty) (Empty, error))(nil)) },
			"GET /x: the handler is nil", false},
		{"a typed route with no method", func(a *bites.App) { here(); a.Handle("", "/x", ok[Empty]) },
			" /x: a typed route needs a method; a method-less pattern is for mounting a plain http.Handler", false},
		{"a method that is not a token", func(a *bites.App) { here(); a.Handle("GE T", "/x", ok[Empty]) },
			`invalid method "GE T" registered at refusal_test.go:%d: a method is a token`, true},
		{"a pattern without a leading slash", func(a *bites.App) { here(); a.Get("x", ok[Empty]) },
			`invalid pattern "x" registered at refusal_test.go:%d: must start with '/'`, true},
		{"two patterns that match the same requests", func(a *bites.App) { hereFirst(); a.Get("/dup", ok[Empty]); here(); a.Get("/dup", ok[Empty]) },
			`pattern "GET /dup" (registered at refusal_test.go:%d) conflicts with pattern "GET /dup" (registered at refusal_test.go:%d): they match the same requests`, true},
		{"a group prefix without a slash", func(a *bites.App) { here(); a.Group("admin") },
			"group \"admin\": a group prefix starts with a slash, does not end with one, and has no wildcard; to give options to routes that share no prefix, bundle them once with bites.Options(...) and pass that to each route", false},
		{"a group MaxBody of zero", func(a *bites.App) { here(); a.Group("/g", bites.MaxBody(0)) },
			`group "/g": MaxBody(0) is not a limit: it must be a positive number of bytes`, false},
		{"a nested group MaxBody below zero", func(a *bites.App) { here(); a.Group("/g", bites.MaxBody(5)).Group("/n", bites.MaxBody(-1)) },
			`group "/g/n": MaxBody(-1) is not a limit: it must be a positive number of bytes`, false},
		{"a group BodyTimeout of zero", func(a *bites.App) { here(); a.Group("/g", bites.BodyTimeout(0)) },
			`group "/g": BodyTimeout(0s) is not a limit: it must be a positive duration`, false},
		{"a nil group option", func(a *bites.App) { here(); a.Group("/g", nil) },
			`group "/g": an option is nil`, false},
		{"a route MaxBody of zero", func(a *bites.App) { here(); a.Get("/x", ok[Empty], bites.MaxBody(0)) },
			"GET /x: MaxBody(0) is not a limit: it must be a positive number of bytes", false},
		{"a route BodyTimeout below zero", func(a *bites.App) { here(); a.Post("/x", ok[Empty], bites.BodyTimeout(-time.Second)) },
			"POST /x: BodyTimeout(-1s) is not a limit: it must be a positive duration", false},
		{"a route of a group with a nil option", func(a *bites.App) { here(); a.Group("/g", bites.MaxBody(5)).Get("/x", ok[Empty], nil) },
			"GET /g/x: an option is nil", false},
		{"a nil app middleware", func(a *bites.App) { here(); a.Use(passthru, nil) },
			"app: Use: middleware 2 is nil", false},
		{"a nil group middleware", func(a *bites.App) { here(); a.Group("/g").Use(nil) },
			`group "/g": Use: middleware 1 is nil`, false},
		{"a nil middleware in a group's Use option", func(a *bites.App) { here(); a.Group("/g", bites.Use(passthru, nil)) },
			`group "/g": Use: middleware 2 is nil`, false},
		{"a nil middleware in a route's Use option", func(a *bites.App) { here(); a.Get("/x", ok[Empty], bites.Use(nil)) },
			"GET /x: Use: middleware 1 is nil", false},
		{"a nil middleware in a nested group's Use option", func(a *bites.App) { here(); a.Group("/g").Group("/n", bites.Use(nil)) },
			`group "/g/n": Use: middleware 1 is nil`, false},
		{"group Use after a route of the group", func(a *bites.App) {
			g := a.Group("/g")
			func() { here(); g.Get("/x", ok[Empty]) }()
			func() { hereFirst(); g.Use(passthru) }()
		}, `group "/g": Use is called after GET /g/x (at refusal_test.go:%d) was registered in this group, so it would not reach it: move Use above it` + "\n\tregistered at refusal_test.go:%d", true},
		{"group Use after a child group", func(a *bites.App) {
			g := a.Group("/g")
			func() { here(); g.Group("/c") }()
			func() { hereFirst(); g.Use(passthru) }()
		}, `group "/g": Use is called after the group "/g/c" (at refusal_test.go:%d) was registered in this group, so it would not reach it: move Use above it` + "\n\tregistered at refusal_test.go:%d", true},
		{"a UseEach function that refuses an app route", func(a *bites.App) {
			here(); a.UseEach(refuseAll)
			hereFirst(); a.Get("/x", ok[Empty])
		}, "GET /x: the function given to UseEach (at refusal_test.go:%d) refused the route: no permission for getX\n\tregistered at refusal_test.go:%d", true},
		{"a UseEach function that refuses a route of a group", func(a *bites.App) {
			g := a.Group("/g")
			here(); g.UseEach(refuseAll)
			hereFirst(); g.Post("/y", ok[Empty])
		}, "POST /g/y: the function given to UseEach (at refusal_test.go:%d) refused the route: no permission for postGY\n\tregistered at refusal_test.go:%d", true},
		{"a UseEach function given as an option that refuses a route", func(a *bites.App) {
			here(); a.Get("/z", ok[Empty], bites.OperationID("z.get"), bites.UseEach(refuseAll))
		}, "GET /z: the function given to UseEach (at refusal_test.go:%[1]d) refused the route: no permission for z.get\n\tregistered at refusal_test.go:%[1]d", true},
		{"a UseEach function that refuses a WebSocket route", func(a *bites.App) {
			here(); a.UseEach(refuseAll)
			hereFirst(); a.WebSocket("/ws", wsOK)
		}, "WebSocket /ws: the function given to UseEach (at refusal_test.go:%d) refused the route: no permission for getWs\n\tregistered at refusal_test.go:%d", true},
		{"a nil UseEach function on the app", func(a *bites.App) { here(); a.UseEach(nil) },
			"app: UseEach: the function is nil", false},
		{"a nil UseEach function on a group", func(a *bites.App) { here(); a.Group("/g").UseEach(nil) },
			`group "/g": UseEach: the function is nil`, false},
		{"a nil UseEach function as a route option", func(a *bites.App) { here(); a.Get("/x", ok[Empty], bites.UseEach(nil)) },
			"GET /x: UseEach: the function is nil", false},
		{"app UseEach after a route", func(a *bites.App) {
			func() { here(); a.Get("/x", ok[Empty]) }()
			func() { hereFirst(); a.UseEach(refuseAll) }()
		}, "app: UseEach is called after GET /x (at refusal_test.go:%d) was registered, so it would not reach it: move UseEach above it\n\tregistered at refusal_test.go:%d", true},
		{"app UseEach after a group", func(a *bites.App) {
			func() { here(); a.Group("/g") }()
			func() { hereFirst(); a.UseEach(refuseAll) }()
		}, `app: UseEach is called after the group "/g" (at refusal_test.go:%d) was registered, so it would not reach it: move UseEach above it` + "\n\tregistered at refusal_test.go:%d", true},
		{"group UseEach after a route of the group", func(a *bites.App) {
			g := a.Group("/g")
			func() { here(); g.Get("/x", ok[Empty]) }()
			func() { hereFirst(); g.UseEach(refuseAll) }()
		}, `group "/g": UseEach is called after GET /g/x (at refusal_test.go:%d) was registered in this group, so it would not reach it: move UseEach above it` + "\n\tregistered at refusal_test.go:%d", true},
		{"a UseEach function that refuses the API document", func(a *bites.App) {
			here(); a.UseEach(refuseAll)
			hereFirst(); a.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		}, "OpenAPI /openapi.json: the function given to UseEach (at refusal_test.go:%d) refused the route: no permission for \n\tregistered at refusal_test.go:%d", true},
		{"a UseEach function that refuses the metrics page", func(a *bites.App) {
			here(); a.UseEach(refuseAll)
			hereFirst(); a.ServeMetrics("/metrics", bites.NewMetrics())
		}, "ServeMetrics /metrics: the function given to UseEach (at refusal_test.go:%d) refused the route: no permission for \n\tregistered at refusal_test.go:%d", true},
		{"app UseEach after OpenAPI", func(a *bites.App) {
			func() { here(); a.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"}) }()
			func() { hereFirst(); a.UseEach(refuseAll) }()
		}, "app: UseEach is called after OpenAPI /openapi.json (at refusal_test.go:%d) was registered, so it would not reach it: move UseEach above it\n\tregistered at refusal_test.go:%d", true},
		{"app UseEach after ServeMetrics", func(a *bites.App) {
			func() { here(); a.ServeMetrics("/metrics", bites.NewMetrics()) }()
			func() { hereFirst(); a.UseEach(refuseAll) }()
		}, "app: UseEach is called after ServeMetrics /metrics (at refusal_test.go:%d) was registered, so it would not reach it: move UseEach above it\n\tregistered at refusal_test.go:%d", true},
		{"a provided type whose Provider needs a type no Provider covers", func(a *bites.App) { here(); a.Provide(needsB); a.Get("/x", ok[WithA]) },
			"GET /x: WithA.A needs PA (at refusal_test.go:%[1]d) needs PB, and no Provider of PB covers this route\n\tregistered at refusal_test.go:%[1]d", true},
		{"Providers that need each other", func(a *bites.App) { here(); a.Provide(needsB); a.Provide(needsA); a.Get("/x", ok[WithA]) },
			"GET /x: the Providers form a cycle: WithA.A needs PA (at refusal_test.go:%[1]d) needs PB (at refusal_test.go:%[1]d) needs PA\n\tregistered at refusal_test.go:%[1]d", true},
		{"a Provider that needs its own type", func(a *bites.App) { here(); a.Provide(selfA) },
			"app: Provide[PA]: the Provider of PA needs PA, which is a cycle; a Provider that refines a value provides a distinct type, such as Admin from User", false},
		{"a Provider whose dependencies are not a struct", func(a *bites.App) { here(); a.Provide(intDeps) },
			"app: Provide[PA]: the Provider's dependencies are int; they must be a struct whose fields are the provided types it needs, or struct{} when it needs none", false},
		{"a Provider with an unexported dependency", func(a *bites.App) { here(); a.Provide(hiddenDep) },
			"app: Provide[PA]: the dependency b is unexported, so nothing can fill it", false},
		{"a Provider that asks for a Path section", func(a *bites.App) { here(); a.Provide(pathDep) },
			"app: Provide[PA]: the dependency Path is a section a Provider cannot read; a Provider reads a Header, a Cookie or (on a route a browser opens without headers) a Query section, and the path and the body are the Input's: ask for the value in the Input, or give the Provider a Header", false},
		{"a Provider's Query section on a route that is not a WebSocket or an event stream", func(a *bites.App) {
			a.Provide(queryDep)
			here(); a.Get("/x", ok[WithA])
		}, "GET /x: WithA: Provide[PA] reads a Query section, which a Provider may read only on a route a browser opens without headers of its own, a WebSocket route or one that answers SSE or Events: elsewhere send the value in a header, or ask for it in the Input", false},
		{"a Provider whose Header section is not a struct", func(a *bites.App) { here(); a.Provide(stringHeaderDep) },
			"app: Provide[PA]: the dependency Header is a section and must be a struct, not string", false},
		{"a nil Provider", func(a *bites.App) { here(); a.Provide[PA, struct{}](nil) },
			"app: Provide[PA]: the Provider is nil", false},
		{"a nil Provider as an option", func(a *bites.App) { here(); a.Get("/x", ok[Empty], bites.Provide[PA, struct{}](nil)) },
			"GET /x: Provide[PA]: the Provider is nil", false},
		{"a second Provider of a type in the app", func(a *bites.App) { here(); a.Provide(provA); a.Provide(provA) },
			"app: Provide[PA]: this scope already has a Provider of PA (at refusal_test.go:%[1]d); a scope has one Provider for a type\n\tregistered at refusal_test.go:%[1]d", true},
		{"a second Provider of a type in a group's options", func(a *bites.App) { here(); a.Group("/g", bites.Provide(provA), bites.Provide(provA)) },
			`group "/g": Provide[PA]: this scope already has a Provider of PA (at refusal_test.go:%[1]d); a scope has one Provider for a type` + "\n\tregistered at refusal_test.go:%[1]d", true},
		{"a group Provider of a type the app provides", func(a *bites.App) { here(); a.Provide(provA); a.Group("/g").Provide(provA) },
			`group "/g": Provide[PA]: an enclosing scope already has a Provider of PA (at refusal_test.go:%[1]d); use Override[PA] to replace it for this scope and those inside it` + "\n\tregistered at refusal_test.go:%[1]d", true},
		{"a route Provider of a type a group provides", func(a *bites.App) {
			var g *bites.Group
			func() { here(); g = a.Group("/g", bites.Provide(provA)) }()
			func() { hereFirst(); g.Get("/x", ok[Empty], bites.Provide(provA)) }()
		}, "GET /g/x: Provide[PA]: an enclosing scope already has a Provider of PA (at refusal_test.go:%d); use Override[PA] to replace it for this scope and those inside it\n\tregistered at refusal_test.go:%d", true},
		{"an Override with nothing to replace", func(a *bites.App) { here(); a.Group("/g").Override(provA) },
			`group "/g": Override[PA]: no enclosing scope has a Provider of PA to replace; use Provide[PA]`, false},
		{"an Override of the same scope's own Provider", func(a *bites.App) { here(); a.Group("/g", bites.Provide(provA), bites.Override(provA)) },
			`group "/g": Override[PA]: the Provider of PA it would replace is this scope's own (at refusal_test.go:%[1]d); an Override replaces one from an enclosing scope, so keep one of the two` + "\n\tregistered at refusal_test.go:%[1]d", true},
		{"an Override option with nothing to replace", func(a *bites.App) { here(); a.Get("/x", ok[Empty], bites.Override(provA)) },
			"GET /x: Override[PA]: no enclosing scope has a Provider of PA to replace; use Provide[PA]", false},
		{"a route that needs more Providers than a route runs", func(a *bites.App) { provideMany(a); here(); a.Get("/x", ok[Many]) },
			"GET /x: Many needs 17 Providers; a route runs at most 16", false},
		{"group Provide after a route of the group", func(a *bites.App) {
			g := a.Group("/g")
			func() { here(); g.Get("/x", ok[Empty]) }()
			func() { hereFirst(); g.Provide(provA) }()
		}, `group "/g": Provide is called after GET /g/x (at refusal_test.go:%d) was registered in this group, so it would not reach it: move Provide above it` + "\n\tregistered at refusal_test.go:%d", true},
		{"group Provide after a child group", func(a *bites.App) {
			g := a.Group("/g")
			func() { here(); g.Group("/c") }()
			func() { hereFirst(); g.Provide(provA) }()
		}, `group "/g": Provide is called after the group "/g/c" (at refusal_test.go:%d) was registered in this group, so it would not reach it: move Provide above it` + "\n\tregistered at refusal_test.go:%d", true},
		{"group Override after a route of the group", func(a *bites.App) {
			a.Provide(provA)
			g := a.Group("/g")
			func() { here(); g.Get("/x", ok[Empty]) }()
			func() { hereFirst(); g.Override(provA) }()
		}, `group "/g": Override is called after GET /g/x (at refusal_test.go:%d) was registered in this group, so it would not reach it: move Override above it` + "\n\tregistered at refusal_test.go:%d", true},
		{"app Provide after a route", func(a *bites.App) {
			func() { here(); a.Get("/x", ok[Empty]) }()
			func() { hereFirst(); a.Provide(provA) }()
		}, "app: Provide is called after GET /x (at refusal_test.go:%d) was registered, which was checked without it: move Provide above it\n\tregistered at refusal_test.go:%d", true},
		{"app Provide after a group", func(a *bites.App) {
			func() { here(); a.Group("/g") }()
			func() { hereFirst(); a.Provide(provA) }()
		}, `app: Provide is called after the group "/g" (at refusal_test.go:%d) was registered, which was checked without it: move Provide above it` + "\n\tregistered at refusal_test.go:%d", true},
		{"a pointer to a Stream as the output", func(a *bites.App) { here(); a.Get("/x", streamPtrOut) },
			"GET /x: the handler returns *Stream; a stream is returned by value, because its function and its writer live for one response", false},
		{"a pointer to an SSE as the output", func(a *bites.App) { here(); a.Get("/x", ssePtrOut) },
			"GET /x: the handler returns *SSE; a stream is returned by value, because its function and its writer live for one response", false},
		{"a stream as the Body of a status wrapper", func(a *bites.App) { here(); a.Get("/x", okStreamOut) },
			"GET /x: SSE is the Body of OK; a stream is the whole return type, not the body of a wrapper (its status and headers are its own fields)", false},
		{"a HEAD route that returns a stream", func(a *bites.App) { here(); a.Head("/x", streamOut) },
			"HEAD /x: a HEAD route cannot return a stream: its answer has no body to stream", false},
		{"a pointer to an Events as the output", func(a *bites.App) { here(); a.Get("/x", eventsPtrOut) },
			"GET /x: the handler returns *Events[User]; a stream is returned by value, because its function and its writer live for one response", false},
		{"an Events as the Body of a status wrapper", func(a *bites.App) { here(); a.Get("/x", okEventsOut) },
			"GET /x: Events[User] is the Body of OK; a stream is the whole return type, not the body of a wrapper (its status and headers are its own fields)", false},
		{"a HEAD route that returns an Events", func(a *bites.App) { here(); a.Head("/x", eventsOut[User]) },
			"HEAD /x: a HEAD route cannot return a stream: its answer has no body to stream", false},
		{"an Events whose event type cannot be written as JSON", func(a *bites.App) { here(); a.Get("/x", eventsOut[chan int]) },
			"GET /x: the handler returns Events[chan int], whose event type cannot be written as JSON: chan int: a field of kind chan cannot be encoded", false},
		{"an Events of []byte", func(a *bites.App) { here(); a.Get("/x", eventsOut[[]byte]) },
			"GET /x: the handler returns Events[[]byte]; an event's data is text, and []byte would be sent as base64 JSON: send bytes as text with an SSE and its EventWriter", false},
		{"an Events of one of the framework's response types", func(a *bites.App) { here(); a.Get("/x", eventsOut[bites.Text]) },
			"GET /x: the handler returns Events[Text]; Text is one of the framework's response types, and an event's data is a plain value, written as JSON, with no status or headers", false},
		{"an Events of an Events", func(a *bites.App) { here(); a.Get("/x", eventsOut[bites.Events[User]]) },
			"GET /x: the handler returns Events[Events[User]]; Events[User] is one of the framework's response types, and an event's data is a plain value, written as JSON, with no status or headers", false},
		{"an Upload route with no MaxBody", func(a *bites.App) { here(); a.Post("/x", ok[UploadIn]) },
			"POST /x: UploadIn.Body is an Upload, but the route has no MaxBody: how much a route may receive is the application's decision (an avatar wants 2 MiB, a video 2 GiB), so there is no default; give MaxBody(n) to the route or to a group above it", false},
		{"an Upload route with timeout and in-flight limits but no MaxBody", func(a *bites.App) { here(); a.Post("/x", ok[UploadIn], bites.BodyTimeout(time.Minute)) },
			"POST /x: UploadIn.Body is an Upload, but the route has no MaxBody: how much a route may receive is the application's decision (an avatar wants 2 MiB, a video 2 GiB), so there is no default; give MaxBody(n) to the route or to a group above it", false},
		{"a BodyReader route with no MaxBody", func(a *bites.App) { here(); a.Put("/x", ok[BodyReaderIn]) },
			"PUT /x: BodyReaderIn.Body is a BodyReader, but the route has no MaxBody: how much a route may receive is the application's decision (a part wants 8 MiB, a video 2 GiB), so there is no default; give MaxBody(n) to the route or to a group above it", false},
		{"a BodyReader on a GET route", func(a *bites.App) { here(); a.Get("/x", ok[BodyReaderIn], bites.MaxBody(1<<20)) },
			"GET /x: BodyReaderIn.Body is a Body section, but a GET route has no request body", false},
		{"a BodyReader as a field of a JSON body", func(a *bites.App) { here(); a.Put("/x", ok[BodyReaderInJSON], bites.MaxBody(1<<20)) },
			"PUT /x: BodyReaderInJSON.Body holds a BodyReader; a BodyReader is the whole Body, read as it arrives, not a field of a JSON body", false},
		{"an Upload on a GET route", func(a *bites.App) { here(); a.Get("/x", ok[UploadIn], bites.MaxBody(1<<20)) },
			"GET /x: UploadIn.Body is a Body section, but a GET route has no request body", false},
		{"an Upload as a field of a JSON body", func(a *bites.App) { here(); a.Post("/x", ok[UploadInJSON], bites.MaxBody(1<<20)) },
			"POST /x: UploadInJSON.Body holds an Upload; an Upload is the whole Body, read part by part, not a field of a JSON body", false},
		{"a list of Uploads as the Body", func(a *bites.App) { here(); a.Post("/x", ok[UploadInList], bites.MaxBody(1<<20)) },
			"POST /x: UploadInList.Body holds an Upload; an Upload is the whole Body, read part by part, not a field of a JSON body", false},
		{"an Upload in a text section", func(a *bites.App) { here(); a.Post("/x", ok[UploadInQuery], bites.MaxBody(1<<20)) },
			"POST /x: UploadInQuery.Query.File: bites.Upload is a struct; text holds one scalar, give it an UnmarshalText method or use a type of its own", false},
		{"a Multipart of an int", func(a *bites.App) { here(); a.Post("/x", ok[MPOfInt], bites.MaxBody(1<<20)) },
			"POST /x: MPOfInt.Body is a Multipart of int, but a Multipart wraps a struct whose fields are the form fields and files", false},
		{"a Multipart of an Opt", func(a *bites.App) { here(); a.Post("/x", ok[MPOfOpt], bites.MaxBody(1<<20)) },
			"POST /x: MPOfOpt.Body is a Multipart of Opt[int], but a Multipart wraps a struct whose fields are the form fields and files", false},
		{"a Multipart route with no MaxBody", func(a *bites.App) { here(); a.Post("/x", ok[MPPlain]) },
			"POST /x: MPPlain.Body has no MaxBody: how much a route may receive is the application's decision (an avatar wants 2 MiB, a video 2 GiB), so there is no default; give the route or its group MaxBody", false},
		{"a Submitted with a file and no MaxBody", func(a *bites.App) { here(); a.Post("/x", okPage[MPSubFile]) },
			"POST /x: MPSubFile.Body has no MaxBody: how much a route may receive is the application's decision (an avatar wants 2 MiB, a video 2 GiB), so there is no default; give the route or its group MaxBody", false},
		{"a Multipart on a GET route", func(a *bites.App) { here(); a.Get("/x", ok[MPPlain], bites.MaxBody(1<<20)) },
			"GET /x: MPPlain.Body is a Multipart on a GET route; a body is sent with POST, PUT or PATCH", false},
		{"a FormFile in a Form", func(a *bites.App) { here(); a.Post("/x", ok[FormWithFile]) },
			"POST /x: FormWithFile.Body.Value.Cover is a FormFile in a Form; a Form is read as application/x-www-form-urlencoded and cannot carry a file, so make the Body a Multipart", false},
		{"a FormFile in a Query", func(a *bites.App) { here(); a.Post("/x", ok[QueryFile]) },
			"POST /x: QueryFile.Query.Cover is a FormFile in a Query; a file is a part of a multipart body, so make the Body a Multipart", false},
		{"a FormFile in a Header", func(a *bites.App) { here(); a.Post("/x", ok[HeaderFile]) },
			"POST /x: HeaderFile.Header.Cover is a FormFile in a Header; a file is a part of a multipart body, so make the Body a Multipart", false},
		{"a FormFile in a JSON body", func(a *bites.App) { here(); a.Post("/x", ok[JSONFile]) },
			"POST /x: JSONFile.Body.Cover is a FormFile in a JSON body; a file is a part of a multipart body, so make the Body a Multipart", false},
		{"a pointer to a FormFile", func(a *bites.App) { here(); a.Post("/x", ok[MPPtrFile], bites.MaxBody(1<<20)) },
			"POST /x: MPPtrFile.Body.Value.Cover is a *bites.FormFile; a file field is a FormFile, an Opt of one, or a slice of one", false},
		{"a map of FormFile", func(a *bites.App) { here(); a.Post("/x", ok[MPMapFile], bites.MaxBody(1<<20)) },
			"POST /x: MPMapFile.Body.Value.Cover is a map[string]bites.FormFile; a file field is a FormFile, an Opt of one, or a slice of one", false},
		{"an array of FormFile", func(a *bites.App) { here(); a.Post("/x", ok[MPArrayFile], bites.MaxBody(1<<20)) },
			"POST /x: MPArrayFile.Body.Value.Cover is a [2]bites.FormFile; a file field is a FormFile, an Opt of one, or a slice of one", false},
		{"an Opt of a slice of FormFile", func(a *bites.App) { here(); a.Post("/x", ok[MPOptList], bites.MaxBody(1<<20)) },
			"POST /x: MPOptList.Body.Value.Cover is a bites.Opt[[]bites.FormFile]; a file field is a FormFile, an Opt of one, or a slice of one", false},
		{"a slice of Opt of FormFile", func(a *bites.App) { here(); a.Post("/x", ok[MPListOpt], bites.MaxBody(1<<20)) },
			"POST /x: MPListOpt.Body.Value.Cover is a []bites.Opt[bites.FormFile]; a file field is a FormFile, an Opt of one, or a slice of one", false},
		{"a []byte as a file field", func(a *bites.App) { here(); a.Post("/x", ok[MPBytes], bites.MaxBody(1<<20)) },
			"POST /x: MPBytes.Body.Value.Cover is a []uint8; a file field is a FormFile, an Opt of one, or a slice of one (bites has no multipart.FileHeader; FormFile.Open returns the reader)", false},
		{"a multipart.FileHeader as a file field", func(a *bites.App) { here(); a.Post("/x", ok[MPHeader], bites.MaxBody(1<<20)) },
			"POST /x: MPHeader.Body.Value.Cover is a *multipart.FileHeader; a file field is a FormFile, an Opt of one, or a slice of one (bites has no multipart.FileHeader; FormFile.Open returns the reader)", false},
		{"an io.Reader as a file field", func(a *bites.App) { here(); a.Post("/x", ok[MPReader], bites.MaxBody(1<<20)) },
			"POST /x: MPReader.Body.Value.Cover is a io.Reader; a file field is a FormFile, an Opt of one, or a slice of one (bites has no multipart.FileHeader; FormFile.Open returns the reader)", false},
		{"an Accept entry that is not a media type", func(a *bites.App) { here(); a.Post("/x", ok[MPAccept1], bites.MaxBody(1<<20)) },
			`POST /x: MPAccept1.Body.Value.Cover: badAccept1.Accept returns "image"; an entry is a media type such as image/png or image/*`, false},
		{"an Accept with no entry", func(a *bites.App) { here(); a.Post("/x", ok[MPAccept2], bites.MaxBody(1<<20)) },
			"POST /x: MPAccept2.Body.Value.Cover: badAccept2.Accept returns an empty list; an entry is a media type such as image/png or image/*", false},
		{"an Accept of */*", func(a *bites.App) { here(); a.Post("/x", ok[MPAccept3], bites.MaxBody(1<<20)) },
			`POST /x: MPAccept3.Body.Value.Cover: badAccept3.Accept returns "*/*"; an entry is a media type such as image/png or image/*`, false},
		{"a MaxSize of zero", func(a *bites.App) { here(); a.Post("/x", ok[MPSize0], bites.MaxBody(1<<20)) },
			"POST /x: MPSize0.Body.Value.Cover: badSize0.MaxSize returns 0; it is the most bytes of the file, and at most MaxBody", false},
		{"a MaxSize over MaxBody", func(a *bites.App) { here(); a.Post("/x", ok[MPSizeBig], bites.MaxBody(1<<20)) },
			"POST /x: MPSizeBig.Body.Value.Cover: badSizeBig.MaxSize returns 2097152; it is the most bytes of the file, and at most MaxBody", false},
		{"a MaxSize that is not func() int64", func(a *bites.App) { here(); a.Post("/x", ok[MPSizeType], bites.MaxBody(1<<20)) },
			"POST /x: MPSizeType.Body.Value.Cover: badSizeType.MaxSize is func() int; it must be func() int64, the most bytes of the file", false},
		{"a file type with fields besides the FormFile", func(a *bites.App) { here(); a.Post("/x", ok[MPExtra], bites.MaxBody(1<<20)) },
			"POST /x: MPExtra.Body.Value.Cover is a extraFile with fields besides the embedded FormFile; a file type is a FormFile or a struct whose only field is an embedded FormFile", false},
		{"a Multipart with more than 8 lists", func(a *bites.App) { here(); a.Post("/x", ok[MPNineLists], bites.MaxBody(1<<20)) },
			"POST /x: MPNineLists.Body.Value.I has more than 8 lists", false},
		{"a Multipart and an Upload in one Input", func(a *bites.App) { here(); a.Post("/x", ok[MPAndUpload], bites.MaxBody(1<<20)) },
			"POST /x: MPAndUpload holds a Multipart and an Upload; a request body is read one way", false},
		{"an Upload Body and a Multipart in one Input", func(a *bites.App) { here(); a.Post("/x", ok[UploadAndMP], bites.MaxBody(1<<20)) },
			"POST /x: UploadAndMP holds a Multipart and an Upload; a request body is read one way", false},
		{"a Multipart as a field of a JSON body", func(a *bites.App) { here(); a.Post("/x", ok[MPInJSON], bites.MaxBody(1<<20)) },
			"POST /x: MPInJSON.Body holds a Multipart; a Multipart is the whole Body, read as a form, not a field of a JSON body", false},
		{"a list of Multipart as the Body", func(a *bites.App) { here(); a.Post("/x", ok[MPInSlice], bites.MaxBody(1<<20)) },
			"POST /x: MPInSlice.Body holds a Multipart; a Multipart is the whole Body, read as a form, not a field of a JSON body", false},
		{"a negative UploadMemory in WithLimits is refused naming the field", func(*bites.App) { here(); bites.New(bites.WithLimits(bites.Limits{UploadMemory: -1})) },
			"WithLimits: Limits.UploadMemory is -1, which is not a limit: a limit cannot be switched off, so give a positive number of bytes, or zero for the default of 32 MiB", false},
		{"a negative MaxFieldBytes in WithLimits is refused naming the field", func(*bites.App) { here(); bites.New(bites.WithLimits(bites.Limits{MaxFieldBytes: -1})) },
			"WithLimits: Limits.MaxFieldBytes is -1, which is not a limit: a limit cannot be switched off, so give a positive number of bytes, or zero for the default of 64 KiB", false},
		{"a negative MaxFormBytes in WithLimits is refused naming the field", func(*bites.App) { here(); bites.New(bites.WithLimits(bites.Limits{MaxFormBytes: -1})) },
			"WithLimits: Limits.MaxFormBytes is -1, which is not a limit: a limit cannot be switched off, so give a positive number of bytes, or zero for the default of 1 MiB", false},
		{"an UploadDir that does not exist", func(*bites.App) { here(); bites.New(bites.WithLimits(bites.Limits{UploadDir: "/nonexistent/bites-upload-dir"})) },
			`WithLimits: Limits.UploadDir is "/nonexistent/bites-upload-dir", which cannot hold an upload: it does not exist; it is where a file too large for memory waits while a handler runs, so give a directory that exists and the process can write, or leave it empty for os.TempDir()`, false},
		{"MaxInFlight of zero", func(a *bites.App) { here(); a.Post("/x", ok[UploadIn], bites.MaxBody(1<<20), bites.MaxInFlight(0)) },
			"POST /x: MaxInFlight(0) is not a limit: it must be a positive number of uploads", false},
		{"a group MaxInFlight below zero", func(a *bites.App) { here(); a.Group("/g", bites.MaxInFlight(-1)) },
			`group "/g": MaxInFlight(-1) is not a limit: it must be a positive number of uploads`, false},
		{"MaxParts of zero", func(a *bites.App) { here(); a.Post("/x", ok[UploadIn], bites.MaxBody(1<<20), bites.MaxParts(0)) },
			"POST /x: MaxParts(0) is not a limit: it must be a positive number of parts", false},
		{"a pointer to a File as the output", func(a *bites.App) { here(); a.Get("/x", filePtrOut) },
			"GET /x: the handler returns *File; a File is returned by value, because the answer is built from its fields once", false},
		{"a Page as the Body of a status wrapper", func(a *bites.App) { here(); a.Get("/x", okPageOut) },
			"GET /x: Page is the Body of OK; a Page is the whole return type, not the body of a wrapper (its status, headers and cookies are its own fields)", false},
		{"a File as the Body of a status wrapper", func(a *bites.App) { here(); a.Get("/x", okFileOut) },
			"GET /x: File is the Body of OK; a File is the whole return type, not the body of a wrapper (its status and headers are its own fields)", false},
		{"ServeTLS with a nil config", func(a *bites.App) { here(); a.ServeTLS(nil, nil) },
			"ServeTLS: the TLS config is nil; give a *tls.Config with a certificate", false},
		{"ListenTLS with a nil config", func(a *bites.App) { here(); a.ListenTLS("127.0.0.1:0", nil) },
			"ListenTLS: the TLS config is nil; give a *tls.Config with a certificate", false},
		{"ListenTLS with a config that has no certificate", func(a *bites.App) { here(); a.ListenTLS("127.0.0.1:0", &tls.Config{}) },
			"ListenTLS: the TLS config has no certificate: set Certificates, GetCertificate or GetConfigForClient", false},
		{"ServeTLS with a config that offers h2", func(a *bites.App) { here(); a.ServeTLS(nil, tlsWith("h2", "http/1.1")) },
			`ServeTLS: the TLS config offers "h2" in NextProtos, but bites speaks HTTP/1.1 only until its HTTP/2 engine exists (ADR 0017); a client that negotiated h2 would be sent HTTP/1.1 it cannot read. Remove "h2", or leave NextProtos empty`, false},
		{"ListenTLS with a config that offers another protocol", func(a *bites.App) { here(); a.ListenTLS("127.0.0.1:0", tlsWith("http/1.1", "spdy/3")) },
			`ListenTLS: the TLS config offers "spdy/3" in NextProtos, but bites negotiates only "http/1.1", and "acme-tls/1" for a certificate challenge (ADR 0017); remove it, or leave NextProtos empty`, false},
		{"ServeTLS with acme-tls/1 and only static certificates", func(a *bites.App) { here(); a.ServeTLS(nil, tlsWith("http/1.1", "acme-tls/1")) },
			`ServeTLS: the TLS config offers "acme-tls/1" in NextProtos but has only static Certificates, which cannot answer a tls-alpn-01 challenge: set GetCertificate to the ACME client's (autocert's Manager.GetCertificate), or remove "acme-tls/1" (ADR 0017)`, false},
		{"ServeTLS that verifies client certificates with no ClientCAs", func(a *bites.App) { here(); a.ServeTLS(nil, tlsVerifying(tls.RequireAndVerifyClientCert)) },
			"ServeTLS: the TLS config verifies client certificates (ClientAuth RequireAndVerifyClientCert) but has no ClientCAs, so crypto/tls would accept any certificate the system's roots trust, which is any public CA's: set ClientCAs to the CAs that issue your clients' certificates (ADR 0066)", false},
		{"ListenTLS that verifies a client certificate if given with no ClientCAs", func(a *bites.App) { here(); a.ListenTLS("127.0.0.1:0", tlsVerifying(tls.VerifyClientCertIfGiven)) },
			"ListenTLS: the TLS config verifies client certificates (ClientAuth VerifyClientCertIfGiven) but has no ClientCAs, so crypto/tls would accept any certificate the system's roots trust, which is any public CA's: set ClientCAs to the CAs that issue your clients' certificates (ADR 0066)", false},
		{"ListenUnix with no permission bits", func(a *bites.App) { here(); a.ListenUnix("/nonexistent/x.sock", 0) },
			"ListenUnix: the permission ---------- is not a set of permission bits: give the bits of the socket file, such as 0o660 for the owner and its group, which is who may connect", false},
		{"ListenUnix with bits beyond the permission bits", func(a *bites.App) { here(); a.ListenUnix("/nonexistent/x.sock", os.ModeSocket|0o660) },
			"ListenUnix: the permission Srw-rw---- is not a set of permission bits: give the bits of the socket file, such as 0o660 for the owner and its group, which is who may connect", false},
		{"Static with both Root and FS", func(a *bites.App) { here(); a.Static("/s", bites.Static{Root: &os.Root{}, FS: fstest.MapFS{}}) },
			"Static /s: give exactly one of Root and FS", false},
		{"Static with neither Root nor FS", func(a *bites.App) { here(); a.Static("/s", bites.Static{}) },
			"Static /s: give exactly one of Root and FS", false},
		{"Static with a prefix that ends in a slash", func(a *bites.App) { here(); a.Static("/s/", bites.Static{FS: fstest.MapFS{}}) },
			`Static /s/: the prefix is "/" or starts with a slash, does not end with one, and has no wildcard`, false},
		{"Static with a wildcard in its prefix", func(a *bites.App) { here(); a.Static("/{tenant}", bites.Static{FS: fstest.MapFS{}}) },
			`Static /{tenant}: the prefix is "/" or starts with a slash, does not end with one, and has no wildcard`, false},
		{"a group Static with a prefix that has no slash", func(a *bites.App) { g := a.Group("/g"); here(); g.Static("s", bites.Static{FS: fstest.MapFS{}}) },
			`Static /gs: the prefix is "/" or starts with a slash, does not end with one, and has no wildcard`, false},
		{"Static with an Index that has a slash", func(a *bites.App) {
			a.Static("/s", at(bites.Static{FS: fstest.MapFS{}, Index: "pages/index.html"}))
		},
			`Static /s: Index "pages/index.html" is not a plain file name: give a name with no slash, such as index.html`, false},
		{"Static with a Fallback that does not exist", func(a *bites.App) { here(); a.Static("/", bites.Static{FS: fstest.MapFS{}, Fallback: "index.html"}) },
			`Static /: Fallback "index.html" is not a regular file in the directory: a fallback that does not exist would answer every navigation with a 404`, false},
		{"Static with a Fallback that leaves the directory", func(a *bites.App) {
			a.Static("/", at(bites.Static{FS: fstest.MapFS{}, Fallback: "../index.html"}))
		},
			`Static /: Fallback "../index.html" is not a file name inside the directory`, false},
		{"Static with a negative MaxAge", func(a *bites.App) { here(); a.Static("/s", bites.Static{FS: fstest.MapFS{}, MaxAge: -time.Second}) },
			"Static /s: MaxAge(-1s) is not a cache lifetime: give whole seconds, or zero for no-cache", false},
		{"Static with a MaxAge under a second", func(a *bites.App) {
			a.Static("/s", at(bites.Static{FS: fstest.MapFS{}, MaxAge: 500 * time.Millisecond}))
		},
			"Static /s: MaxAge(500ms) is not a cache lifetime: give whole seconds, or zero for no-cache", false},
		{"Static with a Summary", func(a *bites.App) {
			a.Static("/s", at(bites.Static{FS: fstest.MapFS{}}), bites.Summary("files"))
		},
			"Static /s: Summary, Description, OperationID and Returns describe an operation of the API document, which leaves static files out; Tag is accepted and ignored", false},
		{"Static whose route conflicts with another", func(a *bites.App) {
			func() { hereFirst(); a.Static("/s", bites.Static{FS: fstest.MapFS{}}) }()
			func() { here(); a.Static("/s", bites.Static{FS: fstest.MapFS{}}) }()
		}, `pattern "GET /s/{path...}" (registered at refusal_test.go:%[1]d) conflicts with pattern "GET /s/{path...}" (registered at refusal_test.go:%[2]d): they match the same requests`, true},
		{"a negative MaxHead in WithLimits is refused naming the field", func(*bites.App) { here(); bites.New(bites.WithLimits(bites.Limits{MaxHead: -1})) },
			"WithLimits: Limits.MaxHead is -1, which is not a limit: a limit cannot be switched off, so give a positive number of bytes, or zero for the default of 8 KiB", false},
		{"a negative MaxFields in WithLimits is refused naming the field", func(*bites.App) { here(); bites.New(bites.WithLimits(bites.Limits{MaxFields: -1})) },
			"WithLimits: Limits.MaxFields is -1, which is not a limit: a limit cannot be switched off, so give a positive number of fields, or zero for the default of 100", false},
		{"a negative MaxBody in WithLimits is refused naming the field", func(*bites.App) { here(); bites.New(bites.WithLimits(bites.Limits{MaxBody: -1})) },
			"WithLimits: Limits.MaxBody is -1, which is not a limit: a limit cannot be switched off, so give a positive number of bytes, or zero for the default of 1 MiB", false},
		{"a negative HeadTimeout in WithLimits is refused naming the field", func(*bites.App) { here(); bites.New(bites.WithLimits(bites.Limits{HeadTimeout: -time.Second})) },
			"WithLimits: Limits.HeadTimeout is -1s, which is not a limit: a limit cannot be switched off, so give a positive duration, or zero for the default of 10 s", false},
		{"a negative BodyTimeout in WithLimits is refused naming the field", func(*bites.App) { here(); bites.New(bites.WithLimits(bites.Limits{BodyTimeout: -time.Second})) },
			"WithLimits: Limits.BodyTimeout is -1s, which is not a limit: a limit cannot be switched off, so give a positive duration, or zero for the default of 30 s", false},
		{"a negative IdleTimeout in WithLimits is refused naming the field", func(*bites.App) { here(); bites.New(bites.WithLimits(bites.Limits{IdleTimeout: -time.Second})) },
			"WithLimits: Limits.IdleTimeout is -1s, which is not a limit: a limit cannot be switched off, so give a positive duration, or zero for the default of 60 s", false},
		{"a negative WriteTimeout in WithLimits is refused naming the field", func(*bites.App) { here(); bites.New(bites.WithLimits(bites.Limits{WriteTimeout: -time.Second})) },
			"WithLimits: Limits.WriteTimeout is -1s, which is not a limit: a limit cannot be switched off, so give a positive duration, or zero for the default of 30 s", false},
		{"a negative MaxConns in WithLimits is refused naming the field", func(*bites.App) { here(); bites.New(bites.WithLimits(bites.Limits{MaxConns: -1})) },
			"WithLimits: Limits.MaxConns is -1, which is not a limit: a limit cannot be switched off, so give a positive number of connections, or zero for the default of 90% of the file descriptor limit", false},
		{"a negative MaxDepth in WithLimits is refused naming the field", func(*bites.App) { here(); bites.New(bites.WithLimits(bites.Limits{MaxDepth: -1})) },
			"WithLimits: Limits.MaxDepth is -1, which is not a limit: a limit cannot be switched off, so give a positive number of levels, or zero for the default of 64", false},
		{"a negative HandshakeTimeout in WithLimits is refused naming the field", func(*bites.App) { here(); bites.New(bites.WithLimits(bites.Limits{HandshakeTimeout: -1})) },
			"WithLimits: Limits.HandshakeTimeout is -1ns, which is not a limit: a limit cannot be switched off, so give a positive duration, or zero for the default of 10 s", false},
		{"WithTextFormat with no format", func(*bites.App) { here(); bites.New(bites.WithTextFormat[foreignText]("")) },
			`WithTextFormat[foreignText]: the format is empty, which names no format: give the OpenAPI format, such as "uuid"`, false},
		{"WithTextFormat for a type with no UnmarshalText", func(*bites.App) { here(); bites.New(bites.WithTextFormat[writeOnlyText]("uuid")) },
			"WithTextFormat[writeOnlyText]: *writeOnlyText has no UnmarshalText method, so a request could not be decoded into it", false},
		{"WithTextFormat for a type with no MarshalText", func(*bites.App) { here(); bites.New(bites.WithTextFormat[readOnlyText]("uuid")) },
			"WithTextFormat[readOnlyText]: readOnlyText has no MarshalText method, so a response would write it as the encoding of its fields and not as a string with this format", false},
		{"WithTextFormat for a pointer type", func(*bites.App) { here(); bites.New(bites.WithTextFormat[*foreignText]("uuid")) },
			"WithTextFormat[*bites_test.foreignText]: it is a ptr, and the format belongs to the type a value has: register the concrete, non-pointer type", false},
		{"WithTextFormat for a type the core already knows", func(*bites.App) { here(); bites.New(bites.WithTextFormat[time.Time]("date-time")) },
			"WithTextFormat[Time]: the core already reads and describes it with a format of its own", false},
		{"WithTextFormat twice with two formats", func(*bites.App) {
			here(); bites.New(bites.WithTextFormat[foreignText]("uuid"), bites.WithTextFormat[foreignText]("ulid"))
		},
			`WithTextFormat[foreignText]: it is already registered with the format "uuid"`, false},
		{"WithJSONSchema for a type with no JSON method", func(*bites.App) { here(); bites.New(bites.WithJSONSchema[foreignText](bites.JSONSchema{Type: "string"})) },
			"WithJSONSchema[foreignText]: foreignText has no JSON method, so it is described by its fields and a registration would hide them: if it has a text form, use WithTextFormat", false},
		{"WithJSONSchema with a type that is not a JSON type", func(*bites.App) { here(); bites.New(bites.WithJSONSchema[stamp](bites.JSONSchema{Type: "timestamp"})) },
			`WithJSONSchema[stamp]: the type is "timestamp", which is not a JSON type: give one of string, number, integer, boolean, object, array, any`, false},
		{"WithJSONSchema with no type", func(*bites.App) { here(); bites.New(bites.WithJSONSchema[stamp](bites.JSONSchema{Format: "date-time"})) },
			`WithJSONSchema[stamp]: the type is "", which is not a JSON type: give one of string, number, integer, boolean, object, array, any`, false},
		{"WithJSONSchema with a format on an object", func(*bites.App) { here(); bites.New(bites.WithJSONSchema[stamp](bites.JSONSchema{Type: "object", Format: "date-time"})) },
			`WithJSONSchema[stamp]: a format belongs to a string, number or integer, and the type is "object"`, false},
		{"WithJSONSchema nullable and free-form", func(*bites.App) { here(); bites.New(bites.WithJSONSchema[stamp](bites.JSONSchema{Type: "any", Nullable: true})) },
			"WithJSONSchema[stamp]: a free-form value already admits null: drop Nullable", false},
		{"WithJSONSchema for a pointer type", func(*bites.App) { here(); bites.New(bites.WithJSONSchema[*stamp](bites.JSONSchema{Type: "string"})) },
			"WithJSONSchema[*bites_test.stamp]: it is a ptr, and the schema belongs to the type a value has: register the concrete, non-pointer type", false},
		{"WithJSONSchema for a type the core already knows", func(*bites.App) { here(); bites.New(bites.WithJSONSchema[time.Time](bites.JSONSchema{Type: "string"})) },
			"WithJSONSchema[Time]: the core already reads and describes it with a format of its own", false},
		{"WithJSONSchema for json.RawMessage", func(*bites.App) { here(); bites.New(bites.WithJSONSchema[json.RawMessage](bites.JSONSchema{Type: "any"})) },
			"WithJSONSchema[Value]: the core already describes it as free-form JSON, which needs no registration", false},
		{"WithJSONSchema twice with two schemas", func(*bites.App) {
			here(); bites.New(bites.WithJSONSchema[stamp](bites.JSONSchema{Type: "string"}), bites.WithJSONSchema[stamp](bites.JSONSchema{Type: "object"}))
		},
			"WithJSONSchema[stamp]: it is already registered with a different schema", false},
		{"WithJSONSchema for a type registered with WithTextFormat", func(*bites.App) {
			here(); bites.New(bites.WithTextFormat[stampText]("date-time"), bites.WithJSONSchema[stampText](bites.JSONSchema{Type: "string"}))
		},
			"WithJSONSchema[stampText]: it is already registered with WithTextFormat, and a type is described one way", false},
		{"WithTextFormat for a type registered with WithJSONSchema", func(*bites.App) {
			here(); bites.New(bites.WithJSONSchema[stampText](bites.JSONSchema{Type: "string"}), bites.WithTextFormat[stampText]("date-time"))
		},
			"WithTextFormat[stampText]: it is already registered with WithJSONSchema, and a type is described one way", false},
		{"a WebSocket route with a nil handler", func(a *bites.App) { here(); a.WebSocket[Empty, wsOut, wsMsg]("/x", nil) },
			"WebSocket /x: the handler is nil", false},
		{"a WebSocket route with a path param and no Path section", func(a *bites.App) { here(); a.WebSocket("/x/{id}", wsOK) },
			"WebSocket /x/{id}: the pattern has {id}, but Empty has no Path section", false},
		{"a union message whose field is not an Opt of a struct", func(a *bites.App) { here(); a.WebSocket("/x", wsHandler[Empty, wsOut, plainVariantFrame]) },
			"WebSocket /x: the Inbox's message type cannot be read from JSON: plainVariantFrame.Say: a field of the union plainVariantFrame is string, but each is an Opt of a struct, the variant whose tag is \"say\"", false},
		{"a union message whose variant has the discriminator as a member", func(a *bites.App) { here(); a.WebSocket("/x", wsHandler[Empty, clashingFrame, wsMsg]) },
			"WebSocket /x: the Socket's message type cannot be written as JSON: typedVariant.Type: the variant typedVariant has a member \"type\", which is the discriminator of clashingFrame: rename the member, or return another name from Discriminator", false},
		{"a union whose ServerOnly names a field that is not a variant", func(a *bites.App) { here(); a.WebSocket("/x", wsHandler[Empty, strayServerOnlyFrame, wsMsg]) },
			"WebSocket /x: the Socket's message type cannot be written as JSON: strayServerOnlyFrame: strayServerOnlyFrame.ServerOnly names Rfe, which is not a variant of strayServerOnlyFrame (its variants: Join, Ref): name a field that is an Opt of a variant", false},
		{"a union whose ServerOnly names every variant", func(a *bites.App) { here(); a.WebSocket("/x", wsHandler[Empty, allServerOnlyFrame, wsMsg]) },
			"WebSocket /x: the Socket's message type cannot be written as JSON: allServerOnlyFrame: allServerOnlyFrame.ServerOnly names every variant (Join, Ref), so no client could be sent or send any: leave at least one out", false},
		{"a union whose ServerOnly names a variant twice", func(a *bites.App) { here(); a.WebSocket("/x", wsHandler[Empty, twiceServerOnlyFrame, wsMsg]) },
			"WebSocket /x: the Socket's message type cannot be written as JSON: twiceServerOnlyFrame: twiceServerOnlyFrame.ServerOnly names Ref twice: name each variant once", false},
		{"a union with ServerOnly on the pointer receiver", func(a *bites.App) { here(); a.WebSocket("/x", wsHandler[Empty, ptrServerOnlyFrame, wsMsg]) },
			"WebSocket /x: the Socket's message type cannot be written as JSON: ptrServerOnlyFrame: ptrServerOnlyFrame declares ServerOnly on *ptrServerOnlyFrame, but the codec holds a ptrServerOnlyFrame: declare it on the value receiver, func (ptrServerOnlyFrame) ServerOnly() []string", false},
		{"a WebSocket route whose Input has a Body", func(a *bites.App) { here(); a.WebSocket("/x", wsHandler[BodyOnGet, wsOut, wsMsg]) },
			"WebSocket /x: BodyOnGet.Body is a Body section, but a GET route has no request body", false},
		{"a WebSocket route whose Input asks for a type no Provider covers", func(a *bites.App) { here(); a.WebSocket("/x", wsHandler[WithAuth, wsOut, wsMsg]) },
			"WebSocket /x: WithAuth.Auth is not a section (Path, Query, Header, Cookie or Body), and no Provider of string covers this route", false},
		{"a Socket that sends a status wrapper", func(a *bites.App) { here(); a.WebSocket("/x", wsHandler[Empty, bites.OK[wsOut], wsMsg]) },
			"WebSocket /x: the handler's Socket sends OK[github.com/nevindra/bites_test.wsOut], one of the framework's response types; a WebSocket message is a plain value, written as JSON (or []byte, written as a binary frame), and has no status or headers", false},
		{"a Socket that sends a channel", func(a *bites.App) { here(); a.WebSocket("/x", wsHandler[Empty, chan int, wsMsg]) },
			"WebSocket /x: the Socket's message type cannot be written as JSON: chan int: a field of kind chan cannot be encoded", false},
		{"an Inbox whose message is a number", func(a *bites.App) { here(); a.WebSocket("/x", wsHandler[Empty, wsOut, int]) },
			"WebSocket /x: the Inbox's message type is int; a message is a struct, slice or map (read as JSON), []byte or Raw[T]", false},
		{"an Inbox whose message is a Form", func(a *bites.App) { here(); a.WebSocket("/x", wsHandler[Empty, wsOut, bites.Form[wsMsg]]) },
			"WebSocket /x: the Inbox's message type holds Form[github.com/nevindra/bites_test.wsMsg], which is a kind of request body; a message is JSON, []byte or Raw[T]", false},
		{"an Inbox whose message holds an Upload", func(a *bites.App) { here(); a.WebSocket("/x", wsHandler[Empty, wsOut, UploadInJSON]) },
			"WebSocket /x: the Inbox's message type holds UploadInJSON, which is a kind of request body; a message is JSON, []byte or Raw[T]", false},
		{"an Inbox whose message has a channel field", func(a *bites.App) { here(); a.WebSocket("/x", wsHandler[Empty, wsOut, struct{ C chan int }]) },
			"WebSocket /x: the Inbox's message type cannot be read from JSON: struct { C chan int }.C: a field of kind chan cannot be decoded", false},
		{"AllowOrigins with *", func(a *bites.App) { here(); a.WebSocket("/x", wsOK, bites.AllowOrigins("*")) },
			`WebSocket /x: AllowOrigins("*") is refused: an Origin check that lets every site in is AllowAnyOrigin(), which says so in the code; use it if that is what you mean`, false},
		{"AllowOrigins with null", func(a *bites.App) { here(); a.WebSocket("/x", wsOK, bites.AllowOrigins("null")) },
			`WebSocket /x: AllowOrigins("null") is refused: a sandboxed iframe or a file:// page sends Origin: null, so every such page would pass; list the sites that may connect`, false},
		{"AllowOrigins with an entry that has no scheme", func(a *bites.App) { here(); a.WebSocket("/x", wsOK, bites.AllowOrigins("app.example.com")) },
			`WebSocket /x: AllowOrigins("app.example.com") has no scheme: an origin is scheme://host[:port], such as https://app.example.com`, false},
		{"AllowOrigins with a trailing slash", func(a *bites.App) { here(); a.WebSocket("/x", wsOK, bites.AllowOrigins("https://app.example.com/")) },
			`WebSocket /x: AllowOrigins("https://app.example.com/") has a path, a trailing slash, a userinfo or a space: an origin is scheme://host[:port] and nothing after it`, false},
		{"AllowOrigins with a path", func(a *bites.App) { here(); a.WebSocket("/x", wsOK, bites.AllowOrigins("https://a.io/chat")) },
			`WebSocket /x: AllowOrigins("https://a.io/chat") has a path, a trailing slash, a userinfo or a space: an origin is scheme://host[:port] and nothing after it`, false},
		{"AllowOrigins with a wildcard subdomain", func(a *bites.App) { here(); a.WebSocket("/x", wsOK, bites.AllowOrigins("https://*.example.com")) },
			`WebSocket /x: AllowOrigins("https://*.example.com") has a wildcard, which is not supported: list each origin in full`, false},
		{"AllowOrigins with no origin", func(a *bites.App) { here(); a.WebSocket("/x", wsOK, bites.AllowOrigins()) },
			"WebSocket /x: AllowOrigins is given no origin: list the sites that may connect, or use AllowAnyOrigin() to say any may", false},
		{"AllowAnyOrigin after AllowOrigins in one scope", func(a *bites.App) {
			a.WebSocket("/x", wsOK, at(bites.AllowOrigins("https://a.io")), bites.AllowAnyOrigin())
		},
			"WebSocket /x: AllowAnyOrigin() and AllowOrigins are both given in this scope: AllowAnyOrigin lets every site in, so the list would mean nothing; keep one", false},
		{"AllowOrigins after AllowAnyOrigin in one scope", func(a *bites.App) {
			a.WebSocket("/x", wsOK, bites.AllowAnyOrigin(), at(bites.AllowOrigins("https://a.io")))
		},
			"WebSocket /x: AllowOrigins and AllowAnyOrigin() are both given in this scope: AllowAnyOrigin lets every site in, so the list would mean nothing; keep one", false},
		{"AllowOrigins with * on a group", func(a *bites.App) { here(); a.Group("/g", bites.AllowOrigins("*")) },
			`group "/g": AllowOrigins("*") is refused: an Origin check that lets every site in is AllowAnyOrigin(), which says so in the code; use it if that is what you mean`, false},
		{"WithAllowOrigins with *", func(*bites.App) { here(); bites.WithAllowOrigins("*") },
			`WithAllowOrigins("*") is refused: an Origin check that lets every site in is AllowAnyOrigin(), which says so in the code; use it if that is what you mean`, false},
		{"WithAllowOrigins with an entry that has no scheme", func(*bites.App) { here(); bites.WithAllowOrigins("https://a.io", "b.example") },
			`WithAllowOrigins("b.example") has no scheme: an origin is scheme://host[:port], such as https://app.example.com`, false},
		{"WithAllowOrigins with no origin", func(*bites.App) { here(); bites.WithAllowOrigins() },
			"WithAllowOrigins is given no origin: list the sites that may connect, or use WithAllowAnyOrigin() to say any may", false},
		{"WithAllowAnyOrigin after WithAllowOrigins", func(*bites.App) {
			bites.New(bites.WithAllowOrigins(at("https://a.io")), bites.WithAllowAnyOrigin())
		},
			"WithAllowAnyOrigin() and WithAllowOrigins are both given: AllowAnyOrigin lets every site in, so the list would mean nothing; keep one", false},
		{"WithAllowOrigins after WithAllowAnyOrigin", func(*bites.App) {
			bites.New(bites.WithAllowAnyOrigin(), bites.WithAllowOrigins(at("https://a.io")))
		},
			"WithAllowOrigins and WithAllowAnyOrigin() are both given: AllowAnyOrigin lets every site in, so the list would mean nothing; keep one", false},
		{"MaxMessage of zero", func(a *bites.App) { here(); a.WebSocket("/x", wsOK, bites.MaxMessage(0)) },
			"WebSocket /x: MaxMessage(0) is not a limit: it must be a positive number of bytes", false},
		{"a group MaxMessage below zero", func(a *bites.App) { here(); a.Group("/g", bites.MaxMessage(-5)) },
			`group "/g": MaxMessage(-5) is not a limit: it must be a positive number of bytes`, false},
		{"a MaxMessage above the reassembly budget", func(a *bites.App) { here(); a.WebSocket("/x", wsOK, bites.MaxMessage(128<<20)) },
			"WebSocket /x: MaxMessage is 134217728 bytes, above the app's reassembly budget of 67108864 bytes (Limits.ReassemblyBudget), so a message of that size could never be assembled: lower MaxMessage or raise the budget", false},
		{"a negative MaxMessage in WithLimits is refused naming the field", func(*bites.App) { here(); bites.New(bites.WithLimits(bites.Limits{MaxMessage: -1})) },
			"WithLimits: Limits.MaxMessage is -1, which is not a limit: a limit cannot be switched off, so give a positive number of bytes, or zero for the default of 64 KiB", false},
		{"a negative MessageTimeout in WithLimits is refused naming the field", func(*bites.App) { here(); bites.New(bites.WithLimits(bites.Limits{MessageTimeout: -1})) },
			"WithLimits: Limits.MessageTimeout is -1ns, which is not a limit: a limit cannot be switched off, so give a positive duration, or zero for the default of 30 s", false},
		{"a negative ReassemblyBudget in WithLimits is refused naming the field", func(*bites.App) { here(); bites.New(bites.WithLimits(bites.Limits{ReassemblyBudget: -1})) },
			"WithLimits: Limits.ReassemblyBudget is -1, which is not a limit: a limit cannot be switched off, so give a positive number of bytes, or zero for the default of 64 MiB", false},
		{"a negative SendQueue in WithLimits is refused naming the field", func(*bites.App) { here(); bites.New(bites.WithLimits(bites.Limits{SendQueue: -1})) },
			"WithLimits: Limits.SendQueue is -1, which is not a limit: a limit cannot be switched off, so give a positive number of bytes, or zero for the default of 1 MiB", false},
		{"a negative SendBudget in WithLimits is refused naming the field", func(*bites.App) { here(); bites.New(bites.WithLimits(bites.Limits{SendBudget: -1})) },
			"WithLimits: Limits.SendBudget is -1, which is not a limit: a limit cannot be switched off, so give a positive number of bytes, or zero for the default of 64 MiB", false},
		{"a negative HeartbeatInterval in WithLimits is refused naming the field", func(*bites.App) { here(); bites.New(bites.WithLimits(bites.Limits{HeartbeatInterval: -1})) },
			"WithLimits: Limits.HeartbeatInterval is -1ns, which is not a limit: a limit cannot be switched off, so give a positive duration, or zero for the default of 30 s", false},
		{"a negative MissedPongs in WithLimits is refused naming the field", func(*bites.App) { here(); bites.New(bites.WithLimits(bites.Limits{MissedPongs: -1})) },
			"WithLimits: Limits.MissedPongs is -1, which is not a limit: a limit cannot be switched off, so give a positive number of pongs, or zero for the default of 2", false},
		{"a MinSize below 1 KiB in WithGzip is refused naming the floor", func(*bites.App) { here(); bites.New(bites.WithGzip(bites.Gzip{MinSize: 512})) },
			"WithGzip: Gzip.MinSize is 512, which is below the floor of 1024 bytes: under about 1.2 KiB an answer fits one TCP segment, so compressing it saves bytes and no packets; give 1024 or more, or zero for the default of 1 KiB", false},
		{"a negative MinSize in WithGzip is refused naming the floor", func(*bites.App) { here(); bites.New(bites.WithGzip(bites.Gzip{MinSize: -1})) },
			"WithGzip: Gzip.MinSize is -1, which is below the floor of 1024 bytes: under about 1.2 KiB an answer fits one TCP segment, so compressing it saves bytes and no packets; give 1024 or more, or zero for the default of 1 KiB", false},
		{"WithGzip given twice is refused naming both places", func(*bites.App) {
			bites.New(bites.WithGzip(atBoth(bites.Gzip{})), bites.WithGzip(bites.Gzip{MinSize: 2048}))
		},
			"WithGzip: given twice (first at refusal_test.go:%d, again at refusal_test.go:%d): give one Gzip, because two could say different things", true},
		{"WithCompression after WithGzip is refused naming both places", func(*bites.App) {
			bites.New(bites.WithGzip(atBoth(bites.Gzip{})), bites.WithCompression(bites.Compression{}))
		},
			"WithCompression: given after WithGzip (first at refusal_test.go:%d, again at refusal_test.go:%d): give one Compression that lists every Encoder, in the order the server prefers", true},
		{"WithCompression given twice is refused naming both places", func(*bites.App) {
			bites.New(bites.WithCompression(atBoth(bites.Compression{})), bites.WithCompression(bites.Compression{}))
		},
			"WithCompression: given twice (first at refusal_test.go:%d, again at refusal_test.go:%d): give one Compression that lists every Encoder, in the order the server prefers", true},
		{"a MinSize below 1 KiB in WithCompression is refused naming the floor", func(*bites.App) { here(); bites.New(bites.WithCompression(bites.Compression{MinSize: 100})) },
			"WithCompression: Compression.MinSize is 100, which is below the floor of 1024 bytes: under about 1.2 KiB an answer fits one TCP segment, so compressing it saves bytes and no packets; give 1024 or more, or zero for the default of 1 KiB", false},
		{"a nil Encoder is refused naming its place", func(*bites.App) {
			bites.New(bites.WithCompression(at(bites.Compression{Encoders: []bites.Encoder{bites.GzipEncoder(), nil}})))
		},
			"WithCompression: Encoders[1] is nil", false},
		{"an Encoder whose coding is not a lowercase token is refused", func(*bites.App) {
			bites.New(bites.WithCompression(at(bites.Compression{Encoders: []bites.Encoder{coding("Br")}})))
		},
			`WithCompression: Encoders[0] has the coding "Br", which is not a lowercase content coding token such as "br", "zstd" or "gzip"`, false},
		{"an Encoder that calls itself identity is refused", func(*bites.App) {
			bites.New(bites.WithCompression(at(bites.Compression{Encoders: []bites.Encoder{coding("identity")}})))
		},
			`WithCompression: Encoders[0] has the coding "identity", which is not a lowercase content coding token such as "br", "zstd" or "gzip"`, false},
		{"an Encoder that calls itself * is refused", func(*bites.App) {
			bites.New(bites.WithCompression(at(bites.Compression{Encoders: []bites.Encoder{coding("*")}})))
		},
			`WithCompression: Encoders[0] has the coding "*", which is not a lowercase content coding token such as "br", "zstd" or "gzip"`, false},
		{"a coding listed twice is refused naming both places in the list", func(*bites.App) {
			bites.New(bites.WithCompression(at(bites.Compression{Encoders: []bites.Encoder{coding("br"), bites.GzipEncoder(), coding("gzip")}})))
		},
			`WithCompression: Encoders[2] has the coding "gzip", as Encoders[1] has: list each coding once, in the order the server prefers`, false},
		{"WithErrorMapper with a nil function is refused", func(*bites.App) { here(); bites.New(bites.WithErrorMapper(nil)) },
			"WithErrorMapper: the function is nil: give the function that turns an error into a *StatusError, or leave the option out", false},
		{"WithObserver given twice is refused naming both places", func(*bites.App) {
			bites.New(bites.WithObserver(atBoth(bites.AccessLog())), bites.WithObserver(bites.AccessLog()))
		},
			"WithObserver: given twice (first at refusal_test.go:%d, again at refusal_test.go:%d): give every Observer in one WithObserver, in the order they should run", true},
		{"a nil Observer is refused naming its place in the list", func(*bites.App) {
			bites.New(bites.WithObserver(at(bites.AccessLog()), nil))
		},
			"WithObserver: Observer 2 is nil", false},
		{"WithHealth with neither path is refused", func(*bites.App) { here(); bites.New(bites.WithHealth(bites.Health{})) },
			`WithHealth: Health has neither a Live nor a Ready path: give at least one, such as Health{Live: "/livez", Ready: "/readyz"}`, false},
		{"WithHealth with a Live path that does not begin with a slash is refused", func(*bites.App) { here(); bites.New(bites.WithHealth(bites.Health{Live: "livez"})) },
			`WithHealth: Health.Live is "livez", which is not a path: it must start with a slash and have no wildcard, query or fragment`, false},
		{"WithHealth with a Ready path that has a wildcard is refused", func(*bites.App) { here(); bites.New(bites.WithHealth(bites.Health{Ready: "/ready/{x}"})) },
			`WithHealth: Health.Ready is "/ready/{x}", which is not a path: it must start with a slash and have no wildcard, query or fragment`, false},
		{"WithHealth with the same path for Live and Ready is refused", func(*bites.App) {
			bites.New(bites.WithHealth(at(bites.Health{Live: "/health", Ready: "/health"})))
		},
			`WithHealth: Health.Live and Health.Ready are both "/health": a probe could not tell liveness from readiness; give each its own path`, false},
		{"WithHealth given twice is refused naming both places", func(*bites.App) {
			bites.New(bites.WithHealth(atBoth(bites.Health{Live: "/livez"})), bites.WithHealth(bites.Health{Ready: "/readyz"}))
		},
			"WithHealth: given twice (first at refusal_test.go:%d, again at refusal_test.go:%d): give one Health", true},
		{"a route at the Live path of WithHealth is refused naming both", func(*bites.App) {
			a := bites.New(bites.WithHealth(atFirst(bites.Health{Live: "/livez", Ready: "/readyz"})))
			a.Get(at("/livez"), ok[Empty])
		},
			"GET /livez: /livez is the Live path of WithHealth (given at refusal_test.go:%[2]d), which is answered before any route, so this route would never run: give the route or Health.Live another path\n\tregistered at refusal_test.go:%[1]d", true},
		{"a WebSocket route at the Ready path of WithHealth is refused naming both", func(*bites.App) {
			a := bites.New(bites.WithHealth(atFirst(bites.Health{Live: "/livez", Ready: "/readyz"})))
			a.WebSocket(at("/readyz"), wsOK)
		},
			"WebSocket /readyz: /readyz is the Ready path of WithHealth (given at refusal_test.go:%[2]d), which is answered before any route, so this route would never run: give the route or Health.Ready another path\n\tregistered at refusal_test.go:%[1]d", true},
		{"a group route at the Live path of WithHealth is refused naming both", func(*bites.App) {
			a := bites.New(bites.WithHealth(atFirst(bites.Health{Live: "/ops/livez"})))
			a.Group("/ops").Get(at("/livez"), ok[Empty])
		},
			"GET /ops/livez: /ops/livez is the Live path of WithHealth (given at refusal_test.go:%[2]d), which is answered before any route, so this route would never run: give the route or Health.Live another path\n\tregistered at refusal_test.go:%[1]d", true},
		{"the API document at the Ready path of WithHealth is refused naming both", func(*bites.App) {
			a := bites.New(bites.WithHealth(atFirst(bites.Health{Ready: "/readyz"})))
			a.OpenAPI(at("/readyz"), bites.Info{Title: "t", Version: "1"})
		},
			"OpenAPI /readyz: /readyz is the Ready path of WithHealth (given at refusal_test.go:%[2]d), which is answered before any route, so this route would never run: give the route or Health.Ready another path\n\tregistered at refusal_test.go:%[1]d", true},
		{"a negative WithShutdownDelay is refused", func(*bites.App) { here(); bites.New(bites.WithShutdownDelay(-time.Second)) },
			"WithShutdownDelay: the delay is -1s, which is not a delay: give zero for none, or a positive duration such as 5*time.Second", false},
		{"WithShutdownDelay given twice is refused naming both places", func(*bites.App) {
			bites.New(bites.WithShutdownDelay(atBoth(time.Second)), bites.WithShutdownDelay(2*time.Second))
		},
			"WithShutdownDelay: given twice (first at refusal_test.go:%d, again at refusal_test.go:%d): give one delay", true},
		{"WithTraceContext given twice is refused naming both places", func(*bites.App) {
			bites.New(bites.WithTraceContext(atBoth(bites.TraceContext{})), bites.WithTraceContext(bites.TraceContext{Begin: true}))
		},
			"WithTraceContext: given twice (first at refusal_test.go:%d, again at refusal_test.go:%d): give one TraceContext", true},
		{"NewMetrics with two options values is refused at construction", func(*bites.App) {
			bites.NewMetrics(at(bites.MetricsOptions{}), bites.MetricsOptions{})
		},
			"NewMetrics: 2 options values were given; give one MetricsOptions", false},
		{"NewMetrics with an empty bucket list is refused at construction", func(*bites.App) {
			bites.NewMetrics(at(bites.MetricsOptions{Buckets: []time.Duration{}}))
		},
			"NewMetrics: Buckets is empty: give at least one upper bound, or nil for the default", false},
		{"NewMetrics with a bucket that is not positive is refused at construction", func(*bites.App) {
			bites.NewMetrics(at(bites.MetricsOptions{Buckets: []time.Duration{0}}))
		},
			"NewMetrics: Buckets[0] is 0s, which is not an upper bound: give positive durations in increasing order", false},
		{"NewMetrics with buckets out of order is refused at construction", func(*bites.App) {
			bites.NewMetrics(at(bites.MetricsOptions{Buckets: []time.Duration{time.Second, time.Second}}))
		},
			"NewMetrics: Buckets[1] is 1s, which is not above Buckets[0] (1s): give the bounds in increasing order, each once", false},
		{"NewMetrics with a negative MaxSeries is refused at construction", func(*bites.App) {
			bites.NewMetrics(at(bites.MetricsOptions{MaxSeries: -1}))
		},
			"NewMetrics: MaxSeries is -1, which is not a limit: give a positive number of series, or zero for the default of 10,000", false},
		{"NewTopic with an empty name is refused at construction", func(*bites.App) {
			bites.NewTopic[User]("", at(refusalBroker{}), bites.TopicOptions{})
		},
			`NewTopic: the name is empty: give the name every instance uses for this topic, such as "room.7"`, false},
		{"NewTopic with a nil broker is refused at construction", func(*bites.App) {
			here(); bites.NewTopic[User]("room.1", nil, bites.TopicOptions{})
		},
			`NewTopic "room.1": the broker is nil: give the Broker this topic publishes through, or use a zero bites.Topic[Out] for a topic of this process only`, false},
		{"NewTopic with a negative MaxPayload is refused at construction", func(*bites.App) {
			here(); bites.NewTopic[User]("room.1", refusalBroker{}, bites.TopicOptions{MaxPayload: -1})
		},
			`NewTopic "room.1": MaxPayload is -1, which is not a limit: give a positive number of bytes, or zero for 1 MiB`, false},
		{"NewTopic with a name the broker does not carry is refused at construction", func(*bites.App) {
			here(); bites.NewTopic[User]("room.1", refusalBroker{reject: "the name is over 63 bytes"}, bites.TopicOptions{})
		},
			`NewTopic "room.1": the broker does not carry this name: the name is over 63 bytes`, false},
		{"NewTopic with a message type that cannot be encoded is refused at construction", func(*bites.App) {
			here(); bites.NewTopic[struct{ C chan int }]("room.1", refusalBroker{}, bites.TopicOptions{})
		},
			`NewTopic "room.1": the message type struct { C chan int } cannot be sent through a broker: struct { C chan int }.C: a field of kind chan cannot be encoded: use a type a Socket can send, without channel or function fields`, false},
		{"NewSocketOwners with an empty name is refused at construction", func(*bites.App) {
			bites.NewSocketOwners("", at(refusalBroker{}), bites.SocketOwnersOptions{})
		},
			`NewSocketOwners: the name is empty: give the name every instance uses for these orders, such as "signout"`, false},
		{"NewSocketOwners with a nil broker is refused at construction", func(*bites.App) {
			here(); bites.NewSocketOwners("signout", nil, bites.SocketOwnersOptions{})
		},
			`NewSocketOwners "signout": the broker is nil: give the Broker the orders travel through, or use a zero bites.SocketOwners for sockets of this process only`, false},
		{"NewSocketOwners with a name the broker does not carry is refused at construction", func(*bites.App) {
			here(); bites.NewSocketOwners("signout", refusalBroker{reject: "the name is over 63 bytes"}, bites.SocketOwnersOptions{})
		},
			`NewSocketOwners "signout": the broker does not carry this name: the name is over 63 bytes`, false},
		{"NewTopics with an empty prefix is refused at construction", func(*bites.App) {
			bites.NewTopics[User]("", at(refusalBroker{}), bites.TopicsOptions{})
		},
			`NewTopics: the prefix is empty: give the start every instance puts before a key, such as "room.", so the names of this family are not those of another`, false},
		{"NewTopics with a nil broker is refused at construction", func(*bites.App) {
			here(); bites.NewTopics[User]("room.", nil, bites.TopicsOptions{})
		},
			`NewTopics "room.": the broker is nil: give the Broker this family publishes through, or use a zero bites.Topics[Out] for a family of this process only`, false},
		{"NewTopics with a negative MaxPayload is refused at construction", func(*bites.App) {
			here(); bites.NewTopics[User]("room.", refusalBroker{}, bites.TopicsOptions{MaxPayload: -1})
		},
			`NewTopics "room.": MaxPayload is -1, which is not a limit: give a positive number of bytes, or zero for 1 MiB`, false},
		{"NewTopics with a message type that cannot be encoded is refused at construction", func(*bites.App) {
			here(); bites.NewTopics[struct{ C chan int }]("room.", refusalBroker{}, bites.TopicsOptions{})
		},
			`NewTopics "room.": the message type struct { C chan int } cannot be sent through a broker: struct { C chan int }.C: a field of kind chan cannot be encoded: use a type a Socket can send, without channel or function fields`, false},
		{"ServeMetrics with a nil Metrics", func(a *bites.App) { here(); a.ServeMetrics("/metrics", nil) },
			"ServeMetrics /metrics: the Metrics is nil: make one with NewMetrics and give the same one to WithObserver", false},
		{"ServeMetrics at a path with no slash", func(a *bites.App) { here(); a.ServeMetrics("metrics", bites.NewMetrics()) },
			"ServeMetrics metrics: the path must start with a slash and have no wildcard, query or fragment", false},
		{"ServeMetrics at a path a route already has", func(a *bites.App) {
			a.Get(atFirst("/metrics"), ok[Empty])
			a.ServeMetrics(at("/metrics"), bites.NewMetrics())
		},
			`pattern "GET /metrics" (registered at refusal_test.go:%[1]d) conflicts with pattern "GET /metrics" (registered at refusal_test.go:%[2]d): they match the same requests`, true},
		{"CORS given to a group", func(a *bites.App) { here(); a.Group("/api", bites.Use(corsMW)) },
			`group "/api": Use: middleware 1 is CORS, which answers a preflight, an OPTIONS request no route has, so on a group or a route it is never reached and every preflight is a 405: give it to App.Use, with CORSOptions.Paths to limit it to a path such as /api`, false},
		{"CORS given to a route", func(a *bites.App) { here(); a.Get("/x", ok[Empty], bites.Use(passthru, corsMW)) },
			`GET /x: Use: middleware 2 is CORS, which answers a preflight, an OPTIONS request no route has, so on a group or a route it is never reached and every preflight is a 405: give it to App.Use, with CORSOptions.Paths to limit it to a path such as /api`, false},
		{"CORS given to Group.Use", func(a *bites.App) {
			g := a.Group("/api")
			here(); g.Use(corsMW)
		}, `group "/api": Use: middleware 1 is CORS, which answers a preflight, an OPTIONS request no route has, so on a group or a route it is never reached and every preflight is a 405: give it to App.Use, with CORSOptions.Paths to limit it to a path such as /api`, false},
		{"CORS returned by a UseEach function", func(a *bites.App) {
			here(); a.UseEach(func(bites.RouteInfo) (bites.Middleware, error) { return corsMW, nil })
			hereFirst(); a.Get("/x", ok[Empty])
		}, "GET /x: the function given to UseEach (at refusal_test.go:%d) returned CORS, which answers a preflight, an OPTIONS request no route has, so on a route it is never reached and every preflight is a 405: give it to App.Use, with CORSOptions.Paths to limit it to a path such as /api\n\tregistered at refusal_test.go:%d", true},
		{"CORS Paths of /", func(*bites.App) {
			bites.CORS(at(bites.CORSOptions{Origins: []string{"https://a.io"}, Paths: []string{"/"}}))
		},
			`CORS: Paths[0] is "/", which is every path: leave Paths empty for that`, false},
		{"CORS Paths that are not a prefix", func(*bites.App) {
			bites.CORS(at(bites.CORSOptions{Origins: []string{"https://a.io"}, Paths: []string{"/api", "api/"}}))
		},
			`CORS: Paths[1] "api/" is not a path prefix: it starts with a slash, does not end with one and has no wildcard, query or fragment ("/api")`, false},
		{"CORS Paths given twice", func(*bites.App) {
			bites.CORS(at(bites.CORSOptions{Origins: []string{"https://a.io"}, Paths: []string{"/api", "/api"}}))
		},
			`CORS: Paths[1] "/api" is given twice`, false},
		{"CORS with no origin is refused at construction", func(*bites.App) {
			bites.CORS(at(bites.CORSOptions{}))
		},
			`CORS: Origins is empty and AllowOrigin is nil: list the full origins that may call this API ("https://app.example.com") or a wildcard subdomain ("https://*.example.com"), set AllowOrigin to decide at run time, or use "*" for any origin`, false},
		{"CORS with * and another origin is refused at construction", func(*bites.App) {
			bites.CORS(at(bites.CORSOptions{Origins: []string{"*", "https://a.io"}}))
		},
			`CORS: Origins has "*" together with 1 other origins: "*" already allows every origin, so list it alone`, false},
		{"CORS with * and credentials is refused at construction", func(*bites.App) {
			bites.CORS(at(bites.CORSOptions{Origins: []string{"*"}, Credentials: true}))
		},
			`CORS: Origins is "*" with Credentials: a browser refuses a wildcard answer to a request with credentials, so list the full origins (https://app.example.com) or drop Credentials`, false},
		{"CORS with an origin that has no scheme is refused at construction", func(*bites.App) {
			bites.CORS(at(bites.CORSOptions{Origins: []string{"app.example.com"}}))
		},
			`CORS: Origins[0] "app.example.com" is not a full origin: it must start with http:// or https://`, false},
		{"CORS with an origin that has a trailing slash is refused at construction", func(*bites.App) {
			bites.CORS(at(bites.CORSOptions{Origins: []string{"https://ok.example", "https://app.example.com/"}}))
		},
			`CORS: Origins[1] "https://app.example.com/" is not a full origin: it must be scheme://host[:port], with no path, no trailing slash, no query and no user`, false},
		{"CORS with * and a wildcard subdomain is refused at construction", func(*bites.App) {
			bites.CORS(at(bites.CORSOptions{Origins: []string{"https://*.example.com", "*"}}))
		},
			`CORS: Origins has "*" together with the wildcard subdomain "https://*.example.com": "*" already allows every origin, so list it alone`, false},
		{"CORS with * and AllowOrigin is refused at construction", func(*bites.App) {
			bites.CORS(at(bites.CORSOptions{Origins: []string{"*"}, AllowOrigin: func(string) bool { return true }}))
		},
			`CORS: Origins is "*" with AllowOrigin: "*" already allows every origin, so AllowOrigin would never be asked; drop one of them`, false},
		{"CORS with a * inside a label is refused at construction", func(*bites.App) {
			bites.CORS(at(bites.CORSOptions{Origins: []string{"https://app-*.example.com"}}))
		},
			`CORS: Origins[0] "https://app-*.example.com" has a * that is not the whole leftmost label: write a wildcard subdomain as https://*.example.com, and list any other host in full`, false},
		{"CORS with a * that is not the leftmost label is refused at construction", func(*bites.App) {
			bites.CORS(at(bites.CORSOptions{Origins: []string{"https://app.*.com"}}))
		},
			`CORS: Origins[0] "https://app.*.com" has a * that is not the whole leftmost label: write a wildcard subdomain as https://*.example.com, and list any other host in full`, false},
		{"CORS with a wildcard over a top-level domain is refused at construction", func(*bites.App) {
			bites.CORS(at(bites.CORSOptions{Origins: []string{"https://*.com"}}))
		},
			`CORS: Origins[0] "https://*.com" is a wildcard over fewer than two labels: a wildcard such as https://*.com would allow a whole top-level domain, so name a domain you control (https://*.example.com)`, false},
		{"CORS with a wildcard over localhost is refused at construction", func(*bites.App) {
			bites.CORS(at(bites.CORSOptions{Origins: []string{"http://*.localhost:5173"}}))
		},
			`CORS: Origins[0] "http://*.localhost:5173" is a wildcard over fewer than two labels: a wildcard such as https://*.com would allow a whole top-level domain, so name a domain you control (https://*.example.com)`, false},
		{"CORS with a wildcard that has no scheme is refused at construction", func(*bites.App) {
			bites.CORS(at(bites.CORSOptions{Origins: []string{"*.example.com"}}))
		},
			`CORS: Origins[0] "*.example.com" is not a full origin: it must start with http:// or https://`, false},
		{"CORS with a wildcard that has a path is refused at construction", func(*bites.App) {
			bites.CORS(at(bites.CORSOptions{Origins: []string{"https://*.example.com/"}}))
		},
			`CORS: Origins[0] "https://*.example.com/" is not a full origin: it must be scheme://host[:port], with no path, no trailing slash, no query and no user`, false},
		{"CORS with the origin null is refused at construction", func(*bites.App) {
			bites.CORS(at(bites.CORSOptions{Origins: []string{"null"}}))
		},
			`CORS: Origins[0] "null" is not a full origin: "null" is what any sandboxed page sends, so it cannot be listed`, false},
		{"CORS with an origin whose port is not a number is refused at construction", func(*bites.App) {
			bites.CORS(at(bites.CORSOptions{Origins: []string{"http://localhost:http"}}))
		},
			`CORS: Origins[0] "http://localhost:http" is not a full origin: the port is not a number from 1 to 65535`, false},
		{"CORS with a method that is not a token is refused at construction", func(*bites.App) {
			bites.CORS(at(bites.CORSOptions{Origins: []string{"https://a.io"}, Methods: []string{"GET", "PU T"}}))
		},
			`CORS: Methods[1] "PU T" is not an HTTP method`, false},
		{"CORS with * as a method and credentials is refused at construction", func(*bites.App) {
			bites.CORS(at(bites.CORSOptions{Origins: []string{"https://a.io"}, Methods: []string{"*"}, Credentials: true}))
		},
			`CORS: Methods[0] is "*" with Credentials: a browser reads "*" literally when credentials are sent, so name the methods`, false},
		{"CORS with * as a header and credentials is refused at construction", func(*bites.App) {
			bites.CORS(at(bites.CORSOptions{Origins: []string{"https://a.io"}, Headers: []string{"*"}, Credentials: true}))
		},
			`CORS: Headers[0] is "*" with Credentials: a browser reads "*" literally when credentials are sent, so name the headers`, false},
		{"CORS with * and another header is refused at construction", func(*bites.App) {
			bites.CORS(at(bites.CORSOptions{Origins: []string{"https://a.io"}, Headers: []string{"*", "X-A"}}))
		},
			`CORS: Headers has "*" together with 1 other headers: "*" already allows every header, so list it alone`, false},
		{"CORS with a header that is not a token is refused at construction", func(*bites.App) {
			bites.CORS(at(bites.CORSOptions{Origins: []string{"https://a.io"}, Headers: []string{"Content Type"}}))
		},
			`CORS: Headers[0] "Content Type" is not an HTTP header name`, false},
		{"CORS with an exposed header that is not a token is refused at construction", func(*bites.App) {
			bites.CORS(at(bites.CORSOptions{Origins: []string{"https://a.io"}, ExposeHeaders: []string{"X:A"}}))
		},
			`CORS: ExposeHeaders[0] "X:A" is not an HTTP header name`, false},
		{"CORS with a negative max age is refused at construction", func(*bites.App) {
			bites.CORS(at(bites.CORSOptions{Origins: []string{"https://a.io"}, MaxAge: -time.Second}))
		},
			"CORS: MaxAge is -1s, which is not a whole number of seconds: give zero for the browser's default, or at least a second", false},
		{"CORS with a max age under a second is refused at construction", func(*bites.App) {
			bites.CORS(at(bites.CORSOptions{Origins: []string{"https://a.io"}, MaxAge: 500 * time.Millisecond}))
		},
			"CORS: MaxAge is 500ms, which is not a whole number of seconds: give zero for the browser's default, or at least a second", false},
		{"RequestID with a header that is not a token is refused at construction", func(*bites.App) {
			bites.RequestID(at(bites.RequestIDOptions{Header: "X Request"}))
		},
			`RequestID: Header "X Request" is not an HTTP header name: use letters, digits and the symbols of RFC 9110 token, without spaces or colons`, false},
		{"RequestID with two options values is refused at construction", func(*bites.App) {
			bites.RequestID(at(bites.RequestIDOptions{}), bites.RequestIDOptions{})
		},
			"RequestID: 2 options values were given; give one RequestIDOptions", false},
		{"AccessLog with two options values is refused at construction", func(*bites.App) {
			bites.AccessLog(at(bites.AccessLogOptions{}), bites.AccessLogOptions{})
		},
			"AccessLog: 2 options values were given; give one AccessLogOptions", false},
		{"Timeout of zero is refused at construction", func(*bites.App) {
			bites.Timeout(at(time.Duration(0)))
		},
			"Timeout: the duration is 0s, which is not a deadline: give a positive duration such as 2*time.Second", false},
		{"Timeout of a negative duration is refused at construction", func(*bites.App) {
			bites.Timeout(at(-time.Second))
		},
			"Timeout: the duration is -1s, which is not a deadline: give a positive duration such as 2*time.Second", false},
		// ---- the security track: trusted proxies (ADR 0025), then ADR 0053 to ADR 0057 ----
		{"WithTrustedProxies with no prefix is refused in New", func(*bites.App) { here(); bites.New(bites.WithTrustedProxies(bites.TrustedProxies{})) },
			`WithTrustedProxies: Prefixes is empty and UnixSockets is false: list the networks your load balancers connect from (netip.MustParsePrefix("10.0.0.0/8")), set UnixSockets for a proxy on the app's Unix socket, or leave the option out when no proxy is in front`, false},
		{"ProxyProtocol on an app that trusts no proxy is refused at the serving call", func(a *bites.App) { here(); a.Listen("127.0.0.1:0", bites.ProxyProtocol()) },
			"Listen: ProxyProtocol is given but the app trusts no proxy, so no peer could send the header: name the balancers with WithTrustedProxies (ADR 0058)", false},
		{"ProxyProtocol on a Unix socket whose peers are not trusted is refused", func(*bites.App) {
			b := bites.New(bites.WithTrustedProxies(bites.TrustedProxies{Prefixes: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}))
			b.ListenUnix("/nonexistent/x.sock", 0o660, at(bites.ProxyProtocol()))
		},
			"ListenUnix: ProxyProtocol on a Unix socket, but TrustedProxies.UnixSockets is false, so no peer on it could send the header: set UnixSockets, which trusts whoever the socket's permission bits let connect (ADR 0058)", false},
		{"ProxyProtocol on a TCP listener when only Unix peers are trusted is refused", func(*bites.App) {
			b := bites.New(bites.WithTrustedProxies(bites.TrustedProxies{UnixSockets: true}))
			b.Listen("127.0.0.1:0", at(bites.ProxyProtocol()))
		},
			"Listen: ProxyProtocol on a TCP listener, but TrustedProxies has no Prefixes, so no peer on it could send the header: list the networks the balancer connects from (ADR 0058)", false},
		{"a nil ListenOption is refused naming its place", func(a *bites.App) { here(); a.Listen("127.0.0.1:0", bites.ProxyProtocol(), nil) },
			"Listen: ListenOption 2 is nil", false},
		{"WithTrustedProxies with a zero prefix is refused in New", func(*bites.App) {
			bites.New(bites.WithTrustedProxies(at(bites.TrustedProxies{Prefixes: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), {}}})))
		},
			"WithTrustedProxies: Prefixes[1] is not a valid prefix (the zero netip.Prefix, or a length past the address): build it with netip.ParsePrefix", false},
		{"WithTrustedProxies with an IPv4 prefix of length 0 is refused in New", func(*bites.App) {
			bites.New(bites.WithTrustedProxies(at(bites.TrustedProxies{Prefixes: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")}})))
		},
			"WithTrustedProxies: Prefixes[0] 0.0.0.0/0 trusts every peer, so any client could name any address in X-Forwarded-For: list the networks of your proxies", false},
		{"WithTrustedProxies with an IPv6 prefix of length 0 is refused in New", func(*bites.App) {
			bites.New(bites.WithTrustedProxies(at(bites.TrustedProxies{Prefixes: []netip.Prefix{netip.MustParsePrefix("::/0")}, Header: "X-Real-IP"})))
		},
			"WithTrustedProxies: Prefixes[0] ::/0 trusts every peer, so any client could name any address in X-Real-IP: list the networks of your proxies", false},
		{"WithTrustedProxies with a header that is not a token is refused in New", func(*bites.App) {
			bites.New(bites.WithTrustedProxies(at(bites.TrustedProxies{Prefixes: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}, Header: "X Real IP"})))
		},
			`WithTrustedProxies: Header "X Real IP" is not an HTTP header name`, false},
		{"WithTrustedProxies with the Forwarded header is refused in New", func(*bites.App) {
			bites.New(bites.WithTrustedProxies(at(bites.TrustedProxies{Prefixes: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}, Header: "Forwarded"})))
		},
			`WithTrustedProxies: Header "Forwarded" is not read: its elements are "for=...;proto=...;host=..." pairs, not one address, and only the X-Forwarded-* headers are read (ADR 0025); name X-Forwarded-For (the default), or the single-address header your proxy writes`, false},
		{"WithTrustedProxies with X-Forwarded-Host as the address header is refused in New", func(*bites.App) {
			bites.New(bites.WithTrustedProxies(at(bites.TrustedProxies{Prefixes: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}, Header: "X-Forwarded-Host"})))
		},
			`WithTrustedProxies: Header "X-Forwarded-Host" does not carry the client's address: name X-Forwarded-For (the default), or the single-address header your proxy writes (CF-Connecting-IP, X-Real-IP); the forwarded host and prefix are ForwardedHost and ForwardedPrefix`, false},
		{"WithTrustedProxies given twice is refused naming both places", func(*bites.App) {
			p := bites.TrustedProxies{Prefixes: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}
			bites.New(bites.WithTrustedProxies(atBoth(p)), bites.WithTrustedProxies(p))
		},
			"WithTrustedProxies: given twice (first at refusal_test.go:%d, again at refusal_test.go:%d): give one TrustedProxies, because two could say different things", true},
		{"SecurityHeaders with two options values is refused at construction", func(*bites.App) {
			bites.SecurityHeaders(at(bites.SecurityHeadersOptions{}), bites.SecurityHeadersOptions{})
		},
			"SecurityHeaders: 2 options values were given; give one SecurityHeadersOptions", false},
		{"SecurityHeaders with a CSP that has no frame-ancestors is refused at construction", func(*bites.App) {
			bites.SecurityHeaders(at(bites.SecurityHeadersOptions{CSP: "default-src 'self'"}))
		},
			`SecurityHeaders: CSP "default-src 'self'" has no frame-ancestors directive, so any site could frame these answers: add frame-ancestors 'none', 'self', or the origins that may frame them`, false},
		{"SecurityHeaders with a CSP that would split the header is refused at construction", func(*bites.App) {
			bites.SecurityHeaders(at(bites.SecurityHeadersOptions{CSP: "default-src 'self';\r\nframe-ancestors 'none'"}))
		},
			"SecurityHeaders: CSP has the byte 0xd at 19, which a header value cannot carry", false},
		{"SecurityHeaders with a referrer policy that is not one is refused at construction", func(*bites.App) {
			bites.SecurityHeaders(at(bites.SecurityHeadersOptions{ReferrerPolicy: "none"}))
		},
			`SecurityHeaders: ReferrerPolicy "none" is not a referrer policy: use one of no-referrer, no-referrer-when-downgrade, origin, origin-when-cross-origin, same-origin, strict-origin, strict-origin-when-cross-origin, unsafe-url`, false},
		{"SecurityHeaders with a negative HSTS max-age is refused at construction", func(*bites.App) {
			bites.SecurityHeaders(at(bites.SecurityHeadersOptions{HSTSMaxAge: -time.Second}))
		},
			"SecurityHeaders: HSTSMaxAge is -1s, which is not a whole number of seconds: give zero for a year, or whole seconds", false},
		{"SecurityHeaders with an HSTS max-age that is not whole seconds is refused at construction", func(*bites.App) {
			bites.SecurityHeaders(at(bites.SecurityHeadersOptions{HSTSMaxAge: 1500 * time.Millisecond}))
		},
			"SecurityHeaders: HSTSMaxAge is 1.5s, which is not a whole number of seconds: give zero for a year, or whole seconds", false},
		{"SecurityHeaders with preload but not includeSubDomains is refused at construction", func(*bites.App) {
			bites.SecurityHeaders(at(bites.SecurityHeadersOptions{HSTSPreload: true}))
		},
			"SecurityHeaders: HSTSPreload needs HSTSIncludeSubdomains and an HSTSMaxAge of at least a year, or the preload list refuses the host", false},
		{"SecurityHeaders with preload and a max-age under a year is refused at construction", func(*bites.App) {
			bites.SecurityHeaders(at(bites.SecurityHeadersOptions{HSTSPreload: true, HSTSIncludeSubdomains: true, HSTSMaxAge: time.Hour}))
		},
			"SecurityHeaders: HSTSPreload needs HSTSIncludeSubdomains and an HSTSMaxAge of at least a year, or the preload list refuses the host", false},
		{"SecurityHeaders with NoHSTS and an HSTS field is refused at construction", func(*bites.App) {
			bites.SecurityHeaders(at(bites.SecurityHeadersOptions{NoHSTS: true, HSTSMaxAge: time.Hour}))
		},
			"SecurityHeaders: NoHSTS is given together with an HSTS field: keep one of the two", false},
		{"SecurityHeaders with a Permissions-Policy that is not name=value members is refused at construction", func(*bites.App) {
			bites.SecurityHeaders(at(bites.SecurityHeadersOptions{PermissionsPolicy: "geolocation"}))
		},
			`SecurityHeaders: PermissionsPolicy "geolocation" has a member that is not name=value: write feature=(allowlist) members separated by commas, as in "geolocation=(), camera=(), microphone=()"`, false},
		{"SecurityHeaders with a Permissions-Policy that leaves a parenthesis open is refused at construction", func(*bites.App) {
			bites.SecurityHeaders(at(bites.SecurityHeadersOptions{PermissionsPolicy: "camera=(self"}))
		},
			`SecurityHeaders: PermissionsPolicy "camera=(self" leaves a quote or parenthesis open: write feature=(allowlist) members separated by commas, as in "geolocation=(), camera=(), microphone=()"`, false},
		{"SecurityHeaders with NoPermissionsPolicy and a PermissionsPolicy is refused at construction", func(*bites.App) {
			bites.SecurityHeaders(at(bites.SecurityHeadersOptions{NoPermissionsPolicy: true, PermissionsPolicy: "camera=()"}))
		},
			"SecurityHeaders: NoPermissionsPolicy is given together with PermissionsPolicy: keep one of the two", false},
		{"SecurityHeaders with an opener policy that is not one is refused at construction", func(*bites.App) {
			bites.SecurityHeaders(at(bites.SecurityHeadersOptions{CrossOriginOpenerPolicy: "same-site"}))
		},
			`SecurityHeaders: CrossOriginOpenerPolicy "same-site" is not one of same-origin, same-origin-allow-popups, unsafe-none`, false},
		{"SecurityHeaders with an embedder policy that is not one is refused at construction", func(*bites.App) {
			bites.SecurityHeaders(at(bites.SecurityHeadersOptions{CrossOriginEmbedderPolicy: "require-cors"}))
		},
			`SecurityHeaders: CrossOriginEmbedderPolicy "require-cors" is not one of require-corp, credentialless, unsafe-none`, false},
		{"SecurityHeaders with a resource policy that is not one is refused at construction", func(*bites.App) {
			bites.SecurityHeaders(at(bites.SecurityHeadersOptions{CrossOriginResourcePolicy: "none"}))
		},
			`SecurityHeaders: CrossOriginResourcePolicy "none" is not one of same-site, same-origin, cross-origin`, false},
		{"BearerAuth with a nil verification function is refused at construction", func(*bites.App) {
			bites.BearerAuth[Principal](at[func(context.Context, string) (Principal, error)](nil))
		},
			"BearerAuth: the verification function is nil: give the function that decides whether a credential is good", false},
		{"BasicAuth with two options values is refused at construction", func(*bites.App) {
			bites.BasicAuth(at(basicNobody), bites.AuthOptions{}, bites.AuthOptions{})
		},
			"BasicAuth: 2 options values were given; give one AuthOptions", false},
		{"BearerAuth with a realm a quoted string cannot carry is refused at construction", func(*bites.App) {
			bites.BearerAuth(at(verifyToken), bites.AuthOptions{Realm: `my "api"`})
		},
			`BearerAuth: Realm "my \"api\"" has a character a quoted realm cannot carry: use printable ASCII without a double quote or a backslash`, false},
		{"BearerAuth with a Header is refused at construction", func(*bites.App) {
			bites.BearerAuth(at(verifyToken), bites.AuthOptions{Header: "X-Token"})
		},
			`BearerAuth: Header is "X-Token", but BearerAuth reads Authorization, as its RFC says; Header is for APIKeyAuth`, false},
		{"APIKeyAuth with a header that is not a token is refused at construction", func(*bites.App) {
			bites.APIKeyAuth(at(verifyToken), bites.AuthOptions{Header: "X Key"})
		},
			`APIKeyAuth: Header "X Key" is not an HTTP header name`, false},
		{"APIKeyAuth reading Authorization is refused at construction", func(*bites.App) {
			bites.APIKeyAuth(at(verifyToken), bites.AuthOptions{Header: "authorization"})
		},
			"APIKeyAuth: Header is authorization, whose value is not a bare key: use BearerAuth for a token in Authorization, or a header of the key's own (X-API-Key)", false},
		{"CustomAuth with a nil Provider is refused at construction", func(*bites.App) {
			bites.CustomAuth[Principal](bites.SecurityScheme{Type: "mutualTLS"}, at[func(context.Context, bites.Request, struct{}) (Principal, error)](nil))
		},
			"CustomAuth: the Provider is nil", false},
		{"CustomAuth with a Type it cannot state is refused at construction", func(*bites.App) {
			bites.CustomAuth(at(bites.SecurityScheme{Type: "oauth2"}), customWho)
		},
			`CustomAuth: Type "oauth2" is not a scheme CustomAuth states: give "http", "apiKey", "mutualTLS" or "openIdConnect" (an OAuth 2 access token is Type "http" with Scheme "bearer")`, false},
		{"CustomAuth of Type http with no Scheme is refused at construction", func(*bites.App) {
			bites.CustomAuth(at(bites.SecurityScheme{Type: "http"}), customWho)
		},
			`CustomAuth: Scheme "" is not an HTTP auth scheme: give a token, such as "bearer" or "digest"`, false},
		{"CustomAuth with a BearerFormat for a scheme that is not bearer is refused at construction", func(*bites.App) {
			bites.CustomAuth(at(bites.SecurityScheme{Type: "http", Scheme: "basic", BearerFormat: "JWT"}), customWho)
		},
			`CustomAuth: BearerFormat is given, but Scheme is "basic": a bearer format describes a Bearer token`, false},
		{"CustomAuth of an API key in the query is refused at construction", func(*bites.App) {
			bites.CustomAuth(at(bites.SecurityScheme{Type: "apiKey", In: "body", Name: "key"}), customWho)
		},
			`CustomAuth: In "body" is not where an API key is described: give "header", "cookie" or, for a Provider that reads a Query section on a WebSocket or event stream route, "query"`, false},
		{"CustomAuth of an API key in Authorization is refused at construction", func(*bites.App) {
			bites.CustomAuth(at(bites.SecurityScheme{Type: "apiKey", In: "header", Name: "Authorization"}), customWho)
		},
			`CustomAuth: Name "Authorization" is not the header of a key: give a header name of its own, such as X-API-Key (Authorization is Type "http", a cookie is In "cookie")`, false},
		{"CustomAuth of an API key in a cookie whose name is not a token is refused at construction", func(*bites.App) {
			bites.CustomAuth(at(bites.SecurityScheme{Type: "apiKey", In: "cookie", Name: "my sid"}), customWho)
		},
			`CustomAuth: Name "my sid" is not a cookie name: use an HTTP token`, false},
		{"CustomAuth of OpenID Connect with a URL that is not absolute is refused at construction", func(*bites.App) {
			bites.CustomAuth(at(bites.SecurityScheme{Type: "openIdConnect", OpenIDConnectURL: "/.well-known/openid-configuration"}), customWho)
		},
			`CustomAuth: OpenIDConnectURL "/.well-known/openid-configuration" is not an absolute URL: give the provider's discovery document, such as https://id.example.com/.well-known/openid-configuration`, false},
		{"CustomAuth with a field its Type does not have is refused at construction", func(*bites.App) {
			bites.CustomAuth(at(bites.SecurityScheme{Type: "mutualTLS", Name: "X-Client-Cert"}), customWho)
		},
			`CustomAuth: Name is given, but a scheme of Type "mutualTLS" has no Name`, false},
		{"AnyAuth of one Provider is refused at construction", func(*bites.App) {
			bites.AnyAuth(at(bites.BearerAuth(verifyToken)))
		},
			"AnyAuth: it chooses among two Providers or more, and 1 is given: give a single one to Provide directly", false},
		{"AnyAuth with a nil Provider is refused at construction", func(*bites.App) {
			bites.AnyAuth(at(bites.BearerAuth(verifyToken)), nil)
		},
			"AnyAuth: Provider 2 is nil", false},
		{"AnyAuth of a Provider the framework did not make is refused at construction", func(*bites.App) {
			bites.AnyAuth(at(bites.BearerAuth(verifyToken)), customWho)
		},
			"AnyAuth: Provider 2 was not made by BearerAuth, BasicAuth, APIKeyAuth, CustomAuth or AnyAuth, so where its credential is sent is not known: give it to CustomAuth with its SecurityScheme", false},
		{"AnyAuth of two Providers of one credential is refused at construction", func(*bites.App) {
			bites.AnyAuth(at(bites.BearerAuth(verifyToken)), bites.CustomAuth(bites.SecurityScheme{Type: "http", Scheme: "Bearer"}, customWho))
		},
			"AnyAuth: Provider 2 reads the bearer scheme of Authorization, as Provider 1 does, so it would never run: keep one of the two", false},
		{"NewSessions with no key is refused at construction", func(*bites.App) {
			bites.NewSessions[Cart](at(bites.SessionOptions{}))
		},
			"NewSessions: Keys is empty: give at least one key of 32 random bytes (crypto/rand), kept outside the source", false},
		{"NewSessions with a key that is not 32 bytes is refused at construction", func(*bites.App) {
			bites.NewSessions[Cart](at(bites.SessionOptions{Keys: [][]byte{key(1)[:16]}}))
		},
			"NewSessions: Keys[0] is 16 bytes; a session key is 32 random bytes, for AES-256-GCM", false},
		{"NewSessions with a key of zeros is refused at construction", func(*bites.App) {
			bites.NewSessions[Cart](at(bites.SessionOptions{Keys: [][]byte{make([]byte, 32)}}))
		},
			"NewSessions: Keys[0] is all zeros, a placeholder and not a key: generate 32 random bytes with crypto/rand", false},
		{"NewSessions with a key given twice is refused at construction", func(*bites.App) {
			bites.NewSessions[Cart](at(bites.SessionOptions{Keys: [][]byte{key(1), key(1)}}))
		},
			"NewSessions: Keys[1] is the same key as Keys[0]", false},
		{"NewSessions with a cookie name that is not a token is refused at construction", func(*bites.App) {
			bites.NewSessions[Cart](at(bites.SessionOptions{Keys: [][]byte{key(1)}, Cookie: "my session"}))
		},
			`NewSessions: Cookie "my session" is not a cookie name: use an HTTP token`, false},
		{"NewSessions with the __Host- prefix and a Domain is refused at construction", func(*bites.App) {
			bites.NewSessions[Cart](at(bites.SessionOptions{Keys: [][]byte{key(1)}, Cookie: "__Host-s", Domain: "example.com"}))
		},
			`NewSessions: Cookie "__Host-s" has the __Host- prefix, which browsers accept only with Secure, Path=/ and no Domain: drop the prefix or those options`, false},
		{"NewSessions with the __Secure- prefix and NotSecure is refused at construction", func(*bites.App) {
			bites.NewSessions[Cart](at(bites.SessionOptions{Keys: [][]byte{key(1)}, Cookie: "__Secure-s", NotSecure: true}))
		},
			`NewSessions: Cookie "__Secure-s" has the __Secure- prefix, which browsers accept only with Secure: drop the prefix or NotSecure`, false},
		{"NewSessions with SameSite None and NotSecure is refused at construction", func(*bites.App) {
			bites.NewSessions[Cart](at(bites.SessionOptions{Keys: [][]byte{key(1)}, SameSite: bites.NoSameSite, NotSecure: true}))
		},
			"NewSessions: SameSite is None without Secure, which browsers reject: drop NotSecure", false},
		{"NewSessions with a negative MaxAge is refused at construction", func(*bites.App) {
			bites.NewSessions[Cart](at(bites.SessionOptions{Keys: [][]byte{key(1)}, MaxAge: -time.Second}))
		},
			"NewSessions: MaxAge is -1s, which is not a whole number of seconds: give zero for 24 hours, or whole seconds", false},
		{"NewSessions with a RefreshAfter that is not under MaxAge is refused at construction", func(*bites.App) {
			bites.NewSessions[Cart](at(bites.SessionOptions{Keys: [][]byte{key(1)}, MaxAge: time.Hour, RefreshAfter: time.Hour}))
		},
			"NewSessions: RefreshAfter is 1h0m0s, but it must be under MaxAge (1h0m0s) and not negative, or a session would expire before it is refreshed: give MaxAge/2, or zero for a session that does not slide", false},
		{"NewSessions of a type JSON cannot encode is refused at construction", func(*bites.App) {
			bites.NewSessions[func()](at(bites.SessionOptions{Keys: [][]byte{key(1)}}))
		},
			"NewSessions: the session's type func() cannot be encoded as JSON, so it cannot be kept in a cookie or a store: give a type of plain fields", false},
		{"NewSessions of a type that states an owner with no Store is refused at construction", func(*bites.App) {
			bites.NewSessions[Login](at(bites.SessionOptions{Keys: [][]byte{key(1)}}))
		},
			"NewSessions: Login states an owner (a SessionOwner method) but SessionOptions has no Store, and a session sealed in a cookie cannot be revoked: give a Store that implements OwnerSessionStore, or drop the SessionOwner method", false},
		{"NewSessions of a type that states an owner with a Store that cannot keep owners is refused at construction", func(*bites.App) {
			bites.NewSessions[Login](at(bites.SessionOptions{Keys: [][]byte{key(1)}, Store: plainStore{}}))
		},
			"NewSessions: Login states an owner (a SessionOwner method) but the Store bites_test.plainStore does not implement OwnerSessionStore (SaveOwned and DeleteOwner), so it cannot find the sessions of one owner: add those two methods to the store, or drop the SessionOwner method", false},
		{"NewSessions of a type whose SessionOwner is on the pointer receiver is refused at construction", func(*bites.App) {
			bites.NewSessions[ptrOwner](at(bites.SessionOptions{Keys: [][]byte{key(1)}, Store: newMemStore()}))
		},
			"NewSessions: SessionOwner is declared on *ptrOwner but a Session holds a ptrOwner, so it would never be found: declare it on the value receiver, func (ptrOwner) SessionOwner() string", false},
		{"NewSessions of a type whose SessionOwner does not return a string is refused at construction", func(*bites.App) {
			bites.NewSessions[intOwner](at(bites.SessionOptions{Keys: [][]byte{key(1)}, Store: newMemStore()}))
		},
			"NewSessions: intOwner has a SessionOwner method of type func(bites_test.intOwner) int; it must be func() string, returning the owner of the session", false},
		{"NewRateLimiter with no limit is refused at construction", func(*bites.App) {
			bites.NewRateLimiter(at(bites.RateLimitOptions{Period: time.Minute}))
		},
			"NewRateLimiter: Limit is 0: give the number of requests a key may make in a Period, at least 1", false},
		{"NewRateLimiter with a period that is not whole seconds is refused at construction", func(*bites.App) {
			bites.NewRateLimiter(at(bites.RateLimitOptions{Limit: 10, Period: 1500 * time.Millisecond}))
		},
			"NewRateLimiter: Period is 1.5s, which is not a whole number of seconds: the RateLimit headers count in seconds, so give time.Second or more in whole seconds", false},
		{"NewRateLimiter with no period is refused at construction", func(*bites.App) {
			bites.NewRateLimiter(at(bites.RateLimitOptions{Limit: 10}))
		},
			"NewRateLimiter: Period is 0s, which is not a whole number of seconds: the RateLimit headers count in seconds, so give time.Second or more in whole seconds", false},
		{"NewRateLimiter with a negative burst is refused at construction", func(*bites.App) {
			bites.NewRateLimiter(at(bites.RateLimitOptions{Limit: 10, Period: time.Minute, Burst: -1}))
		},
			"NewRateLimiter: Burst is -1: give zero for Limit, or the most requests a key may make at once", false},
		{"NewRateLimiter with a negative MaxKeys is refused at construction", func(*bites.App) {
			bites.NewRateLimiter(at(bites.RateLimitOptions{Limit: 10, Period: time.Minute, MaxKeys: -1}))
		},
			"NewRateLimiter: MaxKeys is -1, which is not a bound: give zero for 100,000, or a positive number", false},
		{"NewRateLimiter with MaxKeys and a Store is refused at construction", func(*bites.App) {
			bites.NewRateLimiter(at(bites.RateLimitOptions{Limit: 10, Period: time.Minute, MaxKeys: 10, Store: &fakeStore{}}))
		},
			"NewRateLimiter: MaxKeys is given with a Store: MaxKeys bounds the in-memory store, which a Store replaces, so bound the Store's own memory instead", false},
		{"NewRateLimiter with a name that is not a policy name is refused at construction", func(*bites.App) {
			bites.NewRateLimiter(at(bites.RateLimitOptions{Limit: 10, Period: time.Minute, Name: `per "user"`}))
		},
			`NewRateLimiter: Name "per \"user\"" is not a policy name: use at most 16 letters, digits, '-' or '_'`, false},
		{"NewRateLimiter with more than a request a nanosecond is refused at construction", func(*bites.App) {
			bites.NewRateLimiter(at(bites.RateLimitOptions{Limit: 2_000_000_000, Period: time.Second}))
		},
			"NewRateLimiter: Limit 2000000000 in a Period of 1s is more than one request a nanosecond", false},
		{"CSRF with no origin on an app with no list is refused when the app gets it", func(a *bites.App) { here(); a.Use(bites.CSRF(bites.CSRFOptions{})) },
			`app: Use: CSRF: Origins is empty and the app has no list to take them from: list the full origins of the pages that send forms and scripts to this app ("https://app.example.com"), its own included, in CSRFOptions.Origins, or once for the app, WebSocket routes too, with WithAllowOrigins`, false},
		{"CSRF with no origin on a route of an app with no list is refused at the route", func(a *bites.App) { here(); a.Post("/x", ok[Empty], bites.Use(bites.CSRF(bites.CSRFOptions{}))) },
			`POST /x: CSRF: Origins is empty and the app has no list to take them from: list the full origins of the pages that send forms and scripts to this app ("https://app.example.com"), its own included, in CSRFOptions.Origins, or once for the app, WebSocket routes too, with WithAllowOrigins`, false},
		{"CSRF with no origin on an app that allows any origin is refused when the app gets it", func(*bites.App) {
			a := bites.New(bites.WithAllowAnyOrigin())
			a.Use(at(bites.CSRF(bites.CSRFOptions{})))
		},
			"app: Use: CSRF: Origins is empty and the app's list is WithAllowAnyOrigin(), which would let every site send requests with the user's cookies: list the origins in CSRFOptions.Origins, or give the app WithAllowOrigins instead", false},
		{"a WebSocket route listing an origin outside the app's list is refused", func(*bites.App) {
			a := bites.New(bites.WithAllowOrigins(atFirst("https://app.example.com")))
			a.WebSocket("/x", wsOK, at(bites.AllowOrigins("https://other.example")))
		},
			`WebSocket /x: AllowOrigins("https://other.example") is not in the app's list (WithAllowOrigins at refusal_test.go:%[2]d): add it there, or give the app no list and list origins per route
	registered at refusal_test.go:%[1]d`, true},
		{"WithAllowOrigins with an origin that is not http or https", func(*bites.App) { here(); bites.WithAllowOrigins("ftp://files.example") },
			`WithAllowOrigins("ftp://files.example") is not a full origin: it must start with http:// or https://`, false},
		{"CSRF with the origin * is refused at construction", func(*bites.App) {
			bites.CSRF(at(bites.CSRFOptions{Origins: []string{"*"}}))
		},
			`CSRF: Origins[0] is "*", which would let every site send requests with the user's cookies: list the origins`, false},
		{"CSRF with an origin that is not a full origin is refused at construction", func(*bites.App) {
			bites.CSRF(at(bites.CSRFOptions{Origins: []string{"https://a.io", "app.example.com"}}))
		},
			`CSRF: Origins[1] "app.example.com" is not a full origin: it must start with http:// or https://`, false},
		{"CSRF with a cookie name that is not a token is refused at construction", func(*bites.App) {
			bites.CSRF(at(bites.CSRFOptions{Origins: []string{"https://a.io"}, Cookie: "my csrf"}))
		},
			`CSRF: Cookie "my csrf" is not a cookie name: use an HTTP token`, false},
		{"CSRF with a prefixed cookie and NotSecure is refused at construction", func(*bites.App) {
			bites.CSRF(at(bites.CSRFOptions{Origins: []string{"https://a.io"}, Cookie: "__Host-x", NotSecure: true}))
		},
			`CSRF: Cookie "__Host-x" has a prefix browsers accept only with Secure: drop the prefix or NotSecure`, false},
		{"CSRF with a header that is not a token is refused at construction", func(*bites.App) {
			bites.CSRF(at(bites.CSRFOptions{Origins: []string{"https://a.io"}, Header: "X CSRF"}))
		},
			`CSRF: Header "X CSRF" is not an HTTP header name`, false},
		{"NewRateLimiter with a bucket that takes more than ten years to refill is refused at construction", func(*bites.App) {
			bites.NewRateLimiter(at(bites.RateLimitOptions{Limit: 1, Period: 365 * 24 * time.Hour, Burst: 11}))
		},
			"NewRateLimiter: a Burst of 11 at one request every 8760h0m0s takes more than ten years to refill: give a shorter Period or a smaller Burst", false},
		// ---- end of the security track ----
		{"an interface output with no Returns", func(a *bites.App) { here(); a.Get("/x", anyOut) },
			"GET /x: the handler returns interface {}, an interface, and the route has no Returns: declare each type it may return with Returns[T](), so the API description has its statuses and a wrong type is refused now and not answered with a 500", false},
		{"a Returns of a type that does not implement the output", func(a *bites.App) { here(); a.Get("/x", namerOut, bites.Returns[Empty]()) },
			"GET /x: Returns[bites_test.Empty]: bites_test.Empty does not implement bites_test.namer, the type the handler returns", false},
		{"a Returns given twice", func(a *bites.App) { here(); a.Get("/x", anyOut, bites.Returns[User](), bites.Returns[User]()) },
			"GET /x: Returns[bites_test.User] is given twice", false},
		{"a Returns on a route whose output is not an interface", func(a *bites.App) { here(); a.Get("/x/{id}", getUser, bites.Returns[User]()) },
			"GET /x/{id}: Returns[bites_test.User] is given, but the handler returns bites_test.User, which is not an interface: Returns declares what an interface return may hold", false},
		{"a Returns given to a group", func(a *bites.App) { here(); a.Group("/g", bites.Returns[User]()) },
			`group "/g": Returns[bites_test.User] is given to a group, but each route's handler returns its own types: give it to the route`, false},
		{"a Returns of a pointer type", func(a *bites.App) { here(); a.Get("/x", anyOut, bites.Returns[*User]()) },
			"GET /x: Returns[*bites_test.User] names a pointer; declare bites_test.User, which the handler may return by value or by pointer", false},
		{"a Returns of an interface type", func(a *bites.App) { here(); a.Get("/x", anyOut, bites.Returns[any]()) },
			"GET /x: Returns[interface {}] names an interface; declare the concrete types the handler returns", false},
		{"a Returns of a type that cannot be written as JSON", func(a *bites.App) { here(); a.Get("/x", anyOut, bites.Returns[chan int]()) },
			"GET /x: Returns[chan int]: the output type cannot be written as JSON: chan int: a field of kind chan cannot be encoded", false},
		{"a Returns of a stream on a HEAD route", func(a *bites.App) { here(); a.Head("/x", anyOut, bites.Returns[bites.SSE]()) },
			"HEAD /x: Returns[bites.SSE]: a HEAD route cannot return a stream: its answer has no body to stream", false},
		{"a Returns on a WebSocket route", func(a *bites.App) { here(); a.WebSocket("/x", wsOK, bites.Returns[User]()) },
			"WebSocket /x: Returns lists the answers of a handler that returns an interface, but a WebSocket handler returns an Inbox and answers 101: its messages are the Socket's and the Inbox's types", false},
		{"a OneOf variant that cannot be written as JSON", func(a *bites.App) { here(); a.Get("/x", outOf[bites.OneOf2[chan int, User]]) },
			"GET /x: variant A of the OneOf2, chan int: the output type cannot be written as JSON: chan int: a field of kind chan cannot be encoded", false},
		{"a OneOf that names one type twice", func(a *bites.App) { here(); a.Get("/x", outOf[bites.OneOf3[User, bites.NoContent, User]]) },
			"GET /x: variants A and C of the OneOf3 are both bites_test.User: each variant is a different answer, so name each type once", false},
		{"a OneOf nested in a OneOf", func(a *bites.App) { here(); a.Get("/x", outOf[nestedOneOf]) },
			"GET /x: variant B of the OneOf2 is itself a OneOf2: list its types in the outer OneOf, which has room for up to four", false},
		{"a OneOf variant that is a pointer", func(a *bites.App) { here(); a.Get("/x", outOf[bites.OneOf2[*User, bites.NoContent]]) },
			"GET /x: variant A of the OneOf2 is *bites_test.User, a pointer: name bites_test.User, which the OneOf holds by value", false},
		{"a OneOf variant that is an interface", func(a *bites.App) { here(); a.Get("/x", outOf[bites.OneOf2[User, any]]) },
			"GET /x: variant B of the OneOf2 is interface {}, an interface: a OneOf names the concrete types the handler returns", false},
		{"a OneOf variant that is a stream on a HEAD route", func(a *bites.App) { here(); a.Head("/x", outOf[bites.OneOf2[bites.NoContent, bites.SSE]]) },
			"HEAD /x: variant B of the OneOf2, bites.SSE: a HEAD route cannot return a stream: its answer has no body to stream", false},
		{"a Returns on a route whose output is a OneOf", func(a *bites.App) { here(); a.Get("/x", outOf[bites.OneOf2[User, Empty]], bites.Returns[User]()) },
			"GET /x: Returns[bites_test.User] is given, but the handler returns a OneOf2, whose type arguments already say what it may return: drop the Returns", false},
		{"a Returns of a OneOf", func(a *bites.App) { here(); a.Get("/x", anyOut, bites.Returns[bites.OneOf2[User, Empty]]()) },
			"GET /x: Returns[bites.OneOf2[github.com/nevindra/bites_test.User,github.com/nevindra/bites_test.Empty]] names a OneOf; make the OneOf2 the handler's return type, which needs no Returns", false},
		{"a pointer to a OneOf as the output", func(a *bites.App) { here(); a.Get("/x", outOf[*bites.OneOf2[User, Empty]]) },
			"GET /x: the handler returns a pointer to a OneOf2; a OneOf is returned by value, its variant chosen with the method of its letter", false},
		{"a OneOf as the Body of a wrapper", func(a *bites.App) { here(); a.Get("/x", outOf[bites.OK[bites.OneOf2[User, Empty]]]) },
			"GET /x: OneOf2 is the Body of OK; a OneOf is the whole return type, and each of its variants is a wrapper of its own", false},
		{"a OneOf inside a plain output", func(a *bites.App) { here(); a.Get("/x", outOf[oneOfHolder]) },
			"GET /x: the output type oneOfHolder holds a OneOf2; a OneOf is the handler's whole return type, not a value written as JSON", false},
		{"a OneOf in a request body", func(a *bites.App) { here(); a.Post("/x", ok[OneOfBodyIn]) },
			"POST /x: OneOfBodyIn.Body holds a OneOf2; a OneOf is a handler's return type, not a request body", false},
		{"a Socket that sends a OneOf", func(a *bites.App) { here(); a.WebSocket("/x", wsHandler[Empty, bites.OneOf2[wsOut, Empty], wsMsg]) },
			"WebSocket /x: the handler's Socket sends OneOf2[github.com/nevindra/bites_test.wsOut,github.com/nevindra/bites_test.Empty], one of the framework's response types; a WebSocket message is a plain value, written as JSON (or []byte, written as a binary frame), and has no status or headers", false},
		{"a Socket that sends a value holding a OneOf", func(a *bites.App) { here(); a.WebSocket("/x", wsHandler[Empty, oneOfHolder, wsMsg]) },
			"WebSocket /x: the handler's Socket sends oneOfHolder, which holds a OneOf2; a OneOf is a handler's return type, not a WebSocket message", false},
		{"an Inbox whose message holds a OneOf", func(a *bites.App) { here(); a.WebSocket("/x", wsHandler[Empty, wsOut, []bites.OneOf2[wsMsg, Empty]]) },
			"WebSocket /x: the Inbox's message type holds a OneOf2; a OneOf is a handler's return type, not a WebSocket message", false},
		{"a Summary given to a group", func(a *bites.App) { here(); a.Group("/g", bites.Summary("s")) },
			`group "/g": Summary is given to a group, but one summary cannot describe every operation in it: give it to each route`, false},
		{"a Description given to a group", func(a *bites.App) { here(); a.Group("/g", bites.Description("s")) },
			`group "/g": Description is given to a group, but one description cannot describe every operation in it: give it to each route`, false},
		{"an empty Summary", func(a *bites.App) { here(); a.Get("/x", ok[Empty], bites.Summary(" ")) },
			"GET /x: Summary is empty", false},
		{"an empty Description", func(a *bites.App) { here(); a.Get("/x", ok[Empty], bites.Description("")) },
			"GET /x: Description is empty", false},
		{"a Summary on an Undocumented route", func(a *bites.App) { here(); a.Get("/x", ok[Empty], bites.Summary("s"), bites.Undocumented()) },
			"GET /x: Summary, Description and OperationID describe an operation of the API document, which Undocumented leaves this route out of: give one or the other", false},
		{"a Description on a route of an Undocumented group", func(a *bites.App) {
			a.Group("/g", bites.Undocumented()).Get(at("/x"), ok[Empty], bites.Description("d"))
		},
			"GET /g/x: Summary, Description and OperationID describe an operation of the API document, which Undocumented leaves this route out of: give one or the other", false},
		{"an OperationID on an Undocumented route", func(a *bites.App) { here(); a.Get("/x", ok[Empty], bites.OperationID("x"), bites.Undocumented()) },
			"GET /x: Summary, Description and OperationID describe an operation of the API document, which Undocumented leaves this route out of: give one or the other", false},
		{"ErrorStatuses with no status", func(a *bites.App) { here(); a.Get("/x", ok[Empty], bites.ErrorStatuses()) },
			"GET /x: ErrorStatuses is given no status: name the error statuses the handler returns, as ErrorStatuses(404, 409)", false},
		{"ErrorStatuses given a status that is not an error", func(a *bites.App) { here(); a.Get("/x", ok[Empty], bites.ErrorStatuses(404, 302)) },
			"GET /x: ErrorStatuses is given 302, which is not an error status: give the statuses from 400 to 599 the handler returns", false},
		{"ErrorStatuses given a status twice", func(a *bites.App) { here(); a.Get("/x", ok[Empty], bites.ErrorStatuses(404, 409, 404)) },
			"GET /x: ErrorStatuses is given 404 twice", false},
		{"ErrorStatuses on an Undocumented route", func(a *bites.App) { here(); a.Get("/x", ok[Empty], bites.ErrorStatuses(404), bites.Undocumented()) },
			"GET /x: ErrorStatuses states answers in the API document, which Undocumented leaves this route out of: give one or the other", false},
		{"ErrorStatuses on a Static route", func(a *bites.App) {
			a.Static("/s", at(bites.Static{FS: fstest.MapFS{}}), bites.ErrorStatuses(404))
		},
			"Static /s: ErrorStatuses states answers in the API document, which leaves static files out: give it to the typed routes instead", false},
		{"ErrorStatus given 400, 401 or 429", func(a *bites.App) { here(); a.Get("/x", ok[Empty], bites.ErrorStatus[staleBlocks](429)) },
			"GET /x: ErrorStatus[staleBlocks] is given 429, which the framework answers in a shape of its own (400 lists the Input's mistakes, 401 and 429 carry headers): use a status the handler decides, such as 409 or 422", false},
		{"ErrorStatus given a status that is not an error", func(a *bites.App) { here(); a.Get("/x", ok[Empty], bites.ErrorStatus[staleBlocks](302)) },
			"GET /x: ErrorStatus[staleBlocks] is given 302, which is not an error status: give a status from 400 to 599 the handler returns", false},
		{"ErrorStatus given a type that is not a struct", func(a *bites.App) { here(); a.Get("/x", ok[Empty], bites.ErrorStatus[string](409)) },
			"GET /x: ErrorStatus[string](409): the members of an error are the fields of a struct, so T must be a struct type, as ErrorStatus[StaleBlocks](409)", false},
		{"ErrorStatus given one status twice", func(a *bites.App) {
			a.Group("/g", bites.ErrorStatus[staleBlocks](409)).Get(at("/x"), ok[Empty], bites.ErrorStatus[staleBlocks](409))
		},
			"GET /g/x: ErrorStatus is given 409 twice: state the members of a status once, on the route or on its group", false},
		{"ErrorStatus with a field that is a member of the problem", func(a *bites.App) { here(); a.Get("/x", ok[Empty], bites.ErrorStatus[clashingMembers](409)) },
			`GET /x: ErrorStatus[clashingMembers](409): a field of clashingMembers is written as "title", a member the problem body already has (type, title, status, detail, instance, errors, truncated, request_id): give the field another wire name with a json tag`, false},
		{"ErrorStatus for a status the framework answers on the route", func(a *bites.App) {
			a.Post(at("/x"), ok[struct{ Body struct{ N int } }], bites.ErrorStatus[staleBlocks](413))
		},
			"POST /x: ErrorStatus[staleBlocks](413): the framework already answers 413 on this route with a shape of its own, so the document cannot state members for it: use a status the handler decides, such as 409 or 422", false},
		{"ErrorStatus on an Undocumented route", func(a *bites.App) { here(); a.Get("/x", ok[Empty], bites.ErrorStatus[staleBlocks](409), bites.Undocumented()) },
			"GET /x: ErrorStatus states answers in the API document, which Undocumented leaves this route out of: give one or the other", false},
		{"MediaType given to a group", func(a *bites.App) { here(); a.Group("/g", bites.MediaType("text/csv")) },
			`group "/g": MediaType is given to a group, but the routes of a group send different types: give it to each route`, false},
		{"MediaType that is not a media type", func(a *bites.App) { here(); a.Get("/x", streamOK, bites.MediaType("ndjson")) },
			`GET /x: MediaType "ndjson" is not a media type: give type/subtype, with no wildcard, as "application/x-ndjson" or "text/csv; charset=utf-8"`, false},
		{"MediaType with a wildcard", func(a *bites.App) { here(); a.Get("/x", streamOK, bites.MediaType("text/*")) },
			`GET /x: MediaType "text/*" is not a media type: give type/subtype, with no wildcard, as "application/x-ndjson" or "text/csv; charset=utf-8"`, false},
		{"MediaType on a JSON route", func(a *bites.App) { here(); a.Get("/x", ok[Empty], bites.MediaType("text/csv")) },
			"GET /x: MediaType is given, but every answer of the handler has a media type the API document already states (JSON, text or an event stream): MediaType is for a Bytes, File or Stream answer", false},
		{"MediaType on an Undocumented route", func(a *bites.App) { here(); a.Get("/x", streamOK, bites.MediaType("text/csv"), bites.Undocumented()) },
			"GET /x: MediaType states answers in the API document, which Undocumented leaves this route out of: give one or the other", false},
		{"SendsHeader with a name that is not a header", func(a *bites.App) { here(); a.Get("/x", ok[Empty], bites.SendsHeader[string]("E Tag", "d")) },
			`GET /x: SendsHeader is given "E Tag", which is not a header name`, false},
		{"SendsHeader with Content-Type", func(a *bites.App) { here(); a.Get("/x", ok[Empty], bites.SendsHeader[string]("content-type", "d")) },
			"GET /x: SendsHeader is given Content-Type, which the media type of the answer states: use MediaType", false},
		{"SendsHeader with Location", func(a *bites.App) { here(); a.Get("/x", ok[Empty], bites.SendsHeader[string]("Location", "d")) },
			"GET /x: SendsHeader is given Location, which SetsLocation states for a Created and a redirect states by itself: use SetsLocation", false},
		{"SendsHeader with no description", func(a *bites.App) { here(); a.Get("/x", ok[Empty], bites.SendsHeader[int]("X-Total-Count", " ")) },
			`GET /x: SendsHeader X-Total-Count has no description: say what the header means to a client, as SendsHeader[int]("X-Total-Count", "...")`, false},
		{"SendsHeader with one header twice", func(a *bites.App) { here(); a.Get("/x", ok[Empty], bites.SendsHeader[string]("ETag", "d"), bites.SendsHeader[string]("etag", "d")) },
			"GET /x: SendsHeader is given etag twice: state a header once, on the route or on its group", false},
		{"SendsHeader on a group and again on its route", func(a *bites.App) { here(); a.Group("/g", bites.SendsHeader[string]("ETag", "d")).Get("/x", ok[Empty], bites.SendsHeader[string]("ETag", "e")) },
			"GET /g/x: SendsHeader is given ETag twice: state a header once, on the route or on its group", false},
		{"SendsHeader of a type that is not text", func(a *bites.App) { here(); a.Get("/x", ok[Empty], bites.SendsHeader[[]string]("X-List", "d")) },
			"GET /x: SendsHeader[[]string] is given for X-List, but a header value is text and []string cannot be read as text ([]string is a slice; a slice is filled from repeated keys by the section, so its element type is what decodes from text): use a string, a number, a bool or a named type of one", false},
		{"SendsHeader on a WebSocket route", func(a *bites.App) { here(); a.WebSocket("/x", wsOK, bites.SendsHeader[string]("ETag", "d")) },
			"WebSocket /x: SendsHeader states what a handler's answer sends, but a WebSocket route answers 101 with no body, and its messages are the Socket's type", false},
		{"Example that its own type refuses in a Body", func(a *bites.App) { here(); a.Post("/x", docEcho[struct{ Body struct{ H exBadHandle } }, bites.NoContent]) },
			"POST /x: the API description cannot describe the Body: exBadHandle.Example returns \"ab\", which its own type refuses: must be between 3 and 12 characters: change the value so the type accepts it", false},
		{"Example that its own type refuses in a Query", func(a *bites.App) { here(); a.Get("/x", docEcho[struct{ Query struct{ H exBadHandle } }, bites.NoContent]) },
			"GET /x: the API description cannot describe exBadHandle: exBadHandle.Example returns \"ab\", which its own type refuses: must be between 3 and 12 characters: change the value so the type accepts it", false},
		{"Example that panics", func(a *bites.App) { here(); a.Get("/x", docEcho[struct{ Query struct{ P exPanicky } }, bites.NoContent]) },
			"GET /x: the API description cannot describe exPanicky: exPanicky.Example panicked: no example today", false},
		{"Example that returns another type", func(a *bites.App) { here(); a.Get("/x", docEcho[struct{ Query struct{ W exWrongSig } }, bites.NoContent]) },
			"GET /x: the API description cannot describe exWrongSig: exWrongSig.Example has the signature func() string, but an example is a method that returns the type's own value: write func (exWrongSig) Example() exWrongSig, or rename the method", false},
		{"DeprecatedFields naming a field a Body does not have", func(a *bites.App) { here(); a.Post("/x", docEcho[struct{ Body exStaleDeprecation }, bites.NoContent]) },
			"POST /x: the API description cannot describe the Body: exStaleDeprecation.DeprecatedFields names Nmae, which is not a field of exStaleDeprecation on the wire (its fields: Name)", false},
		{"DeprecatedFields naming a field a Query does not have", func(a *bites.App) { here(); a.Get("/x", docEcho[struct{ Query exStaleSection }, bites.NoContent]) },
			"GET /x: struct { Query bites_test.exStaleSection }.Query.DeprecatedFields names Nmae, which is not a field of exStaleSection (its fields: Name)", false},
		{"SetsLocation given to a group", func(a *bites.App) { here(); a.Group("/g", bites.SetsLocation()) },
			`group "/g": SetsLocation is given to a group, but only some of its routes may return a Created: give it to each route that does`, false},
		{"SetsLocation on a route that cannot return a Created", func(a *bites.App) { here(); a.Get("/x", ok[Empty], bites.SetsLocation()) },
			"GET /x: SetsLocation is given, but the handler returns struct {}, which cannot be a Created: only a Created has a Location the handler may leave empty", false},
		{"SetsLocation on a WebSocket route", func(a *bites.App) { here(); a.WebSocket("/x", wsOK, bites.SetsLocation()) },
			"WebSocket /x: SetsLocation is given to a WebSocket route, which answers 101 and has no Created to carry a Location", false},
		{"SetsLocation on a Static route", func(a *bites.App) {
			a.Static("/s", at(bites.Static{FS: fstest.MapFS{}}), bites.SetsLocation())
		},
			"Static /s: SetsLocation is given to a static route, which answers files and has no Created to carry a Location", false},
		{"a nil option in Options", func(a *bites.App) { here(); a.Get("/x", ok[Empty], bites.Options(bites.Tag("t"), nil)) },
			"GET /x: Options: option 2 is nil", false},
		{"a Summary inside an Options given to a group", func(a *bites.App) { here(); a.Group("/g", bites.Options(bites.Tag("t"), bites.Summary("s"))) },
			`group "/g": Summary is given to a group, but one summary cannot describe every operation in it: give it to each route`, false},
		{"a MaxBody(0) inside an Options", func(a *bites.App) { here(); a.Get("/x", ok[Empty], bites.Options(bites.MaxBody(0))) },
			"GET /x: MaxBody(0) is not a limit: it must be a positive number of bytes", false},
		{"an OperationID given to a group", func(a *bites.App) { here(); a.Group("/g", bites.OperationID("x")) },
			`group "/g": OperationID is given to a group, but an operationId names one operation: give it to each route`, false},
		{"an OperationID a client generator cannot name a method by", func(a *bites.App) { here(); a.Get("/x", ok[Empty], bites.OperationID("list users")) },
			`GET /x: OperationID "list users" is not an operationId a client generator can name a method by: start with a letter and use letters, digits, '_', '.' and '-'`, false},
		{"two OperationIDs a client generator makes one method of", func(a *bites.App) {
			a.Get(atFirst("/a"), ok[Empty], bites.OperationID("listUsers"))
			a.Get(at("/b"), ok[Empty], bites.OperationID("list_users"))
		},
			`GET /b: OperationID "list_users" names the same method of a generated client as the operationId "listUsers" of the route registered at refusal_test.go:%[2]d (given with OperationID): give each operation its own` + "\n\tregistered at refusal_test.go:%[1]d", true},
		{"two routes whose method and path make one operationId", func(a *bites.App) {
			a.Get(atFirst("/x"), ok[Empty])
			a.Get(at("/x/"), ok[Empty])
		},
			`GET /x/: the operationId made from the method and path, "getX", names the same method of a generated client as the operationId "getX" of the route registered at refusal_test.go:%[2]d (made from its method and path): give this route its own with OperationID` + "\n\tregistered at refusal_test.go:%[1]d", true},
		{"an Opt field whose type's Provider reads no credential", func(a *bites.App) {
			a.Provide(atFirst(customWho))
			a.Get(at("/x"), maybeWho)
		},
			"GET /x: MaybeWho.P is not a section (Path, Query, Header, Cookie or Body), and no Provider of Opt[Principal] covers this route; the Provider of Principal (at refusal_test.go:%[2]d) does not read a credential the framework knows, so it has no optional form: make it with BearerAuth, BasicAuth, APIKeyAuth, CustomAuth or AnyAuth, or provide Opt[Principal] itself\n\tregistered at refusal_test.go:%[1]d", true},
		{"a route that needs a credential and the same one as optional", func(a *bites.App) {
			a.Provide(bites.BearerAuth(verifyToken))
			a.Get(at("/x"), ok[BothWho])
		},
			"GET /x: BothWho needs both Principal and Opt[Principal], so the credential is required on this route: ask for Principal", false},
		{"a Tag with no name", func(a *bites.App) { here(); a.Get("/x", ok[Empty], bites.Tag()) },
			"GET /x: Tag is given no name", false},
		{"a Tag with an empty name", func(a *bites.App) { here(); a.Get("/x", ok[Empty], bites.Tag("a", "")) },
			"GET /x: Tag is given an empty name", false},
		{"a body field description that names no field", func(a *bites.App) { here(); a.Post("/x", ok[TypoBodyIn]) },
			"POST /x: the API description cannot describe the Body: typoBody.Descriptions names Nmae, which is not a field of typoBody on the wire (its fields: Name)", false},
		{"a response field description that names no field", func(a *bites.App) { here(); a.Get("/x", typoOutOut) },
			"GET /x: the API description cannot describe the response typoOut: typoOut.Descriptions names Totl, which is not a field of typoOut on the wire (its fields: Total)", false},
		{"a Range that is the whole of its type", func(a *bites.App) { here(); a.Post("/x", ok[WholeRangeBody]) },
			"POST /x: WholeRangeBody.Body.N: bites_test.wholeRange.Range returns the whole of int64 and bounds nothing: drop the method", false},
		{"a Length that is the whole of a string", func(a *bites.App) { here(); a.Post("/x", ok[WholeLengthBody]) },
			"POST /x: WholeLengthBody.Body.S: bites_test.wholeLength.Length returns (0, math.MaxInt), which is the whole of a string and bounds nothing: drop the method", false},
		{"an embedded section whose Descriptions names a field it lacks", func(a *bites.App) { here(); a.Get("/x", ok[EmbeddedTypoIn]) },
			"GET /x: typoQuery.Descriptions names Pag, which is not a field of typoQuery (its fields: Page)", false},
		{"a Defaults entry that names no field", func(a *bites.App) { here(); a.Get("/x", ok[DefTypoIn]) },
			"GET /x: DefTypoIn.Query.Defaults names Lmit, which is not a field of defTypoQuery (its fields: Limit)", false},
		{"a Defaults entry that names no field of an embedded section", func(a *bites.App) { here(); a.Get("/x", ok[DefEmbIn]) },
			"GET /x: DefEmbIn.Query.Defaults names Offst, which is not a field of defEmbQuery (its fields: Limit, Offset)", false},
		{"a default for a path parameter", func(a *bites.App) { here(); a.Get("/x/{id}", ok[DefPathIn]) },
			"GET /x/{id}: DefPathIn.Path.ID has a default, but a path parameter is always present", false},
		{"a default for an Opt", func(a *bites.App) { here(); a.Get("/x", ok[DefOptIn]) },
			"GET /x: DefOptIn.Query.Limit is an Opt and has a default: a default removes the absence an Opt exists to report, so make it a plain int", false},
		{"a default for a list", func(a *bites.App) { here(); a.Get("/x", ok[DefListIn]) },
			"GET /x: DefListIn.Query.Tags is a list and has a default: a list default would be one slice shared by every request or a copy made for each, so a list has none", false},
		{"a default for a comma-separated list", func(a *bites.App) { here(); a.Get("/x", ok[DefCSVIn]) },
			"GET /x: DefCSVIn.Query.Stages is a list and has a default: a list default would be one slice shared by every request or a copy made for each, so a list has none", false},
		{"a default outside its type's Range", func(a *bites.App) { here(); a.Get("/x", ok[DefRangeIn]) },
			"GET /x: DefRangeIn.Query.Limit has the default \"500\", which its own type refuses: expected an integer between 1 and 100", false},
		{"a default that is not one of its enum's Values", func(a *bites.App) { here(); a.Get("/x", ok[DefEnumIn]) },
			"GET /x: DefEnumIn.Query.Sort has the default \"up\", which its own type refuses: expected one of: risk, due, name", false},
		{"a default that is not a value of its type", func(a *bites.App) { here(); a.Get("/x", ok[DefIntIn]) },
			"GET /x: DefIntIn.Query.N has the default \"ten\", which its own type refuses: expected an integer", false},
		{"a Defaults method on a JSON Body", func(a *bites.App) { here(); a.Post("/x", ok[DefBodyIn]) },
			"POST /x: DefBodyIn.Body: defBodyType has a Defaults method, but a JSON Body does not read it: use Opt[T] and Or in the handler, or a Form[T], whose fields are read like a Query", false},
		{"a Defaults method on a type nested in a JSON Body", func(a *bites.App) { here(); a.Post("/x", ok[DefNestedIn]) },
			"POST /x: DefNestedIn.Body: defInnerBody has a Defaults method, but a JSON Body does not read it: use Opt[T] and Or in the handler, or a Form[T], whose fields are read like a Query", false},
		{"a section field description that names no field", func(a *bites.App) { here(); a.Get("/x", ok[TypoQueryIn]) },
			"GET /x: TypoQueryIn.Query.Descriptions names Pag, which is not a field of typoQuery (its fields: Page)", false},
		{"two paths that differ only in their wildcard names", func(a *bites.App) {
			a.Get(atFirst("/u/{id}"), ok[GetUser])
			a.Post(at("/u/{uid}"), ok[GetUser])
		},
			"POST /u/{uid}: the API document would hold /u/{uid} and /u/{id} as two paths, but OpenAPI treats paths that differ only in the names of their wildcards as one: use the same wildcard names in both (the other is registered at refusal_test.go:%[2]d)\n\tregistered at refusal_test.go:%[1]d", true},
		{"two routes the document would hold as one operation", func(a *bites.App) {
			a.Get(atFirst("/x/"), ok[Empty])
			a.Get(at("/x/{$}"), ok[Empty])
		},
			"GET /x/{$}: the API document would hold GET /x/ twice: the route registered at refusal_test.go:%[2]d already describes it\n\tregistered at refusal_test.go:%[1]d", true},
		{"OpenAPI called twice", func(a *bites.App) {
			a.OpenAPI(atFirst("/d"), bites.Info{Title: "t", Version: "1"})
			a.OpenAPI(at("/e"), bites.Info{Title: "t", Version: "1"})
		},
			"OpenAPI /e: the document is already served at /d (registered at refusal_test.go:%[2]d)\n\tregistered at refusal_test.go:%[1]d", true},
		{"OpenAPI at a path with no slash", func(a *bites.App) { here(); a.OpenAPI("d", bites.Info{Title: "t", Version: "1"}) },
			"OpenAPI d: the path must start with a slash and have no wildcard, query or fragment", false},
		{"OpenAPI with no title", func(a *bites.App) { here(); a.OpenAPI("/d", bites.Info{Version: "1"}) },
			"OpenAPI /d: Info.Title is empty: OpenAPI requires a title", false},
		{"OpenAPI with no version", func(a *bites.App) { here(); a.OpenAPI("/d", bites.Info{Title: "t"}) },
			"OpenAPI /d: Info.Version is empty: OpenAPI requires the version of the API (not of OpenAPI)", false},
		{"OpenAPI with a server of no URL", func(a *bites.App) {
			a.OpenAPI(at("/d"), bites.Info{Title: "t", Version: "1", Servers: []bites.Server{{Description: "production"}}})
		},
			`OpenAPI /d: Info.Servers[0].URL "" is empty: give the URL the API is served at, such as https://api.example.com or /api`, false},
		{"OpenAPI with a server URL that ends with a slash", func(a *bites.App) {
			a.OpenAPI(at("/d"), bites.Info{Title: "t", Version: "1", Servers: []bites.Server{{URL: "https://api.example.com/"}}})
		},
			`OpenAPI /d: Info.Servers[0].URL "https://api.example.com/" ends with a slash, and every path of the document begins with one, so a client would ask for //...: drop the trailing slash`, false},
		{"OpenAPI with a server URL of the root path", func(a *bites.App) {
			a.OpenAPI(at("/d"), bites.Info{Title: "t", Version: "1", Servers: []bites.Server{{URL: "/"}}})
		},
			`OpenAPI /d: Info.Servers[0].URL "/" is "/", which OpenAPI reads when servers is absent: leave Servers out`, false},
		{"OpenAPI with a server variable", func(a *bites.App) {
			a.OpenAPI(at("/d"), bites.Info{Title: "t", Version: "1", Servers: []bites.Server{{URL: "https://{region}.example.com"}}})
		},
			`OpenAPI /d: Info.Servers[0].URL "https://{region}.example.com" has a server variable, which is not supported: write the URL in full`, false},
		{"OpenAPI with a server URL with a query", func(a *bites.App) {
			a.OpenAPI(at("/d"), bites.Info{Title: "t", Version: "1", Servers: []bites.Server{{URL: "https://api.example.com?v=1"}}})
		},
			`OpenAPI /d: Info.Servers[0].URL "https://api.example.com?v=1" has a query or a fragment: a server URL is scheme://host[:port][/path] or /path`, false},
		{"OpenAPI with a server URL that is not http or https", func(a *bites.App) {
			a.OpenAPI(at("/d"), bites.Info{Title: "t", Version: "1", Servers: []bites.Server{{URL: "api.example.com/v1"}}})
		},
			`OpenAPI /d: Info.Servers[0].URL "api.example.com/v1" is neither an http or https URL nor a path that starts with a slash`, false},
		{"OpenAPI with a server URL with a userinfo", func(a *bites.App) {
			a.OpenAPI(at("/d"), bites.Info{Title: "t", Version: "1", Servers: []bites.Server{{URL: "https://ada:pw@api.example.com"}}})
		},
			`OpenAPI /d: Info.Servers[0].URL "https://ada:pw@api.example.com" has a host that is not a host name, an IPv4 address or a bracketed IPv6 address with an optional port (a userinfo or a space is refused)`, false},
		{"OpenAPI with a server path that climbs out", func(a *bites.App) {
			a.OpenAPI(at("/d"), bites.Info{Title: "t", Version: "1", Servers: []bites.Server{{URL: "/api/../admin"}}})
		},
			`OpenAPI /d: Info.Servers[0].URL "/api/../admin" has a path that is not an absolute path of RFC 3986 path characters with no empty, "." or ".." segment`, false},
		{"OpenAPI with a server URL given twice", func(a *bites.App) {
			a.OpenAPI(at("/d"), bites.Info{Title: "t", Version: "1", Servers: []bites.Server{{URL: "/api"}, {URL: "/api", Description: "again"}}})
		},
			`OpenAPI /d: Info.Servers[1].URL "/api" is given twice (also Servers[0])`, false},
		{"a schema namer that gives two types one name", func(a *bites.App) {
			a.OpenAPI(at("/d"), bites.Info{Title: "t", Version: "1", SchemaName: func(_, _ string) string { return "Same" }})
			a.Get("/a", outOf[docAdmin])
			a.Get("/b", outOf[docMsg])
			bites.StartForTest(a)
		},
			`OpenAPI /d: the API document cannot be built: openapi: the schema namer gave github.com/nevindra/bites_test.docAdmin and github.com/nevindra/bites_test.docMsg the same name "Same", but each component needs its own`, false},
		{"a schema namer name with a character a component name cannot have", func(a *bites.App) {
			a.OpenAPI(at("/d"), bites.Info{Title: "t", Version: "1", SchemaName: func(_, _ string) string { return "Commitment List" }})
			a.Get("/a", outOf[docAdmin])
			bites.StartForTest(a)
		},
			`OpenAPI /d: the API document cannot be built: openapi: the schema namer named github.com/nevindra/bites_test.docAdmin "Commitment List", but a component name may use only A-Z a-z 0-9 . - and _`, false},
		{"a schema namer name that another type has by default", func(a *bites.App) {
			a.OpenAPI(at("/d"), bites.Info{Title: "t", Version: "1", SchemaName: func(_, n string) string {
				if n == "docAdmin" {
					return "docMsg"
				}
				return ""
			}})
			a.Get("/a", outOf[docAdmin])
			a.Get("/b", outOf[docMsg])
			bites.StartForTest(a)
		},
			`OpenAPI /d: the API document cannot be built: openapi: the schema namer named github.com/nevindra/bites_test.docAdmin "docMsg", which is the name github.com/nevindra/bites_test.docMsg has without it: name both or neither`, false},
		{"a Service with no name is refused", func(a *bites.App) { here(); a.Service(bites.Service{Stop: svcNoop}) },
			"app: Service has no name: set Service.Name, the word the log and the errors call this service by", false},
		{"a Service whose name has a space is refused", func(a *bites.App) { here(); a.Service(bites.Service{Name: "order db", Stop: svcNoop}) },
			"app: Service has no name: set Service.Name, the word the log and the errors call this service by", false},
		{"a Service whose name has a quote is refused", func(a *bites.App) { here(); a.Service(bites.Service{Name: `a"b`, Stop: svcNoop}) },
			"app: Service has no name: set Service.Name, the word the log and the errors call this service by", false},
		{"a Service with no function is refused", func(a *bites.App) { here(); a.Service(bites.Service{Name: "queue"}) },
			`app: Service "queue" has no Start, BeforeShutdown or Stop: a service with nothing to run is a mistake; set at least one of them`, false},
		{"a Service registered twice is refused naming both places", func(a *bites.App) {
			a.Service(atFirst(bites.Service{Name: "queue", Stop: svcNoop}))
			a.Service(at(bites.Service{Name: "queue", Stop: svcNoop}))
		},
			`app: Service "queue" is registered twice, first at refusal_test.go:%[2]d: a name is one service; give the second its own name` + "\n\tregistered at refusal_test.go:%[1]d", true},
		{"a Service registered after the app started serving is refused", func(a *bites.App) {
			bites.StartForTest(a)
			here(); a.Service(bites.Service{Name: "queue", Stop: svcNoop})
		},
			`app: Service "queue" registered at refusal_test.go:%d after the app started serving: services can be added only before it does`, true},
		{"WithServiceTimeout of zero is refused", func(*bites.App) { here(); bites.New(bites.WithServiceTimeout(0)) },
			"app: WithServiceTimeout(0) is not a time to wait: give a positive duration, or leave it out for 30s", false},
		{"a negative WithServiceTimeout is refused", func(*bites.App) { here(); bites.New(bites.WithServiceTimeout(-time.Second)) },
			"app: WithServiceTimeout(-1s) is not a time to wait: give a positive duration, or leave it out for 30s", false},
		{"WithServiceTimeout given twice is refused", func(*bites.App) {
			bites.New(bites.WithServiceTimeout(time.Second), bites.WithServiceTimeout(at(2*time.Second)))
		},
			"app: WithServiceTimeout is given twice", false},
		{"a Worker with no name is refused", func(*bites.App) { here(); bites.Worker("", svcNoop) },
			`bites.Worker: the name is empty or has a space, a quote or a control character: give the word the log and the errors call this worker by, such as "mail-sender"`, false},
		{"a Worker whose name has a space is refused", func(*bites.App) { here(); bites.Worker("mail sender", svcNoop) },
			`bites.Worker: the name is empty or has a space, a quote or a control character: give the word the log and the errors call this worker by, such as "mail-sender"`, false},
		{"a Worker with a nil run is refused", func(*bites.App) { here(); bites.Worker("mail", nil) },
			`bites.Worker("mail"): run is nil: give the function the worker runs until its ctx is cancelled`, false},
		{"an Every with no name is refused", func(*bites.App) { here(); bites.Every("", time.Minute, svcNoop) },
			`bites.Every: the name is empty or has a space, a quote or a control character: give the word the log and the errors call this job by, such as "purge-deleted"`, false},
		{"an Every with a nil fn is refused", func(*bites.App) { here(); bites.Every("purge", time.Minute, nil) },
			`bites.Every("purge"): fn is nil: give the function called once every interval`, false},
		{"an Every with an interval of zero is refused", func(*bites.App) { here(); bites.Every("purge", 0, svcNoop) },
			`bites.Every("purge"): the interval 0 is not a time between calls: give a positive duration, such as time.Minute`, false},
		{"an Every with a negative interval is refused", func(*bites.App) { here(); bites.Every("purge", -time.Second, svcNoop) },
			`bites.Every("purge"): the interval -1s is not a time between calls: give a positive duration, such as time.Minute`, false},
		{"an Every given two options values is refused", func(*bites.App) {
			here(); bites.Every("purge", time.Minute, svcNoop, bites.EveryOptions{}, bites.EveryOptions{AtStart: true})
		},
			`bites.Every("purge"): 2 options values were given; give one EveryOptions`, false},
		{"WithGoAwaySpread of zero is refused", func(*bites.App) { here(); bites.New(bites.WithGoAwaySpread(0)) },
			"WithGoAwaySpread: the window is 0s, which is not a window: give a positive duration such as 10*time.Second, or leave the option out for the default of 3s", false},
		{"a negative WithGoAwaySpread is refused", func(*bites.App) { here(); bites.New(bites.WithGoAwaySpread(-time.Second)) },
			"WithGoAwaySpread: the window is -1s, which is not a window: give a positive duration such as 10*time.Second, or leave the option out for the default of 3s", false},
		{"WithGoAwaySpread given twice is refused naming both places", func(*bites.App) {
			bites.New(bites.WithGoAwaySpread(atBoth(time.Second)), bites.WithGoAwaySpread(2*time.Second))
		},
			"WithGoAwaySpread: given twice (first at refusal_test.go:%d, again at refusal_test.go:%d): give one window", true},
		{"two types the document cannot name apart", func(a *bites.App) {
			a.OpenAPI(at("/d"), bites.Info{Title: "t", Version: "1"})
			registerTwins(a)
			bites.StartForTest(a)
		},
			`OpenAPI /d: the API document cannot be built: openapi: two different types are both named github.com.nevindra.bites_test.Twin in package "github.com/nevindra/bites_test", so they cannot get distinct component names; give one of them another name`, false},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			a := bites.New(bites.WithClock(bitetest.NewClock()))
			got := recoverMessage(func() { row.reg(a) })
			if got == "" {
				t.Fatal("registered without a panic")
			}
			want := row.want
			switch {
			case !row.full:
				want += fmt.Sprintf("\n\tregistered at refusal_test.go:%d", hereLine)
			case strings.Count(want, "%d") == 2 || strings.Contains(want, "%[2]d"):
				want = fmt.Sprintf(want, hereLine, firstLine)
			default:
				want = fmt.Sprintf(want, hereLine)
			}
			if got != want {
				t.Errorf("\n got  %q\n want %q", got, want)
			}
		})
	}
}

// tlsWith is a config with a certificate source and the given NextProtos; the refusals fire
// before the certificate is looked at.
func tlsWith(protos ...string) *tls.Config {
	return &tls.Config{Certificates: []tls.Certificate{{}}, NextProtos: protos}
}

// tlsVerifying is a config that verifies client certificates and names no ClientCAs.
func tlsVerifying(auth tls.ClientAuthType) *tls.Config {
	return &tls.Config{Certificates: []tls.Certificate{{}}, ClientAuth: auth}
}

func recoverMessage(f func()) (msg string) {
	defer func() {
		if v := recover(); v != nil {
			if err, ok := v.(error); ok {
				msg = err.Error()
			} else {
				msg = fmt.Sprint(v)
			}
		}
	}()
	f()
	return ""
}

var _ = errors.As
var _ = strings.TrimSpace

// coding is an Encoder that only names a coding, for the refusals of WithCompression.
type coding string

func (c coding) Coding() string                { return string(c) }
func (c coding) Encode(dst, src []byte) []byte { return append(dst, src...) }

// stamp stands for pgx's pgtype.Timestamptz: it writes and reads its own JSON, can write null, and
// has no text form, so the codec delegates to it and the API document cannot see inside it.
type stamp struct {
	at    time.Time
	valid bool
}

func (s stamp) MarshalJSON() ([]byte, error) {
	if !s.valid {
		return []byte("null"), nil
	}
	return s.at.MarshalJSON()
}

func (s *stamp) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*s = stamp{}
		return nil
	}
	s.valid = true
	return s.at.UnmarshalJSON(b)
}

// stampText has a text form and a JSON form, so the JSON methods win and it is stamp's case.
type stampText struct{ stamp }

func (s stampText) MarshalText() ([]byte, error) { return s.at.MarshalText() }
func (s *stampText) UnmarshalText(b []byte) error { s.valid = true; return s.at.UnmarshalText(b) }

func out[Out any](context.Context, Empty) (Out, error) { var o Out; return o, nil }

type foreignText [4]byte

func (f foreignText) MarshalText() ([]byte, error) { return f[:], nil }
func (f *foreignText) UnmarshalText(b []byte) error { copy(f[:], b); return nil }

type writeOnlyText [4]byte

func (f writeOnlyText) MarshalText() ([]byte, error) { return f[:], nil }

type readOnlyText [4]byte

func (f *readOnlyText) UnmarshalText(b []byte) error { copy(f[:], b); return nil }

type hiddenLine struct {
	Qty  int
	note string
}

type idPathSection struct{ Path struct{ ID int } }

type mixedSections struct {
	Path struct{ ID int }
	Me   string
}

type joinVariant struct{ Room int64 }
type typedVariant struct{ Type string }

type plainVariantFrame struct {
	Join bites.Opt[joinVariant]
	Say  string
}

func (plainVariantFrame) Discriminator() string { return "type" }

type clashingFrame struct {
	Join  bites.Opt[joinVariant]
	Typed bites.Opt[typedVariant]
}

func (clashingFrame) Discriminator() string { return "type" }

type (
	strayServerOnlyFrame struct {
		Join bites.Opt[joinVariant]
		Ref  bites.Opt[typedRef]
	}
	allServerOnlyFrame   strayServerOnlyFrame
	twiceServerOnlyFrame strayServerOnlyFrame
	ptrServerOnlyFrame   strayServerOnlyFrame
	typedRef             struct{ ID string }
)

func (strayServerOnlyFrame) Discriminator() string { return "type" }
func (strayServerOnlyFrame) ServerOnly() []string  { return []string{"Rfe"} }

func (allServerOnlyFrame) Discriminator() string { return "type" }
func (allServerOnlyFrame) ServerOnly() []string  { return []string{"Join", "Ref"} }

func (twiceServerOnlyFrame) Discriminator() string { return "type" }
func (twiceServerOnlyFrame) ServerOnly() []string  { return []string{"Ref", "Ref"} }

func (ptrServerOnlyFrame) Discriminator() string { return "type" }
func (*ptrServerOnlyFrame) ServerOnly() []string { return []string{"Ref"} }


// The types and Inputs of the refusals of ADR 0021's exclusive bounds, MultipleOf, Unique and Members.
type (
	exMinOnInt      int
	exBothMin       float64
	exBothMax       float64
	exEmpty         float64
	exclWrongSig    float64
	exOnString      string
	exAtLimit       float64
	exNaN           float64
	mulZeroInt      int
	mulOneInt       int
	mulNegFloat     float64
	mulOnString     string
	mulOnInt32      int32
	mulOnBool       bool
	mulNoneInRange  int
	mulWrongSig     int
	mulFloatNoneIn  float64
	mulOnSlice      []string
	uniqueFalse     []string
	uniqueOnMap     map[string]string
	uniqueOnString  string
	uniqueOfStructs []struct{ A string }
	uniqueWrongSig  []string
	uniqueOnBytes   []byte
	uniqueOnInt     int
	membersOnList   []string
	membersOnString string
	membersBad      map[string]string
	itemsOnMap      map[string]string
	membersWrongSig map[string]string
	membersOnInt    int
)

func (exMinOnInt) ExclusiveMin() float64 { return 0 }
func (exBothMin) Range() (min, max float64) {
	return 1, 10
}
func (exBothMin) ExclusiveMin() float64 { return 0 }
func (exBothMax) Range() (min, max float64) {
	return -math.MaxFloat64, 10
}
func (exBothMax) ExclusiveMax() float64 { return 5 }
func (exEmpty) ExclusiveMin() float64   { return 5 }
func (exEmpty) ExclusiveMax() float64   { return 5 }
func (exclWrongSig) ExclusiveMin() int    { return 0 }
func (exOnString) ExclusiveMax() float64 {
	return 5
}
func (exAtLimit) ExclusiveMin() float64 { return -math.MaxFloat64 }
func (exNaN) ExclusiveMax() float64     { return math.NaN() }

func (mulZeroInt) MultipleOf() int64    { return 0 }
func (mulOneInt) MultipleOf() int64     { return 1 }
func (mulNegFloat) MultipleOf() float64 { return -0.5 }
func (mulOnString) MultipleOf() int64   { return 3 }
func (mulOnInt32) MultipleOf() int64    { return 3 }
func (mulOnBool) MultipleOf() int64     { return 3 }
func (mulNoneInRange) Range() (min, max int64) {
	return 6, 9
}
func (mulNoneInRange) MultipleOf() int64 { return 5 }
func (mulWrongSig) MultipleOf() int      { return 5 }
func (mulFloatNoneIn) Range() (min, max float64) {
	return 0.011, 0.019
}
func (mulFloatNoneIn) MultipleOf() float64 { return 0.01 }
func (mulOnSlice) MultipleOf() float64     { return 0.5 }

func (uniqueFalse) Unique() bool      { return false }
func (uniqueOnMap) Unique() bool      { return true }
func (uniqueOnString) Unique() bool   { return true }
func (uniqueOfStructs) Unique() bool  { return true }
func (uniqueWrongSig) Unique() string { return "yes" }
func (uniqueOnBytes) Unique() bool    { return true }
func (uniqueOnInt) Unique() bool      { return true }

func (membersOnList) Members() (int, int)   { return 1, 2 }
func (membersOnString) Members() (int, int) { return 1, 2 }
func (membersBad) Members() (int, int)      { return 3, 1 }
func (itemsOnMap) Items() (int, int)        { return 1, 2 }
func (membersWrongSig) Members() int        { return 2 }
func (membersOnInt) Members() (int, int)    { return 1, 2 }

type (
	ExMinOnIntBody      struct{ Body struct{ N exMinOnInt } }
	ExBothMinBody       struct{ Body struct{ N exBothMin } }
	ExBothMaxBody       struct{ Body struct{ N exBothMax } }
	ExEmptyBody         struct{ Body struct{ N exEmpty } }
	ExWrongSigBody      struct{ Body struct{ N exclWrongSig } }
	ExOnStringBody      struct{ Body struct{ N exOnString } }
	ExAtLimitBody       struct{ Body struct{ N exAtLimit } }
	ExNaNBody           struct{ Body struct{ N exNaN } }
	MulZeroIntBody      struct{ Body struct{ N mulZeroInt } }
	MulOneIntBody       struct{ Body struct{ N mulOneInt } }
	MulNegFloatBody     struct{ Body struct{ N mulNegFloat } }
	MulOnStringBody     struct{ Body struct{ N mulOnString } }
	MulOnInt32Body      struct{ Body struct{ N mulOnInt32 } }
	MulOnBoolBody       struct{ Body struct{ N mulOnBool } }
	MulNoneInRangeBody  struct{ Body struct{ N mulNoneInRange } }
	MulWrongSigBody     struct{ Body struct{ N mulWrongSig } }
	MulFloatNoneInBody  struct{ Body struct{ N mulFloatNoneIn } }
	MulOnSliceBody      struct{ Body struct{ N mulOnSlice } }
	UniqueFalseBody     struct{ Body struct{ L uniqueFalse } }
	UniqueOnMapBody     struct{ Body struct{ M uniqueOnMap } }
	UniqueOnStringBody  struct{ Body struct{ S uniqueOnString } }
	UniqueOfStructsBody struct{ Body struct{ L uniqueOfStructs } }
	UniqueWrongSigBody  struct{ Body struct{ L uniqueWrongSig } }
	UniqueOnBytesBody   struct{ Body struct{ B uniqueOnBytes } }
	UniqueOnIntBody     struct{ Body struct{ N uniqueOnInt } }
	UniqueFalseQuery    struct{ Query struct{ L uniqueFalse } }
	MembersOnListBody   struct{ Body struct{ L membersOnList } }
	MembersOnStringBody struct{ Body struct{ S membersOnString } }
	MembersBadBody      struct{ Body struct{ M membersBad } }
	ItemsOnMapBody      struct{ Body struct{ M itemsOnMap } }
	MembersWrongSigBody struct{ Body struct{ M membersWrongSig } }
	MembersOnIntBody    struct{ Body struct{ N membersOnInt } }
	TagMultipleOf       struct {
		Body struct {
			N float64 `multipleOf:"0.5"`
		}
	}
	TagUniqueItems struct {
		Body struct {
			L []string `uniqueItems:"true"`
		}
	}
	TagMinProperties struct {
		Body struct {
			M map[string]string `minProperties:"1"`
		}
	}
)

// clashingMembers has a field written as "title", which the problem body already has.
type clashingMembers struct{ Title string }
