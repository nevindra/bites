// Command patterndialect compares Go's regexp (RE2) with ECMA-262 (new RegExp(p, "u"), the dialect
// of JSON Schema "pattern") for a table of candidate constructs: does each compile in each
// engine, and when it compiles in both, does it match the same samples. It needs node on PATH
// for the ECMA-262 side and says so when it is missing.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

type cand struct {
	P       string   `json:"p"`
	Note    string   `json:"note"`
	Samples []string `json:"samples"`
}

type jsRes struct {
	OK      bool   `json:"ok"`
	Err     string `json:"err"`
	Matches []bool `json:"matches"`
}

var cands = []cand{
	{`^[a-z0-9]+$`, "plain class, anchors", []string{"abc1", "ABC", ""}},
	{`^\d{3}-\d{4}$`, `\d, counted repeat`, []string{"555-1234", "٣٣٣-1234"}},
	{`^\w+$`, `\w`, []string{"abc_1", "é"}},
	{`\bfoo\b`, `\b`, []string{"a foo b", "éfoo"}},
	{`(?:ab)+`, "non-capturing group", []string{"abab", "ba"}},
	{`(ab)+`, "capturing group", []string{"abab", "ba"}},
	{`a+?`, "lazy", []string{"aaa"}},
	{`^\p{L}+$`, `\p{L}`, []string{"héllo", "a1"}},
	{`^\p{Lu}\p{Ll}*$`, `\p{Lu}`, []string{"Hello", "hello"}},
	{`^\p{Greek}+$`, `\p{Greek} (script name)`, []string{"αβγ"}},
	{`^\p{Script=Greek}+$`, `\p{Script=Greek}`, []string{"αβγ"}},
	{`^\pL+$`, `\pL without braces`, []string{"abc"}},
	{`^\p{C}$`, `\p{C} (Other)`, []string{"͸", "\x00"}},
	{`(?P<year>\d{4})`, `(?P<name>…)`, []string{"2024"}},
	{`(?<year>\d{4})`, `(?<name>…) (Go 1.22+)`, []string{"2024"}},
	{`(?i)abc`, `(?i) flag`, []string{"ABC"}},
	{`(?i:abc)`, `(?i:…) scoped flag`, []string{"ABC"}},
	{`(?s)a.b`, `(?s) flag`, []string{"a\nb"}},
	{`(?m)^a$`, `(?m) flag`, []string{"x\na\ny"}},
	{`(?U)a+`, `(?U) flag`, []string{"aaa"}},
	{`\Aabc\z`, `\A and \z`, []string{"abc"}},
	{`\Aabc`, `\A`, []string{"abc"}},
	{`abc\z`, `\z`, []string{"abc"}},
	{`\Qa.b\E`, `\Q…\E`, []string{"a.b", "axb"}},
	{`[[:alpha:]]+`, `POSIX class`, []string{"abc", ":"}},
	{`[^[:digit:]]`, `negated POSIX class`, []string{"a", ":"}},
	{`\C`, `\C one byte`, []string{"a"}},
	{`\x41`, `\xHH`, []string{"A"}},
	{`\x{41}`, `\x{HHHH}`, []string{"A"}},
	{`\u0041`, `\uHHHH`, []string{"A"}},
	{`\a`, `\a bell`, []string{"\a"}},
	{`\t\n\r\f\v`, `control escapes`, []string{"\t\n\r\f\v"}},
	{`\_`, `escaped underscore`, []string{"_"}},
	{`a\-b`, `\- outside a class`, []string{"a-b"}},
	{`[a\-z]`, `\- inside a class`, []string{"-"}},
	{`\ `, `escaped space`, []string{" "}},
	{`\"`, `escaped quote`, []string{`"`}},
	{`\/`, `escaped slash`, []string{"/"}},
	{`\0`, `\0`, []string{"\x00"}},
	{`(a)\1`, `backreference`, []string{"aa"}},
	{`(?=a)a`, `lookahead`, []string{"a"}},
	{`(?<=a)b`, `lookbehind`, []string{"ab"}},
	{`\s`, `\s`, []string{" ", " ", "\v", "\ufeff", " "}},
	{`\S`, `\S`, []string{" ", " "}},
	{`^.$`, `. (dot)`, []string{"a", "\n", "\r", " ", " "}},
	{`^a$`, `$ without m`, []string{"a", "a\n"}},
	{`a{`, `lone {`, []string{"a{"}},
	{`a{x}`, `{ not a count`, []string{"a{x}"}},
	{`a}`, `lone }`, []string{"a}"}},
	{`a]`, `lone ]`, []string{"a]"}},
	{`[]a]`, `] first in a class`, []string{"]", "a"}},
	{`[^]a]`, `] first in a negated class`, []string{"b", "]"}},
	{`[a-]`, `trailing - in a class`, []string{"-"}},
	{`[\b]`, `\b in a class`, []string{"\b"}},
	{`^*`, `quantified ^`, []string{"a"}},
	{`$*`, `quantified $`, []string{"a"}},
	{`\b+`, `quantified \b`, []string{"a"}},
	{`a**`, `nested repeat`, []string{"a"}},
	{`a{1001}`, `repeat over 1000`, []string{"a"}},
	{`a{2,1}`, `inverted repeat`, []string{"aa"}},
	{`(`, `unterminated group`, nil},
	{`[a-`, `unterminated class`, nil},
	{`a|`, `empty alternative`, []string{"", "a"}},
	{`()`, `empty group`, []string{""}},
	{`[\d-x]`, `class escape as range start`, []string{"-"}},
	{`[é-ü]`, `non-ASCII range`, []string{"ö"}},
	{`😀+`, `astral literal`, []string{"😀😀"}},
	{`^[😀]$`, `astral in a class`, []string{"😀"}},
	{`(?:)`, `empty non-capturing group`, []string{""}},
	{`^\x{1F600}$`, `\x{…} astral`, []string{"😀"}},
}

const nodeScript = `
const cs = JSON.parse(require('fs').readFileSync(0, 'utf8'));
const out = cs.map(c => {
  try {
    const re = new RegExp(c.p, 'u');
    return {ok: true, matches: (c.samples || []).map(s => re.test(s))};
  } catch (e) { return {ok: false, err: String(e.message)}; }
});
process.stdout.write(JSON.stringify(out));
`

func main() {
	var js []jsRes
	haveNode := false
	if _, err := exec.LookPath("node"); err == nil {
		in, _ := json.Marshal(cands)
		cmd := exec.Command("node", "-e", nodeScript)
		cmd.Stdin = strings.NewReader(string(in))
		out, err := cmd.Output()
		if err == nil && json.Unmarshal(out, &js) == nil && len(js) == len(cands) {
			haveNode = true
		}
	}
	if !haveNode {
		fmt.Fprintln(os.Stderr, "node is not on PATH: the ECMA-262 column is not run")
	}
	fmt.Printf("%-28s %-6s %-6s %s\n", "construct", "Go", "ECMA", "result")
	for i, c := range cands {
		re, goErr := regexp.Compile(c.P)
		goOK := goErr == nil
		ecma, res := "n/a", ""
		if haveNode {
			ecma = word(js[i].OK)
			switch {
			case goOK && js[i].OK:
				res = "same syntax"
				for j, s := range c.Samples {
					if re.MatchString(s) != js[i].Matches[j] {
						res = fmt.Sprintf("DIFFERENT MEANING: %q Go=%v ECMA=%v", s, re.MatchString(s), js[i].Matches[j])
						break
					}
				}
			case goOK && !js[i].OK:
				res = "Go only: " + js[i].Err
			case !goOK && js[i].OK:
				res = "ECMA only"
			default:
				res = "neither"
			}
		}
		fmt.Printf("%-28s %-6s %-6s %s\n", c.Note, word(goOK), ecma, res)
	}
	if haveNode {
		categories()
	}
}

func word(ok bool) string {
	if ok {
		return "ok"
	}
	return "error"
}

// categories compares the code points of each general category in Go and in ECMA-262: the
// short names are common, but the Unicode versions differ, and Go's \p{C} omits unassigned.
func categories() {
	cats := []string{"L", "Lu", "Ll", "Lt", "Lm", "Lo", "M", "Mn", "Mc", "Me", "N", "Nd", "Nl", "No", "P", "Pc", "Pd", "Ps", "Pe", "Pi", "Pf", "Po", "S", "Sm", "Sc", "Sk", "So", "Z", "Zs", "Zl", "Zp", "C", "Cc", "Cf", "Cs", "Co"}
	script := `
const cats = JSON.parse(require('fs').readFileSync(0, 'utf8'));
const out = {};
for (const c of cats) {
  const re = new RegExp('^\\p{' + c + '}$', 'u');
  let n = 0;
  for (let cp = 0; cp <= 0x10FFFF; cp++) { if (cp >= 0xD800 && cp <= 0xDFFF) continue; if (re.test(String.fromCodePoint(cp))) n++; }
  out[c] = n;
}
process.stdout.write(JSON.stringify(out));
`
	in, _ := json.Marshal(cats)
	cmd := exec.Command("node", "-e", script)
	cmd.Stdin = strings.NewReader(string(in))
	b, err := cmd.Output()
	if err != nil {
		fmt.Println("categories: node failed:", err)
		return
	}
	var node map[string]int
	_ = json.Unmarshal(b, &node)
	fmt.Println("\ngeneral category code point counts (surrogates skipped), Go vs node:")
	for _, c := range cats {
		re := regexp.MustCompile(`^\p{` + c + `}$`)
		n := 0
		for cp := rune(0); cp <= 0x10FFFF; cp++ {
			if cp >= 0xD800 && cp <= 0xDFFF {
				continue
			}
			if re.MatchString(string(cp)) {
				n++
			}
		}
		mark := "same"
		if n != node[c] {
			mark = fmt.Sprintf("DIFFERENT (%+d)", n-node[c])
		}
		fmt.Printf("  %-3s Go=%-7d node=%-7d %s\n", c, n, node[c], mark)
	}
}
