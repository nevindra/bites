package config

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"reflect"
	"strconv"
	"strings"

	"github.com/nevindra/bites"
)

type loader struct {
	c      *compiler
	lo     *loadOptions
	sc     *schema
	leaves []*field
	root   reflect.Value

	probs   []Problem
	unknown map[originRef][]Problem // by origin, in the order each was found
	files   []*loadedFile
	flagSet map[string]string // flag name -> text
	flagBad map[string]bool   // flags already reported
	envSet  map[string]string
	origins map[string]string // key -> origin, for the log
	texts   map[string]string // key -> text written, for the log
}

type loadedFile struct {
	name string
	root *node
}

func (l *loader) add(key, origin, msg string) {
	l.probs = append(l.probs, Problem{Key: key, Origin: origin, Message: msg})
}

// flagProblem lists a mistake in a flag with the flags' unknown ones, in the order found.
func (l *loader) flagProblem(ref originRef, key, origin, msg string) {
	if l.unknown == nil {
		l.unknown = map[originRef][]Problem{}
	}
	l.unknown[ref] = append(l.unknown[ref], Problem{Key: key, Origin: origin, Message: msg})
}

func (l *loader) addUnknown(ref originRef, key, origin, msg string) {
	if l.unknown == nil {
		l.unknown = map[originRef][]Problem{}
	}
	l.unknown[ref] = append(l.unknown[ref], Problem{Key: key, Origin: origin, Message: msg, unknown: true})
}

func fileOrigin(name string, line int) string {
	return "file " + name + ", line " + strconv.Itoa(line)
}

// run reads every origin and fills the struct. It returns a *Error, or the help error.
func (l *loader) run() error {
	fileProbs := map[int][]Problem{}
	// Files first: parsing needs no schema.
	for i, f := range l.lo.files {
		l.files = append(l.files, nil)
		data, err := fs.ReadFile(f.fsys, f.name)
		if err != nil {
			if f.optional && errors.Is(err, fs.ErrNotExist) {
				continue
			}
			msg := "the file cannot be read: " + trimPath(err, f.name)
			if errors.Is(err, fs.ErrNotExist) {
				msg = "the file does not exist"
			}
			fileProbs[i] = append(fileProbs[i], Problem{Key: f.name, Origin: "file " + f.name, Message: msg})
			continue
		}
		root, probs, fatal := parseFile(f.name, data)
		if fatal != nil {
			fileProbs[i] = append(fileProbs[i], Problem{Key: f.name, Origin: fileOrigin(f.name, fatal.line), Message: fatal.msg})
			continue
		}
		root.nulls("", &probs)
		for _, p := range probs {
			fileProbs[i] = append(fileProbs[i], Problem{Key: p.key, Origin: fileOrigin(f.name, p.line), Message: p.msg})
		}
		l.files[i] = &loadedFile{f.name, root}
	}
	if len(l.lo.flags) == 1 {
		if help := l.readFlags(l.lo.flags[0].args); help {
			return &helpError{l.usage()}
		}
	}
	if len(l.lo.env) == 1 {
		l.readEnv(l.lo.env[0])
	}
	for _, f := range l.leaves {
		l.resolve(f)
	}
	// Unknown settings, in the order their origins were given.
	for _, ref := range l.lo.order {
		switch ref.kind {
		case 'f':
			l.probs = append(l.probs, fileProbs[ref.idx]...)
			if lf := l.files[ref.idx]; lf != nil {
				l.walkUnknown(l.sc, lf.root, "", lf.name, ref)
			}
		case 'e':
			l.unknownEnv(l.lo.env[ref.idx], ref)
		}
		l.probs = append(l.probs, l.unknown[ref]...)
	}
	if len(l.probs) > 0 {
		return &Error{Problems: l.probs}
	}
	return nil
}

func trimPath(err error, name string) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

// sources of one setting.
type source struct {
	origin string // "env APP_X"
	text   string
	n      *node // a file's value
	line   int
}

func (l *loader) resolve(f *field) {
	var src *source
	bad := false
	if fn := flagName(f.key); len(l.lo.flags) == 1 && !f.cplx {
		if v, ok := l.flagSet[fn]; ok {
			if f.secret {
				bad = true
			} else {
				src = &source{origin: "flag -" + fn, text: v}
			}
		}
	}
	if src == nil && !bad && len(l.lo.env) == 1 && !f.cplx {
		src, bad = l.envSource(f)
	}
	if src == nil && !bad {
		for i := len(l.files) - 1; i >= 0; i-- {
			if l.files[i] == nil {
				continue
			}
			if n, line := lookup(l.files[i].root, f.key); n != nil {
				switch n.kind {
				case nNull, nRefused:
					bad = true // reported with the file
				default:
					src = &source{origin: fileOrigin(l.files[i].name, line), n: n, line: line}
				}
				break
			}
		}
	}
	if bad {
		return
	}
	if src == nil && f.hasDef {
		src = &source{origin: "default", text: f.def}
	}
	if src == nil {
		if f.opt {
			l.origins[f.key] = "absent"
			return
		}
		l.add(f.key, "", "missing"+l.ways(f))
		return
	}
	l.fill(f, src)
}

// ways says every way to set f, for the missing sentence.
func (l *loader) ways(f *field) string {
	var parts []string
	if len(l.lo.flags) == 1 && !f.secret && !f.cplx {
		parts = append(parts, "-"+flagName(f.key))
	}
	if len(l.lo.env) == 1 && !f.cplx {
		v := envName(l.lo.env[0].prefix, f.key)
		parts = append(parts, v)
		if f.secret {
			parts = append(parts, v+"_FILE")
		}
	}
	for _, fo := range l.lo.files {
		parts = append(parts, fmt.Sprintf("%q in %s", f.key, fo.name))
	}
	switch len(parts) {
	case 0:
		return "; no origin was given to Load, and it has no default"
	case 1:
		return "; set " + parts[0]
	}
	return "; set " + strings.Join(parts[:len(parts)-1], ", ") + " or " + parts[len(parts)-1]
}

func lookup(root *node, key string) (*node, int) {
	n, line := root, 0
	for _, seg := range strings.Split(key, ".") {
		if n.kind != nMap {
			return nil, 0
		}
		n, line = n.child(seg)
		if n == nil {
			return nil, 0
		}
	}
	return n, line
}

func (l *loader) envSource(f *field) (src *source, bad bool) {
	name := envName(l.lo.env[0].prefix, f.key)
	v, has := l.envSet[name]
	fv, hasFile := l.envSet[name+"_FILE"]
	if !f.secret {
		hasFile = false
	}
	switch {
	case has && hasFile:
		l.add(f.key, "env "+name+"_FILE", fmt.Sprintf("%s and %s_FILE are both set; set one", name, name))
		return nil, true
	case has:
		return &source{origin: "env " + name, text: v}, false
	case hasFile:
		text, err := l.readSecretFile(fv)
		if err != "" {
			l.add(f.key, "env "+name+"_FILE", err)
			return nil, true
		}
		return &source{origin: "env " + name + "_FILE", text: text}, false
	}
	return nil, false
}

func (l *loader) readSecretFile(p string) (string, string) {
	if len(l.lo.secretFS) == 0 {
		return "", "the variable names a file, but Load was given no SecretFiles option to read it from; pass config.SecretFiles(os.DirFS(\"/\"))"
	}
	p = strings.TrimPrefix(p, "/")
	if !fs.ValidPath(p) {
		return "", "the path is not valid for the fs.FS given to SecretFiles (no \"..\", no empty element)"
	}
	b, err := fs.ReadFile(l.lo.secretFS[0].fsys, p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", "the file does not exist"
		}
		return "", "the file cannot be read: " + trimPath(err, p)
	}
	return strings.TrimSuffix(string(b), "\n"), ""
}

// fill decodes src into f's place in the struct.
func (l *loader) fill(f *field, src *source) {
	target := l.root.FieldByIndex(f.index)
	v := reflect.New(f.elem).Elem()
	ok := true
	switch {
	case f.cplx:
		if src.n == nil {
			l.add(f.key, src.origin, "a map or a list of structs is read from a file only")
			return
		}
		ok = l.fillTree(src.n, v, f.key, fileNameOf(src.origin))
	case f.list:
		ok = l.fillList(f, src, v)
	default:
		if src.n != nil {
			if src.n.kind != nScalar {
				l.add(f.key, src.origin, "expected a single value, not "+kindName(src.n.kind))
				return
			}
			src.text = src.n.text
		}
		if err := f.lf.dec(v, src.text); err != nil {
			l.add(f.key, src.origin, err.Error())
			ok = false
		}
	}
	if !ok {
		return
	}
	if f.opt {
		target.Addr().MethodByName("Set").Call([]reflect.Value{v})
	} else {
		target.Set(v)
	}
	l.origins[f.key] = src.origin
	if src.n != nil && (f.list || f.cplx) {
		l.texts[f.key] = "(from the file)"
	} else {
		l.texts[f.key] = src.text
	}
}

func fileNameOf(origin string) string {
	name, _, _ := strings.Cut(strings.TrimPrefix(origin, "file "), ", line ")
	return name
}

func kindName(k nodeKind) string {
	switch k {
	case nMap:
		return "an object"
	case nList:
		return "a list"
	}
	return "a value"
}

func (l *loader) fillList(f *field, src *source, v reflect.Value) bool {
	var items []string
	if src.n != nil {
		if src.n.kind != nList {
			l.add(f.key, src.origin, "expected a list, not "+kindName(src.n.kind))
			return false
		}
		ok := true
		sl := reflect.MakeSlice(f.elem, len(src.n.vals), len(src.n.vals))
		for i, it := range src.n.vals {
			key := f.key + "[" + strconv.Itoa(i) + "]"
			if it.kind != nScalar {
				if it.kind == nMap || it.kind == nList {
					l.add(key, fileOrigin(fileNameOf(src.origin), it.line), "expected a single value, not "+kindName(it.kind))
				}
				ok = false
				continue
			}
			if err := f.lf.dec(sl.Index(i), it.text); err != nil {
				l.add(key, fileOrigin(fileNameOf(src.origin), it.line), err.Error())
				ok = false
			}
		}
		v.Set(sl)
		return ok
	}
	if strings.TrimSpace(src.text) != "" {
		for _, p := range strings.Split(src.text, ",") {
			items = append(items, strings.TrimSpace(p))
		}
	}
	sl := reflect.MakeSlice(f.elem, len(items), len(items))
	ok := true
	for i, it := range items {
		if err := f.lf.dec(sl.Index(i), it); err != nil {
			l.add(f.key+"["+strconv.Itoa(i)+"]", src.origin, err.Error()+"; an element cannot contain a comma here, a list of such values is set in a file")
			ok = false
		}
	}
	v.Set(sl)
	return ok
}

// fillTree fills dst from a file node. It reports false when something was wrong.
func (l *loader) fillTree(n *node, dst reflect.Value, key, file string) bool {
	if n.kind == nNull || n.kind == nRefused {
		return false
	}
	origin := fileOrigin(file, n.line)
	t := dst.Type()
	if lf, _ := compileLeaf(t); lf != nil {
		if n.kind != nScalar {
			l.add(key, origin, "expected a single value, not "+kindName(n.kind))
			return false
		}
		if err := lf.dec(dst, n.text); err != nil {
			l.add(key, origin, err.Error())
			return false
		}
		return true
	}
	ok := true
	switch t.Kind() {
	case reflect.Slice:
		if n.kind != nList {
			l.add(key, origin, "expected a list, not "+kindName(n.kind))
			return false
		}
		sl := reflect.MakeSlice(t, len(n.vals), len(n.vals))
		for i, it := range n.vals {
			ok = l.fillTree(it, sl.Index(i), key+"["+strconv.Itoa(i)+"]", file) && ok
		}
		dst.Set(sl)
	case reflect.Map:
		if n.kind != nMap {
			l.add(key, origin, "expected an object, not "+kindName(n.kind))
			return false
		}
		m := reflect.MakeMapWithSize(t, len(n.keys))
		for i, k := range n.keys {
			e := reflect.New(t.Elem()).Elem()
			ok = l.fillTree(n.vals[i], e, joinKey(key, k), file) && ok
			m.SetMapIndex(reflect.ValueOf(k).Convert(t.Key()), e)
		}
		dst.Set(m)
	case reflect.Struct:
		if n.kind != nMap {
			l.add(key, origin, "expected an object, not "+kindName(n.kind))
			return false
		}
		sub := &compiler{root: l.c.root, site: l.c.site, envP: l.c.envP}
		sc := sub.compileStruct(t, "", "", nil)
		ok = l.fillStruct(sc, n, dst, key, file)
	}
	return ok
}

func (l *loader) fillStruct(sc *schema, n *node, dst reflect.Value, key, file string) bool {
	ok := true
	known := make([]string, 0, len(sc.fields))
	for _, f := range sc.fields {
		known = append(known, f.seg)
		k := joinKey(key, f.seg)
		child, _ := n.child(f.seg)
		target := dst.FieldByIndex(f.index)
		switch {
		case f.sub != nil:
			empty := child
			if empty == nil {
				empty = &node{kind: nMap, line: n.line}
			}
			if empty.kind != nMap {
				l.add(k, fileOrigin(file, empty.line), "expected an object, not "+kindName(empty.kind))
				ok = false
				continue
			}
			ok = l.fillStruct(f.sub, empty, dst, k, file) && ok
		case child == nil:
			if !f.opt {
				l.add(k, fileOrigin(file, n.line), "missing")
				ok = false
			}
		default:
			v := reflect.New(f.elem).Elem()
			if !l.fillTree(child, v, k, file) {
				ok = false
				continue
			}
			if f.opt {
				target.Addr().MethodByName("Set").Call([]reflect.Value{v})
			} else {
				target.Set(v)
			}
		}
	}
	for i, k := range n.keys {
		found := false
		for _, s := range known {
			found = found || s == k
		}
		if !found {
			msg := "unknown setting"
			if near, ok := nearest(k, known); ok {
				msg += "; did you mean " + joinKey(key, near) + "?"
			}
			l.probs = append(l.probs, Problem{Key: joinKey(key, k), Origin: fileOrigin(file, n.keyLines[i]), Message: msg, unknown: true})
			ok = false
		}
	}
	return ok
}

// walkUnknown reports the keys of a file that no field has.
func (l *loader) walkUnknown(sc *schema, n *node, prefix, file string, ref originRef) {
	var segs []string
	for _, f := range sc.fields {
		segs = append(segs, f.seg)
	}
	for i, k := range n.keys {
		f := (*field)(nil)
		for _, c := range sc.fields {
			if c.seg == k {
				f = c
			}
		}
		key := joinKey(prefix, k)
		if f == nil {
			msg := "unknown setting"
			if near, ok := nearest(k, segs); ok {
				msg += "; did you mean " + joinKey(prefix, near) + "?"
			}
			l.addUnknown(ref, key, fileOrigin(file, n.keyLines[i]), msg)
			continue
		}
		if f.sub != nil {
			v := n.vals[i]
			if v.kind == nMap {
				l.walkUnknown(f.sub, v, key, file, ref)
			} else if v.kind == nScalar || v.kind == nList {
				l.addUnknown(ref, key, fileOrigin(file, n.keyLines[i]), "expected an object of settings, not "+kindName(v.kind))
			}
		}
	}
}

// readFlags parses the arguments. It reports true for -h and -help.
func (l *loader) readFlags(args []string) (help bool) {
	ref := originRef{'a', 0}
	l.flagSet = map[string]string{}
	byName := map[string]*field{}
	var names []string
	for _, f := range l.leaves {
		byName[flagName(f.key)] = f
		if !f.cplx {
			names = append(names, "-"+flagName(f.key))
		}
	}
	unknownSeen := false
	positional := 0
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional += len(args) - i - 1
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			positional++
			continue
		}
		name := strings.TrimPrefix(strings.TrimPrefix(a, "-"), "-")
		val, hasVal := "", false
		if k, v, ok := strings.Cut(name, "="); ok {
			name, val, hasVal = k, v, true
		}
		if name == "h" || name == "help" {
			return true
		}
		f := byName[name]
		if f == nil || f.cplx {
			unknownSeen = true
			msg := "unknown setting"
			if f != nil {
				msg = "unknown setting; a map or a list of structs is read from a file only, set " + f.key + " in the file"
			} else if near, ok := nearest("-"+name, names); ok {
				msg += "; did you mean " + near + "?"
			}
			l.addUnknown(ref, "-"+name, "flag", msg)
			continue
		}
		if f.secret {
			l.flagProblem(ref, f.key, "flag -"+name, "a Secret cannot be set by flag (the command line is visible to every user of the machine); use "+l.secretWays(f))
			l.flagSet[name] = ""
			continue
		}
		if !hasVal {
			if f.elem.Kind() == reflect.Bool && !f.list {
				val = "true"
			} else if i+1 < len(args) {
				i++
				val = args[i]
			} else {
				l.flagProblem(ref, f.key, "flag -"+name, "needs a value")
				continue
			}
		}
		if _, dup := l.flagSet[name]; dup {
			l.flagProblem(ref, f.key, "flag -"+name, "given twice; give it once")
			continue
		}
		l.flagSet[name] = val
	}
	if positional > 0 && !unknownSeen {
		l.flagProblem(ref, "args", "flag", fmt.Sprintf("%d argument(s) are not flags; settings are given as -name=value", positional))
	}
	return false
}

func (l *loader) secretWays(f *field) string {
	var parts []string
	if len(l.lo.env) == 1 {
		v := envName(l.lo.env[0].prefix, f.key)
		parts = append(parts, v, v+"_FILE")
	}
	parts = append(parts, fmt.Sprintf("%q in a file", f.key))
	if len(parts) == 1 {
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " or " + parts[len(parts)-1]
}

func (l *loader) readEnv(e envOpt) {
	l.envSet = map[string]string{}
	for _, kv := range e.environ {
		if k, v, ok := strings.Cut(kv, "="); ok {
			l.envSet[k] = v
		}
	}
}

func (l *loader) unknownEnv(e envOpt, ref originRef) {
	valid := map[string]*field{}
	var names []string
	for _, f := range l.leaves {
		n := envName(e.prefix, f.key)
		valid[n] = f
		names = append(names, n)
		if f.secret {
			valid[n+"_FILE"] = f
			names = append(names, n+"_FILE")
		}
	}
	skip := map[string]bool{}
	for _, s := range e.skip {
		skip[s] = true
	}
	for _, kv := range e.environ {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(name, e.prefix+"_") || skip[name] {
			continue
		}
		if f, ok := valid[name]; ok {
			if !f.cplx {
				continue
			}
			l.addUnknown(ref, name, "env", "unknown setting; a map or a list of structs is read from a file only, set "+f.key+" in the file")
			continue
		}
		msg := "unknown setting"
		if base, ok := strings.CutSuffix(name, "_FILE"); ok && valid[base] != nil && !valid[base].cplx {
			msg += "; only a Secret is read from a file named by a _FILE variable, and " + valid[base].key + " is not one"
		} else if near, ok := nearest(name, names); ok {
			msg += "; did you mean " + near + "?"
		} else if k8sLink(strings.TrimPrefix(name, e.prefix+"_")) {
			svc := strings.ToLower(strings.ReplaceAll(e.prefix, "_", "-"))
			msg += fmt.Sprintf("; Kubernetes injects %s_SERVICE_HOST, %s_PORT and %s_PORT_<n>_TCP... for a Service named %s: set enableServiceLinks: false on the pod, or list %s among the non-settings of Env", e.prefix, e.prefix, e.prefix, svc, name)
		}
		l.addUnknown(ref, name, "env", msg)
	}
}

// k8sLink says whether rest, the name after the prefix, has the shape of a variable Kubernetes
// injects for a Service: PORT, PORT_80_TCP..., SERVICE_HOST, SERVICE_PORT...
func k8sLink(rest string) bool {
	if rest == "PORT" || rest == "SERVICE_HOST" || rest == "SERVICE_PORT" || strings.HasPrefix(rest, "SERVICE_PORT_") {
		return true
	}
	if after, ok := strings.CutPrefix(rest, "PORT_"); ok {
		num, _, _ := strings.Cut(after, "_")
		_, err := strconv.Atoi(num)
		return err == nil
	}
	return false
}

// validate runs the root's Validate and lists what it returns.
func (l *loader) validate(cfg any) {
	rv := reflect.ValueOf(cfg)
	err := rv.MethodByName("Validate").Call(nil)[0].Interface()
	if err == nil {
		return
	}
	l.collect(err.(error))
}

func (l *loader) collect(err error) {
	if fe, ok := err.(*bites.FieldError); ok {
		l.add(fe.Location, "", fe.Message)
		return
	}
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		for _, c := range j.Unwrap() {
			l.collect(c)
		}
		return
	}
	var fe *bites.FieldError
	if u := errors.Unwrap(err); u != nil && errors.As(u, &fe) {
		l.collect(u)
		return
	}
	l.add("", "", "Validate: "+err.Error())
}

func (l *loader) log() {
	if l.lo.logger == nil {
		return
	}
	for _, f := range l.leaves {
		origin := l.origins[f.key]
		val := l.texts[f.key]
		switch {
		case f.secret:
			val = redacted
		case origin == "absent":
			val = "<absent>"
		}
		l.lo.logger.LogAttrs(context.Background(), slog.LevelInfo, "config: setting",
			slog.String("key", f.key), slog.String("value", val), slog.String("origin", origin))
	}
}
