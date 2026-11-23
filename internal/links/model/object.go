package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Field is one key of an Object.
type Field struct {
	Key   string
	Value any
}

// Object is a mapping that keeps its keys in order, so that converted configs read the way
// clients write them and test vectors compare byte for byte. Values are strings, numbers,
// booleans, []any, []string, Object or nil.
type Object []Field

// Set replaces the value of key, or appends it.
func (o *Object) Set(key string, value any) {
	for index := range *o {
		if (*o)[index].Key == key {
			(*o)[index].Value = value
			return
		}
	}
	*o = append(*o, Field{key, value})
}

// Delete removes key.
func (o *Object) Delete(key string) {
	for index := range *o {
		if (*o)[index].Key == key {
			*o = append((*o)[:index], (*o)[index+1:]...)
			return
		}
	}
}

// Rename gives a key another name in its place. A field already named to is dropped.
func (o *Object) Rename(from, to string) {
	if _, set := o.Get(from); !set || from == to {
		return
	}
	o.Delete(to)
	for index := range *o {
		if (*o)[index].Key == from {
			(*o)[index].Key = to
		}
	}
}

// InsertAfter sets key right after the key after, or appends it when after is missing.
func (o *Object) InsertAfter(after, key string, value any) {
	o.Delete(key)
	for index := range *o {
		if (*o)[index].Key == after {
			*o = append((*o)[:index+1], append(Object{{key, value}}, (*o)[index+1:]...)...)
			return
		}
	}
	*o = append(*o, Field{key, value})
}

// Clone copies the object and every object and list in it.
func (o Object) Clone() Object {
	if o == nil {
		return nil
	}
	out := make(Object, len(o))
	for index, field := range o {
		out[index] = Field{field.Key, clone(field.Value)}
	}
	return out
}

func clone(value any) any {
	switch v := value.(type) {
	case Object:
		return v.Clone()
	case []any:
		out := make([]any, len(v))
		for index, item := range v {
			out[index] = clone(item)
		}
		return out
	case []string:
		return append([]string(nil), v...)
	}
	return value
}

// Value returns the value of key, or nil.
func (o Object) Value(key string) any {
	value, _ := o.Get(key)
	return value
}

// Get returns the value of key.
func (o Object) Get(key string) (any, bool) {
	for _, field := range o {
		if field.Key == key {
			return field.Value, true
		}
	}
	return nil, false
}

// String returns the value of key when it is a string.
func (o Object) String(key string) string {
	value, _ := o.Get(key)
	text, _ := value.(string)
	return text
}

// New returns an Object built from pairs, leaving out empty values: nil, "", false, 0,
// and empty lists and objects. Clients take a missing key as its default.
func New(pairs ...any) Object {
	out := make(Object, 0, len(pairs)/2)
	for index := 0; index+1 < len(pairs); index += 2 {
		key := pairs[index].(string)
		if !Empty(pairs[index+1]) {
			out = append(out, Field{key, pairs[index+1]})
		}
	}
	return out
}

func Empty(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case string:
		return v == ""
	case bool:
		return !v
	case int:
		return v == 0
	case float64:
		return v == 0
	case []any:
		return len(v) == 0
	case []string:
		return len(v) == 0
	case Object:
		return len(v) == 0
	}
	return false
}

// MarshalJSON writes the keys in order.
func (o Object) MarshalJSON() ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteByte('{')
	for index, field := range o {
		if index > 0 {
			buffer.WriteByte(',')
		}
		key, err := json.Marshal(field.Key)
		if err != nil {
			return nil, err
		}
		value, err := json.Marshal(field.Value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", field.Key, err)
		}
		buffer.Write(key)
		buffer.WriteByte(':')
		buffer.Write(value)
	}
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}

// MarshalYAML writes the keys in order.
func (o Object) MarshalYAML() (any, error) {
	node := &yaml.Node{Kind: yaml.MappingNode}
	for _, field := range o {
		value := &yaml.Node{}
		if err := value.Encode(field.Value); err != nil {
			return nil, fmt.Errorf("%s: %w", field.Key, err)
		}
		node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: field.Key}, value)
	}
	return node, nil
}

// Limits on what aliases may make of a document. Decoding into a yaml.Node leaves aliases to
// us, without go-yaml's own guards: an alias inside the node it names would never end, and
// aliases of aliases grow a short body into billions of values ("billion laughs").
const (
	// Values aliases may add, as a multiple of the values the document writes out, within a
	// floor and a ceiling.
	aliasRatio, aliasFloor, aliasCeiling = 100, 10_000, 1_000_000
	// Nesting, as deep as go-yaml lets a document itself go.
	maxDepth = 10_000
)

var errAliases = errors.New("yaml: aliases expand to too many values")

// decoder converts decoded YAML (or JSON, which is YAML) into ordered values.
type decoder struct {
	// Anchored nodes being converted; an alias to one of them would repeat it without end.
	open map[*yaml.Node]bool
	// Values still allowed.
	budget int
}

func (d *decoder) value(node *yaml.Node, depth int) (any, error) {
	if d.budget--; d.budget < 0 {
		return nil, errAliases
	}
	if depth > maxDepth {
		return nil, fmt.Errorf("line %d: nested deeper than %d", node.Line, maxDepth)
	}
	if node.Anchor != "" {
		if d.open[node] {
			return nil, fmt.Errorf("line %d: alias *%s inside the node it names", node.Line, node.Anchor)
		}
		d.open[node] = true
		defer delete(d.open, node)
	}
	switch node.Kind {
	case yaml.DocumentNode:
		if len(node.Content) == 0 {
			return nil, nil
		}
		return d.value(node.Content[0], depth+1)
	case yaml.AliasNode:
		if node.Alias == nil {
			return nil, fmt.Errorf("line %d: unknown anchor %s", node.Line, node.Value)
		}
		return d.value(node.Alias, depth+1)
	case yaml.MappingNode:
		o, err := d.mapping(node, depth)
		if err != nil {
			return nil, err
		}
		return o, nil
	case yaml.SequenceNode:
		out := make([]any, 0, len(node.Content))
		for _, item := range node.Content {
			value, err := d.value(item, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, value)
		}
		return out, nil
	default:
		var value any
		if err := node.Decode(&value); err != nil {
			return nil, err
		}
		// Integers stay ints and floats float64, as encoding/json would give numbers.
		if number, ok := value.(int64); ok {
			return int(number), nil
		}
		return value, nil
	}
}

// mapping converts a mapping. A later key replaces an earlier one in its place. A merge key
// ("<<") puts in its place the fields of a mapping, or of a list of them, that are not set
// yet; a key the mapping sets later replaces the merged value, and the first mapping of a list
// wins, as YAML has it.
func (d *decoder) mapping(node *yaml.Node, depth int) (Object, error) {
	out := fields{o: make(Object, 0, len(node.Content)/2)}
	for index := 0; index+1 < len(node.Content); index += 2 {
		key := node.Content[index]
		if key.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("line %d: a key that is not a scalar", key.Line)
		}
		value, err := d.value(node.Content[index+1], depth+1)
		if err != nil {
			return nil, err
		}
		if key.ShortTag() != "!!merge" {
			out.set(key.Value, value)
			continue
		}
		merged := []any{value}
		if list, ok := value.([]any); ok {
			merged = list
		}
		for _, item := range merged {
			for _, field := range AsObject(item) {
				out.add(field.Key, field.Value)
			}
		}
	}
	return out.o, nil
}

// fields builds an Object of many keys in linear time: past a few keys, a map finds them.
type fields struct {
	o     Object
	index map[string]int
}

func (f *fields) find(key string) int {
	if f.index != nil {
		if at, ok := f.index[key]; ok {
			return at
		}
		return -1
	}
	for at := range f.o {
		if f.o[at].Key == key {
			return at
		}
	}
	return -1
}

// set replaces the value of key, or appends it.
func (f *fields) set(key string, value any) {
	if at := f.find(key); at >= 0 {
		f.o[at].Value = value
		return
	}
	f.append(key, value)
}

// add appends key unless it is set.
func (f *fields) add(key string, value any) {
	if f.find(key) < 0 {
		f.append(key, value)
	}
}

func (f *fields) append(key string, value any) {
	f.o = append(f.o, Field{key, value})
	switch {
	case f.index != nil:
		f.index[key] = len(f.o) - 1
	case len(f.o) > 8:
		f.index = make(map[string]int, cap(f.o))
		for at, field := range f.o {
			f.index[field.Key] = at
		}
	}
}

// nodes counts the nodes a document writes out, aliases once each.
func nodes(node *yaml.Node) int {
	n := 1
	for _, child := range node.Content {
		n += nodes(child)
	}
	return n
}

// Decode parses YAML or JSON into ordered values.
func Decode(data []byte) (any, error) {
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return nil, err
	}
	written := nodes(&node)
	d := &decoder{open: map[*yaml.Node]bool{}, budget: written + min(max(aliasRatio*written, aliasFloor), aliasCeiling)}
	return d.value(&node, 0)
}

// Accessors for decoded values.

func AsObject(value any) Object {
	o, _ := value.(Object)
	return o
}

func AsList(value any) []any {
	switch v := value.(type) {
	case []any:
		return v
	case []string:
		out := make([]any, len(v))
		for index, item := range v {
			out[index] = item
		}
		return out
	case nil:
		return nil
	default:
		// sing-box accepts a single value where it takes a list.
		return []any{v}
	}
}

func AsStrings(value any) []string {
	var out []string
	for _, item := range AsList(value) {
		switch v := item.(type) {
		case string:
			out = append(out, v)
		case int:
			out = append(out, fmt.Sprint(v))
		}
	}
	return out
}

func AsInt(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case float64:
		return int(v)
	}
	return 0
}

func AsBool(value any) bool {
	b, _ := value.(bool)
	return b
}

// Int reads an integer written as a number or as a numeric string, as mihomo accepts.
func Int(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case float64:
		if v == math.Trunc(v) && math.Abs(v) < 1<<53 {
			return int(v), true
		}
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n, true
		}
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && f == math.Trunc(f) && math.Abs(f) < 1<<53 {
			return int(f), true
		}
	}
	return 0, false
}

// Bool reads a boolean written as one, as 1 or 0, or as true/false, yes/no or on/off in any
// case. mihomo itself takes only booleans and numbers.
func Bool(value any) (bool, bool) {
	switch v := value.(type) {
	case bool:
		return v, true
	case int:
		return v != 0, v == 0 || v == 1
	case float64:
		return v != 0, v == 0 || v == 1
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1", "yes", "on":
			return true, true
		case "false", "0", "no", "off", "":
			return false, true
		}
	}
	return false, false
}

// Text reads a scalar as a string: numbers in decimal, booleans as true or false.
func Text(value any) (string, bool) {
	switch v := value.(type) {
	case string:
		return v, true
	case int:
		return strconv.Itoa(v), true
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(v), true
	}
	return "", false
}
