package wire

import (
	"encoding/json"
	"os/exec"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

type pSlug string

func (pSlug) Pattern() string { return `^[a-z0-9-]+$` }

type pOwn string

func (pOwn) Pattern(layout string) string { return layout }

type pInt int32

func (pInt) Pattern() string { return `a` }

type pTime struct{}

func (pTime) Pattern(string) bool { return false }

func TestAPatternMethodOnAStringTypeIsCompiledOnceAndTheTypeBecomesAPatternConstraint(t *testing.T) {
	c, err := ConstraintOf(reflect.TypeFor[pSlug]())
	if err != nil || c.Kind != ConPattern || c.Rule != RulePattern || c.Pat == nil || c.Pat.Src != `^[a-z0-9-]+$` {
		t.Fatalf("constraint %+v, err %v", c, err)
	}
	if !c.OKString("a-1") || c.OKString("A") || c.OKString("") {
		t.Fatalf("the match is wrong for a-1, A or empty")
	}
}

func TestAMethodNamedPatternWithAnotherMeaningOnAnotherTypeIsTheDomainsOwn(t *testing.T) {
	if p, err := PatternOf(reflect.TypeFor[pTime]()); p != nil || err != nil {
		t.Fatalf("got %v, %v", p, err)
	}
	if _, err := PatternOf(reflect.TypeFor[pOwn]()); err == nil || !strings.Contains(err.Error(), "must be func() string") {
		t.Fatalf("got %v", err)
	}
	if _, err := PatternOf(reflect.TypeFor[pInt]()); err == nil || !strings.Contains(err.Error(), "read on a string type") {
		t.Fatalf("got %v", err)
	}
}

// inSubset are patterns the dialect check accepts, with values that tell their meaning.
var inSubset = []struct {
	p       string
	samples []string
}{
	{`^[a-z0-9]+$`, []string{"abc1", "ABC", ""}},
	{`^\d{3}-\d{4}$`, []string{"555-1234", "٣٣٣-1234"}},
	{`^\w+$`, []string{"abc_1", "é"}},
	{`\bfoo\b`, []string{"a foo b", "éfoo"}},
	{`(?:ab)+|(cd){2,}`, []string{"abab", "cdcd", "ba"}},
	{`(?<year>\d{4})-\d\d`, []string{"2024-01", "24-01"}},
	{`^a+?b*?c{2}?$`, []string{"aacc", "ac"}},
	{`^\p{L}+$`, []string{"héllo", "a1"}},
	{`^\p{Lu}\P{L}*$`, []string{"A12", "a"}},
	{`^[\p{L}\d_.\-]+@[^\x00-\x2f]+$`, []string{"a.b@c", "a@/"}},
	{`^[\t\n\r\f\v ]*$`, []string{" \t", " "}},
	{`\/\.\*\+\?\(\)\[\]\{\}\|\\\^\$`, []string{`/.*+?()[]{}|\^$`}},
	{`^[^\]\\]+$`, []string{"ab", "a]"}},
	{`^[a-z\d-]{2,}$`, []string{"a-1", "-"}},
	{`^(|a)$`, []string{"", "a", "b"}},
	{`😀+`, []string{"😀😀"}},
}

func TestEveryPatternTheDialectCheckAcceptsCompilesInEcmaScriptAndMatchesAsGoDoes(t *testing.T) {
	type cand struct {
		P       string   `json:"p"`
		Samples []string `json:"samples"`
	}
	var cands []cand
	for _, c := range inSubset {
		if why := dialectRefusal(c.p); why != "" {
			t.Errorf("%q is refused: %s", c.p, why)
		}
		cands = append(cands, cand{c.p, c.samples})
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH: the ECMA-262 side is not run (docs/research/pattern-dialect runs it)")
	}
	in, _ := json.Marshal(cands)
	cmd := exec.Command(node, "-e", `const cs=JSON.parse(require('fs').readFileSync(0,'utf8'));
process.stdout.write(JSON.stringify(cs.map(c=>{try{const re=new RegExp(c.p,'u');return {ok:true,m:c.samples.map(s=>re.test(s))}}catch(e){return {ok:false,err:String(e.message)}}})))`)
	cmd.Stdin = strings.NewReader(string(in))
	out, err := cmd.Output()
	if err != nil {
		t.Skip("node failed to run:", err)
	}
	var res []struct {
		OK  bool
		M   []bool
		Err string
	}
	if err := json.Unmarshal(out, &res); err != nil || len(res) != len(inSubset) {
		t.Fatalf("node answered %s", out)
	}
	for i, c := range inSubset {
		if !res[i].OK {
			t.Errorf("%q does not compile in ECMA-262: %s", c.p, res[i].Err)
			continue
		}
		re := regexp.MustCompile(c.p)
		for j, s := range c.samples {
			if re.MatchString(s) != res[i].M[j] {
				t.Errorf("%q on %q: Go %v, ECMA-262 %v", c.p, s, re.MatchString(s), res[i].M[j])
			}
		}
	}
}

func TestAPatternOutsideTheCommonSubsetIsRefusedWithTheConstructNamed(t *testing.T) {
	for _, c := range []struct{ p, want string }{
		{`(?i)a`, "flag or lookaround group (?i"},
		{`(?i:a)`, "flag or lookaround group (?i"},
		{`(?s)a`, "flag or lookaround group (?s"},
		{`(?m)^a`, "flag or lookaround group (?m"},
		{`(?U)a+`, "flag or lookaround group (?U"},
		{`(?P<n>a)`, "(?P<name>...) group"},
		{`(?<=a)b`, "group name"},
		{`\Aa`, `the escape \A`},
		{`a\z`, `the escape \z`},
		{`\Qa.b\E`, `the escape \Q`},
		{`\C`, `the escape \C`},
		{`\a`, `the escape \a`},
		{`\_`, `the escape \_`},
		{`a\-b`, `the escape \-`},
		{`\1`, "backreference"},
		{`\x{41}`, `\x without two hex digits`},
		{`\u0041`, `the escape \u`},
		{`[[:alpha:]]`, "POSIX class"},
		{`\pL`, `\p other than`},
		{`\p{Greek}`, `\p other than`},
		{`\p{Script=Greek}`, `\p other than`},
		{`\s`, `uses \s`},
		{`[\S]`, `uses \S`},
		{`.`, "uses ."},
		{`[]a]`, "] first in a class"},
		{`[^]a]`, "] first in a class"},
		{`a]`, "] outside a class"},
		{`a}`, "} outside a repeat count"},
		{`a{`, "{ that does not begin a repeat count"},
		{`a{,3}`, "{ that does not begin a repeat count"},
		{`^*`, "repeats an anchor"},
		{`$+`, "repeats an anchor"},
		{`\b?`, "repeats an anchor"},
		{`[\b]`, `\b in a class`},
		{`[\d-x]`, "range with"},
		{`[a-\w]`, "range with"},
		{`a\`, "lone backslash"},
	} {
		if why := dialectRefusal(c.p); !strings.Contains(why, c.want) {
			t.Errorf("%q: got %q, want it to contain %q", c.p, why, c.want)
		}
	}
}
