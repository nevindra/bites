package config

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	yaml "go.yaml.in/yaml/v3"
)

type nodeKind uint8

const (
	nScalar nodeKind = iota
	nMap
	nList
	nNull
	nRefused // a node already reported (a tag, an anchor, an alias): nothing more is said about it
)

// node is a configuration file as a tree of the text written in it: a scalar is its text, never
// a resolved type.
type node struct {
	kind     nodeKind
	text     string
	line     int
	keys     []string // nMap: in the order written
	keyLines []int
	vals     []*node // nMap values, or the items of an nList
}

func (n *node) child(key string) (*node, int) {
	for i, k := range n.keys {
		if k == key {
			return n.vals[i], n.keyLines[i]
		}
	}
	return nil, 0
}

// fileProblem is a mistake found while reading a file.
type fileProblem struct {
	key  string
	line int
	msg  string
}

func (n *node) add(key string, line int, v *node, path string, probs *[]fileProblem) {
	if prev, first := n.child(key); prev != nil {
		*probs = append(*probs, fileProblem{path, line, fmt.Sprintf("duplicate key; first written at line %d", first)})
		return
	}
	n.keys = append(n.keys, key)
	n.keyLines = append(n.keyLines, line)
	n.vals = append(n.vals, v)
}

func joinKey(prefix, k string) string {
	if prefix == "" {
		return k
	}
	return prefix + "." + k
}

// parseFile reads a JSON or YAML file by its extension. A syntax error is returned alone (fatal):
// no setting is read from the file. Other mistakes are returned with the tree.
func parseFile(name string, data []byte) (root *node, probs []fileProblem, fatal *fileProblem) {
	switch strings.ToLower(path.Ext(name)) {
	case ".json":
		return parseJSON(data)
	}
	return parseYAML(data)
}

func lineAt(data []byte, off int64) int {
	if off > int64(len(data)) {
		off = int64(len(data))
	}
	return bytes.Count(data[:off], []byte("\n")) + 1
}

func parseJSON(data []byte) (*node, []fileProblem, *fileProblem) {
	d := jsontext.NewDecoder(bytes.NewReader(data), jsontext.AllowDuplicateNames(true))
	var probs []fileProblem
	root, err := readJSON(d, data, "", &probs)
	if err == nil {
		if _, e := d.ReadToken(); e != io.EOF {
			err = errors.New("unexpected data after the top-level value")
			if e != nil {
				err = e
			}
		}
	}
	if err != nil {
		line := lineAt(data, d.InputOffset())
		var se *jsontext.SyntacticError
		if errors.As(err, &se) {
			line = lineAt(data, se.ByteOffset)
		}
		return nil, nil, &fileProblem{"", line, "not valid JSON: " + strings.TrimPrefix(err.Error(), "jsontext: ")}
	}
	if root.kind != nMap {
		return nil, nil, &fileProblem{"", root.line, "the top-level value must be an object of settings"}
	}
	return root, probs, nil
}

func readJSON(d *jsontext.Decoder, data []byte, at string, probs *[]fileProblem) (*node, error) {
	tok, err := d.ReadToken()
	if err != nil {
		return nil, err
	}
	line := lineAt(data, d.InputOffset())
	switch tok.Kind() {
	case '{':
		n := &node{kind: nMap, line: line}
		for {
			kt, err := d.ReadToken()
			if err != nil {
				return nil, err
			}
			if kt.Kind() == '}' {
				return n, nil
			}
			key, kline := kt.String(), lineAt(data, d.InputOffset())
			v, err := readJSON(d, data, joinKey(at, key), probs)
			if err != nil {
				return nil, err
			}
			n.add(key, kline, v, joinKey(at, key), probs)
		}
	case '[':
		n := &node{kind: nList, line: line}
		for i := 0; ; i++ {
			if k := d.PeekKind(); k == ']' {
				_, err := d.ReadToken()
				return n, err
			}
			v, err := readJSON(d, data, at+"["+strconv.Itoa(i)+"]", probs)
			if err != nil {
				return nil, err
			}
			n.vals = append(n.vals, v)
		}
	case 'n':
		return &node{kind: nNull, line: line}, nil
	}
	return &node{kind: nScalar, text: tok.String(), line: line}, nil
}

func parseYAML(data []byte) (*node, []fileProblem, *fileProblem) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		msg := strings.TrimPrefix(err.Error(), "yaml: ")
		line := 1
		var te *yaml.TypeError
		if !errors.As(err, &te) {
			if rest, ok := strings.CutPrefix(msg, "line "); ok {
				if num, _, ok := strings.Cut(rest, ":"); ok {
					if v, e := strconv.Atoi(num); e == nil {
						line = v
						msg = strings.TrimSpace(strings.TrimPrefix(rest, num+":"))
					}
				}
			}
		}
		return nil, nil, &fileProblem{"", line, "not valid YAML: " + msg}
	}
	var probs []fileProblem
	if doc.Kind == 0 || len(doc.Content) == 0 {
		return &node{kind: nMap, line: 1}, nil, nil
	}
	root := readYAML(doc.Content[0], "", &probs)
	if root.kind != nMap {
		return nil, nil, &fileProblem{"", root.line, "the top-level value must be a mapping of settings"}
	}
	return root, probs, nil
}

const yamlSecondWay = "a tag, an anchor or a merge key is a second way to write a value; write the value"

func readYAML(y *yaml.Node, at string, probs *[]fileProblem) *node {
	if y.Anchor != "" || y.Kind == yaml.AliasNode || y.Style&yaml.TaggedStyle != 0 {
		*probs = append(*probs, fileProblem{at, y.Line, yamlSecondWay})
		return &node{kind: nRefused, line: y.Line}
	}
	switch y.Kind {
	case yaml.MappingNode:
		n := &node{kind: nMap, line: y.Line}
		for i := 0; i+1 < len(y.Content); i += 2 {
			k, v := y.Content[i], y.Content[i+1]
			if k.Tag == "!!merge" || k.Value == "<<" && k.Style == 0 {
				*probs = append(*probs, fileProblem{joinKey(at, "<<"), k.Line, yamlSecondWay})
				continue
			}
			if k.Kind != yaml.ScalarNode {
				*probs = append(*probs, fileProblem{at, k.Line, "a key must be a plain word"})
				continue
			}
			child := readYAML(v, joinKey(at, k.Value), probs)
			n.add(k.Value, k.Line, child, joinKey(at, k.Value), probs)
		}
		return n
	case yaml.SequenceNode:
		n := &node{kind: nList, line: y.Line}
		for i, c := range y.Content {
			n.vals = append(n.vals, readYAML(c, at+"["+strconv.Itoa(i)+"]", probs))
		}
		return n
	case yaml.ScalarNode:
		if y.Tag == "!!null" {
			return &node{kind: nNull, line: y.Line}
		}
		return &node{kind: nScalar, text: y.Value, line: y.Line}
	}
	return &node{kind: nNull, line: y.Line}
}

// nulls reports every null in the tree: a setting is present or absent, and null is neither.
func (n *node) nulls(at string, probs *[]fileProblem) {
	for i, v := range n.vals {
		k := at + "[" + strconv.Itoa(i) + "]"
		line := v.line
		if n.kind == nMap {
			k, line = joinKey(at, n.keys[i]), n.keyLines[i]
		}
		if v.kind == nNull {
			*probs = append(*probs, fileProblem{k, line, "null is not a value, remove the key"})
		}
		v.nulls(k, probs)
	}
}
