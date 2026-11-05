package config

import (
	"fmt"
	"strings"
)

// usage describes every setting, one line each: the key, how each origin names it, its type,
// whether it is required and its description.
func (l *loader) usage() string {
	var b strings.Builder
	b.WriteString("config: the settings (a flag beats the environment, which beats a file, which beats the default)")
	for _, f := range l.leaves {
		var ways []string
		if len(l.lo.flags) == 1 && !f.secret && !f.cplx {
			ways = append(ways, "-"+flagName(f.key))
		}
		if len(l.lo.env) == 1 && !f.cplx {
			v := envName(l.lo.env[0].prefix, f.key)
			if f.secret {
				v += " or " + v + "_FILE"
			}
			ways = append(ways, v)
		}
		if len(l.lo.files) > 0 {
			ways = append(ways, fmt.Sprintf("%q in a file", f.key))
		}
		typ := f.lf.descOf(f)
		switch {
		case f.hasDef && f.secret:
			typ += ", default [redacted]"
		case f.hasDef:
			typ += ", default " + f.def
		case f.opt:
			typ += ", optional"
		default:
			typ += ", required"
		}
		line := fmt.Sprintf("\n  %s: %s (%s)", f.key, strings.Join(ways, ", "), typ)
		if f.desc != "" {
			line += " " + f.desc
		}
		b.WriteString(line)
	}
	return b.String()
}

func (lf *leaf) descOf(f *field) string {
	switch {
	case f.cplx:
		return "map or list of structs, from a file only"
	case lf == nil:
		return "value"
	case f.list:
		return "list of " + lf.desc
	}
	return lf.desc
}
