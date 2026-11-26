package stack

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// valueNode encodes v as a YAML node; lists of small maps are written in
// flow style ({ id: general, name: General }) like the example file.
func valueNode(v any) (*yaml.Node, error) {
	var n yaml.Node
	if err := n.Encode(v); err != nil {
		return nil, err
	}
	if n.Kind == yaml.SequenceNode {
		for _, c := range n.Content {
			if c.Kind == yaml.MappingNode {
				c.Style = yaml.FlowStyle
			}
		}
		if len(n.Content) > 0 && n.Content[0].Kind == yaml.ScalarNode {
			n.Style = yaml.FlowStyle
		}
	}
	return &n, nil
}

func plain(n *yaml.Node) any {
	var v any
	_ = n.Decode(&v)
	return v
}

func sameValue(n *yaml.Node, v any) bool {
	nv, err := valueNode(v)
	if err != nil {
		return false
	}
	return reflect.DeepEqual(plain(n), plain(nv))
}

// lookup returns the value node for key in mapping m.
func lookup(m *yaml.Node, key string) (*yaml.Node, int) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1], i
		}
	}
	return nil, -1
}

func encode(doc *yaml.Node) ([]byte, error) {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// newSections builds the YAML text of keys as fresh sections.
func newSections(keys []ConfigKey) ([]byte, error) {
	root := &yaml.Node{Kind: yaml.MappingNode}
	for _, k := range keys {
		if k.IfPresent {
			continue
		}
		if _, err := setPath(root, k, -1); err != nil {
			return nil, err
		}
	}
	return encode(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}})
}

// setPath sets k under mapping m, creating mappings on the way. New
// top-level sections go at insertAt (after `version:`), or at the end
// when insertAt < 0. It reports whether anything changed.
func setPath(m *yaml.Node, k ConfigKey, insertAt int) (bool, error) {
	cur := m
	for i, key := range k.Path {
		last := i == len(k.Path)-1
		val, _ := lookup(cur, key)
		if val == nil {
			if k.IfPresent {
				return false, nil
			}
			var nv *yaml.Node
			if last {
				var err error
				if nv, err = valueNode(k.Value); err != nil {
					return false, err
				}
			} else {
				nv = &yaml.Node{Kind: yaml.MappingNode}
			}
			kn := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
			if i == 0 && insertAt >= 0 && insertAt <= len(cur.Content) {
				cur.Content = append(cur.Content[:insertAt], append([]*yaml.Node{kn, nv}, cur.Content[insertAt:]...)...)
			} else {
				cur.Content = append(cur.Content, kn, nv)
			}
			if last {
				return true, nil
			}
			cur = nv
			continue
		}
		if last {
			if k.IfAbsent || sameValue(val, k.Value) {
				return false, nil
			}
			nv, err := valueNode(k.Value)
			if err != nil {
				return false, err
			}
			nv.LineComment = val.LineComment
			*val = *nv
			return true, nil
		}
		if val.Kind != yaml.MappingNode {
			if val.Kind == yaml.ScalarNode && (val.Tag == "!!null" || val.Value == "") {
				*val = yaml.Node{Kind: yaml.MappingNode}
			} else {
				return false, fmt.Errorf("config/boson.yaml: %s is not a section", strings.Join(k.Path[:i+1], "."))
			}
		}
		cur = val
	}
	return false, nil
}

// ApplyConfig returns config/boson.yaml with keys set. A new file
// (existing == nil) is the example file with the keys as sections right
// under `version:`, as scripts/setup.sh writes it. An existing file is
// edited in place (comments and other keys kept); unchanged reports false
// when every key already had its value.
func ApplyConfig(existing, example []byte, keys []ConfigKey) ([]byte, bool, error) {
	if existing == nil {
		sec, err := newSections(keys)
		if err != nil {
			return nil, false, err
		}
		lines := strings.SplitAfter(string(example), "\n")
		var b strings.Builder
		done := false
		for _, l := range lines {
			b.WriteString(l)
			if !done && strings.HasPrefix(l, "version:") {
				b.Write(sec)
				done = true
			}
		}
		if !done {
			return nil, false, errors.New("config/boson.example.yaml has no version: line")
		}
		return []byte(b.String()), true, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(existing, &doc); err != nil {
		return nil, false, fmt.Errorf("config/boson.yaml doesn't parse (fix it or move it away): %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, false, errors.New("config/boson.yaml is not a YAML mapping")
	}
	root := doc.Content[0]
	insertAt := -1
	if _, i := lookup(root, "version"); i >= 0 {
		insertAt = i + 2
	}
	changed := false
	for _, k := range keys {
		before := len(root.Content)
		ch, err := setPath(root, k, insertAt)
		if err != nil {
			return nil, false, err
		}
		if insertAt >= 0 && len(root.Content) > before {
			insertAt += 2
		}
		changed = changed || ch
	}
	if !changed {
		return existing, false, nil
	}
	out, err := encode(&doc)
	return out, err == nil, err
}

// ConfigValue reads a dotted key from a config/boson.yaml text (nil if absent).
func ConfigValue(text []byte, path ...string) any {
	var m map[string]any
	if yaml.Unmarshal(text, &m) != nil {
		return nil
	}
	var cur any = m
	for _, k := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}
