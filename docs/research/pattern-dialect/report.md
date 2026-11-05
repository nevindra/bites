# Pattern dialect: where Go's regexp (RE2) and ECMA-262 read an expression differently

The question: `Pattern() string` on a string type is compiled by the server with Go's `regexp` and stated in the API document as JSON Schema `pattern`, which a client reads as an ECMA-262 regular expression. Which RE2 constructs are invalid in ECMA-262, or mean something else, so that Registration must refuse them? ([ADR 0021](../../adr/0021-validation-is-a-method-and-a-few-constraint-types.md))

## What was run

`go run .` in this folder (its own module, `main.go`). It compiles 70 candidate expressions with `regexp.Compile` and, in node, with `new RegExp(p, 'u')` (the `u` flag is the mode JSON Schema validators and OpenAPI tooling use for `pattern`), then, for every candidate that compiles in both, matches a few sample strings in both and reports a difference. It also counts the code points of every Unicode general category in both engines.

- Machine: Apple M1 Pro, macOS (Darwin 25.6.0, arm64). Not a pinned-core measurement; nothing here is a timing.
- Go go1.27.0, node v26.8.2 (V8). node was on PATH and was run; the ECMA-262 column is measured, not argued from the spec.
- Commit: f22f667eb757dcf88ad28a46b4924720b7668990, with the working tree of this change on top.
- A single run: the results are deterministic, so there is no spread.

## Result

```
construct                    Go     ECMA   result
plain class, anchors         ok     ok     same syntax
\d, counted repeat           ok     ok     same syntax
\w                           ok     ok     same syntax
\b                           ok     ok     same syntax
non-capturing group          ok     ok     same syntax
capturing group              ok     ok     same syntax
lazy                         ok     ok     same syntax
\p{L}                        ok     ok     same syntax
\p{Lu}                       ok     ok     same syntax
\p{Greek} (script name)      ok     error  Go only: Invalid regular expression: /^\p{Greek}+$/u: Invalid property name
\p{Script=Greek}             error  ok     ECMA only
\pL without braces           ok     error  Go only: Invalid regular expression: /^\pL+$/u: Invalid property name
\p{C} (Other)                ok     ok     same syntax
(?P<name>…)                  ok     error  Go only: Invalid regular expression: /(?P<year>\d{4})/u: Invalid group
(?<name>…) (Go 1.22+)        ok     ok     same syntax
(?i) flag                    ok     error  Go only: Invalid regular expression: /(?i)abc/u: Invalid group
(?i:…) scoped flag           ok     ok     same syntax
(?s) flag                    ok     error  Go only: Invalid regular expression: /(?s)a.b/u: Invalid group
(?m) flag                    ok     error  Go only: Invalid regular expression: /(?m)^a$/u: Invalid group
(?U) flag                    ok     error  Go only: Invalid regular expression: /(?U)a+/u: Invalid group
\A and \z                    ok     error  Go only: Invalid regular expression: /\Aabc\z/u: Invalid escape
\A                           ok     error  Go only: Invalid regular expression: /\Aabc/u: Invalid escape
\z                           ok     error  Go only: Invalid regular expression: /abc\z/u: Invalid escape
\Q…\E                        ok     error  Go only: Invalid regular expression: /\Qa.b\E/u: Invalid escape
POSIX class                  ok     error  Go only: Invalid regular expression: /[[:alpha:]]+/u: Lone quantifier brackets
negated POSIX class          ok     error  Go only: Invalid regular expression: /[^[:digit:]]/u: Lone quantifier brackets
\C one byte                  error  error  neither
\xHH                         ok     ok     same syntax
\x{HHHH}                     ok     error  Go only: Invalid regular expression: /\x{41}/u: Invalid escape
\uHHHH                       error  ok     ECMA only
\a bell                      ok     error  Go only: Invalid regular expression: /\a/u: Invalid escape
control escapes              ok     ok     same syntax
escaped underscore           ok     error  Go only: Invalid regular expression: /\_/u: Invalid escape
\- outside a class           ok     error  Go only: Invalid regular expression: /a\-b/u: Invalid escape
\- inside a class            ok     ok     same syntax
escaped space                ok     error  Go only: Invalid regular expression: /\ /u: Invalid escape
escaped quote                ok     error  Go only: Invalid regular expression: /\"/u: Invalid escape
escaped slash                ok     ok     same syntax
\0                           ok     ok     same syntax
backreference                error  ok     ECMA only
lookahead                    error  ok     ECMA only
lookbehind                   error  ok     ECMA only
\s                           ok     ok     DIFFERENT MEANING: "\u00a0" Go=false ECMA=true
\S                           ok     ok     DIFFERENT MEANING: "\u00a0" Go=true ECMA=false
. (dot)                      ok     ok     DIFFERENT MEANING: "\r" Go=true ECMA=false
$ without m                  ok     ok     same syntax
lone {                       ok     error  Go only: Invalid regular expression: /a{/u: Incomplete quantifier
{ not a count                ok     error  Go only: Invalid regular expression: /a{x}/u: Incomplete quantifier
lone }                       ok     error  Go only: Invalid regular expression: /a}/u: Lone quantifier brackets
lone ]                       ok     error  Go only: Invalid regular expression: /a]/u: Lone quantifier brackets
] first in a class           ok     error  Go only: Invalid regular expression: /[]a]/u: Lone quantifier brackets
] first in a negated class   ok     error  Go only: Invalid regular expression: /[^]a]/u: Lone quantifier brackets
trailing - in a class        ok     ok     same syntax
\b in a class                error  ok     ECMA only
quantified ^                 ok     error  Go only: Invalid regular expression: /^*/u: Nothing to repeat
quantified $                 ok     error  Go only: Invalid regular expression: /$*/u: Nothing to repeat
quantified \b                ok     error  Go only: Invalid regular expression: /\b+/u: Nothing to repeat
nested repeat                error  error  neither
repeat over 1000             error  ok     ECMA only
inverted repeat              error  error  neither
unterminated group           error  error  neither
unterminated class           error  error  neither
empty alternative            ok     ok     same syntax
empty group                  ok     ok     same syntax
class escape as range start  ok     error  Go only: Invalid regular expression: /[\d-x]/u: Invalid character class
non-ASCII range              ok     ok     same syntax
astral literal               ok     ok     same syntax
astral in a class            ok     ok     same syntax
empty non-capturing group    ok     ok     same syntax
\x{…} astral                 ok     error  Go only: Invalid regular expression: /^\x{1F600}$/u: Invalid escape

general category code point counts (surrogates skipped), Go vs node:
  L   Go=145672  node=145672  same
  Lu  Go=1886    node=1886    same
  Ll  Go=2283    node=2283    same
  Lt  Go=31      node=31      same
  Lm  Go=410     node=410     same
  Lo  Go=141062  node=141062  same
  M   Go=2543    node=2543    same
  Mn  Go=2059    node=2059    same
  Mc  Go=471     node=471     same
  Me  Go=13      node=13      same
  N   Go=1924    node=1924    same
  Nd  Go=770     node=770     same
  Nl  Go=239     node=239     same
  No  Go=915     node=915     same
  P   Go=856     node=856     same
  Pc  Go=10      node=10      same
  Pd  Go=27      node=27      same
  Ps  Go=79      node=79      same
  Pe  Go=77      node=77      same
  Pi  Go=12      node=12      same
  Pf  Go=10      node=10      same
  Po  Go=641     node=641     same
  S   Go=8617    node=8617    same
  Sm  Go=960     node=960     same
  Sc  Go=64      node=64      same
  Sk  Go=125     node=125     same
  So  Go=7468    node=7468    same
  Z   Go=19      node=19      same
  Zs  Go=17      node=17      same
  Zl  Go=1       node=1       same
  Zp  Go=1       node=1       same
  C   Go=952433  node=952433  same
  Cc  Go=65      node=65      same
  Cf  Go=170     node=170     same
  Cs  Go=0       node=0       same
  Co  Go=137468  node=137468  same
```

## Reading it

Three kinds of difference, and Registration refuses all three (`internal/wire/pattern.go`, `dialectRefusal`):

- **Compiles in Go, refused by ECMA-262 with the u flag:** `(?P<name>...)`, the flag groups `(?i)` `(?s)` `(?m)` `(?U)`, `\A`, `\z`, `\Q...\E`, `\x{HHHH}`, `\a`, a POSIX class `[[:alpha:]]`, `\pL` without braces, `\p{Greek}` (a script is `\p{Script=Greek}` in ECMA-262, which Go refuses), `\_`, `\-` outside a class, `\ ` and `\"` (ECMA-262 allows only syntax characters after a backslash), a lone `{`, `}` or `]` (literals in RE2), a `]` first in a class, a quantified `^`, `$` or `\b`, and a class escape as a range endpoint (`[\d-x]`). A client that follows the document cannot even compile these.
- **Compiles in both and means something else:** `\s` and `\S` (RE2: `[\t\n\f\r ]`; ECMA-262 also U+000B, U+00A0, U+FEFF and the Unicode spaces), and `.` (RE2 matches `\r`, U+2028 and U+2029; ECMA-262 does not). A value with a no-break space or a carriage return passes in one engine and fails in the other.
- **Compiles in both and is not portable in time:** the scoped flag `(?i:...)` compiles in node 26 (regular expression modifiers) but not in an older engine, so it is refused with the other flag groups. `(?<name>...)` compiles in both (Go 1.22 and later) and is allowed. `\1` is a backreference in ECMA-262 and an error in Go; `\0` compiles in both, but digits after a backslash are refused altogether.

Not an issue, found equal: `\d`, `\w`, `\b` (ASCII in both, with the u flag and no `i`), `$` without the `m` flag (end of input in both), lazy quantifiers, `(?:...)`, `{n,m}`, empty alternatives, astral literals and ranges in a class, `\xHH`, `\t \n \r \f \v`, and `\p{X}` of a general category: the 36 categories have the same number of code points in Go 1.27 and node 26, so Unicode versions agree on this pair. A different engine or version could still differ, which is a claim this probe does not cover. Go-only and not in the table: `\C` is an error in Go 1.27, lookaround and backreferences are errors in Go and are valid in ECMA-262 (so a document could state a pattern the server cannot compile, which Registration already refuses because the server compiles it).

Matching is unanchored in both engines (`regexp.MatchString` and `RegExp.test`), which is why `Pattern` does not add anchors and the author writes `^` and `$`.

## Not covered

A different ECMA-262 engine (Safari's JavaScriptCore, Firefox's SpiderMonkey) was not run; the spec says the same for the constructs that failed here, but that is an argument and not a run. The u flag was assumed; a client that compiles `pattern` without it (the older default of JSON Schema drafts) reads some of the accepted escapes (`\p{L}`) as literals.
