package dataapi

import (
	"bytes"
	"encoding"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode"
)

// This file derives JSON Schema (the dialect OpenAPI 3.1 embeds) from the Go types the
// handlers actually encode, so the generated API description cannot name a field the
// code does not send. It understands exactly the encoding/json rules the response types
// use: field names from `json` tags (or the Go name), `omitempty` and `-`, embedded
// structs, pointers, slices, maps, time.Time, any. Go doc comments are not reachable by
// reflection, so a derived field has a type but no description.

// prop is one property of an object schema; propList keeps declaration order (a plain
// map would be re-sorted alphabetically by encoding/json).
type prop struct {
	name   string
	schema any
}

type propList []prop

// MarshalJSON writes the properties as a JSON object in declaration order.
func (p propList) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, e := range p {
		if i > 0 {
			buf.WriteByte(',')
		}
		k, err := marshalPlain(e.name)
		if err != nil {
			return nil, err
		}
		v, err := marshalPlain(e.schema)
		if err != nil {
			return nil, err
		}
		buf.Write(k)
		buf.WriteByte(':')
		buf.Write(v)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// marshalPlain is json.Marshal without HTML escaping ("<", ">", "&" stay readable).
func marshalPlain(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// schema is a JSON Schema object (or a small OpenAPI object built the same way);
// "properties" holds a propList. It marshals its keys in a fixed, readable order
// (type before properties before required) instead of alphabetically.
type schema map[string]any

// schemaKeyOrder lists the keys that come first, in this order; the rest follow
// alphabetically.
var schemaKeyOrder = []string{
	"$ref", "type", "const", "format", "description", "enum", "default", "minimum", "minLength", "pattern",
	"contentEncoding", "items", "oneOf", "allOf", "additionalProperties", "properties", "required",
}

// MarshalJSON implements json.Marshaler.
func (s schema) MarshalJSON() ([]byte, error) {
	keys := make([]string, 0, len(s))
	for k := range s {
		keys = append(keys, k)
	}
	rank := func(k string) int {
		if i := slices.Index(schemaKeyOrder, k); i >= 0 {
			return i
		}
		return len(schemaKeyOrder)
	}
	slices.SortFunc(keys, func(a, b string) int {
		if c := rank(a) - rank(b); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	out := make(propList, len(keys))
	for i, k := range keys {
		out[i] = prop{name: k, schema: s[k]}
	}
	return out.MarshalJSON()
}

// asMap views a schema value, whichever of the two map types it is stored as.
func asMap(v any) (map[string]any, bool) {
	switch m := v.(type) {
	case schema:
		return m, true
	case map[string]any:
		return m, true
	}
	return nil, false
}

// ordered builds a propList from alternating names and values, for the OpenAPI objects
// whose key order matters to a reader (a parameter's name before its description).
func ordered(kv ...any) propList {
	out := make(propList, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, prop{name: kv[i].(string), schema: kv[i+1]})
	}
	return out
}

// orderSchema returns v with every map replaced by a schema, so a tree decoded from
// JSON (the tool argument schemas) marshals in the same readable order.
func orderSchema(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(schema, len(x))
		for k, e := range x {
			out[k] = orderSchema(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = orderSchema(e)
		}
		return out
	}
	return v
}

// schemaBuilder turns Go types into schemas. Named struct types become entries of
// components (referenced by $ref), everything else is inlined.
type schemaBuilder struct {
	components map[string]schema
	owners     map[string]reflect.Type
	// opaque lists the types the builder could not describe (custom marshalers); the
	// tests require it to stay empty for every API type.
	opaque []reflect.Type
	err    error
}

func newSchemaBuilder() *schemaBuilder {
	return &schemaBuilder{components: map[string]schema{}, owners: map[string]reflect.Type{}}
}

var (
	timeType      = reflect.TypeFor[time.Time]()
	durationType  = reflect.TypeFor[time.Duration]()
	rawJSONType   = reflect.TypeFor[json.RawMessage]()
	jsonMarshaler = reflect.TypeFor[json.Marshaler]()
	textMarshaler = reflect.TypeFor[encoding.TextMarshaler]()
)

func (b *schemaBuilder) fail(format string, args ...any) {
	if b.err == nil {
		b.err = fmt.Errorf(format, args...)
	}
}

// ref registers the named struct type t as a component (named name, or the type's name
// with a capital) and returns a $ref to it.
func (b *schemaBuilder) ref(t reflect.Type, name string) schema {
	if name == "" {
		name = componentName(t)
	}
	if owner, ok := b.owners[name]; ok {
		if owner != t {
			b.fail("schema component %q is claimed by both %v and %v", name, owner, t)
		}
		return schema{"$ref": "#/components/schemas/" + name}
	}
	b.owners[name] = t
	b.components[name] = nil // reserve the name before recursing: types may refer to themselves
	b.components[name] = b.object(t)
	return schema{"$ref": "#/components/schemas/" + name}
}

func componentName(t reflect.Type) string {
	n := t.Name()
	if n == "" || strings.ContainsAny(n, "[]*") {
		return "Anonymous"
	}
	r := []rune(n)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// of returns the schema of t, as it appears at a use site.
func (b *schemaBuilder) of(t reflect.Type) any {
	switch {
	case t == timeType:
		return schema{"type": "string", "format": "date-time"}
	case t == durationType:
		return schema{"type": "integer", "description": "nanoseconds"}
	case t == rawJSONType:
		return schema{}
	}
	switch t.Kind() {
	case reflect.Pointer:
		return nullable(b.of(t.Elem()))
	case reflect.Bool:
		return schema{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return schema{"type": "integer"}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return schema{"type": "integer", "minimum": 0}
	case reflect.Float32, reflect.Float64:
		return schema{"type": "number"}
	case reflect.String:
		return schema{"type": "string"}
	case reflect.Interface:
		return schema{}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 && t.Kind() == reflect.Slice {
			return schema{"type": "string", "contentEncoding": "base64"}
		}
		return schema{"type": "array", "items": b.of(t.Elem())}
	case reflect.Map:
		return schema{"type": "object", "additionalProperties": b.of(t.Elem())}
	case reflect.Struct:
		if hasCustomJSON(t) {
			b.opaque = append(b.opaque, t)
			return schema{}
		}
		return b.ref(t, "")
	}
	b.fail("cannot derive a schema for %v", t)
	return schema{}
}

func hasCustomJSON(t reflect.Type) bool {
	p := reflect.PointerTo(t)
	return t.Implements(jsonMarshaler) || p.Implements(jsonMarshaler) ||
		t.Implements(textMarshaler) || p.Implements(textMarshaler)
}

// nullable widens a schema to also accept null.
func nullable(s any) any {
	m, ok := asMap(s)
	if !ok {
		return s
	}
	if _, isRef := m["$ref"]; isRef {
		return schema{"oneOf": []any{m, schema{"type": "null"}}}
	}
	switch typ := m["type"].(type) {
	case string:
		out := schema{}
		for k, v := range m {
			out[k] = v
		}
		out["type"] = []any{typ, "null"}
		return out
	case nil:
		return m // the any-schema already accepts null
	}
	return schema{"oneOf": []any{m, schema{"type": "null"}}}
}

// object is the schema of a struct's JSON object: one property per exported field, all
// required except those tagged omitempty.
func (b *schemaBuilder) object(t reflect.Type) schema {
	var props propList
	var required []string
	b.collect(t, &props, &required)
	s := schema{"type": "object", "properties": props}
	if props == nil {
		s["properties"] = propList{}
	}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func (b *schemaBuilder) collect(t reflect.Type, props *propList, required *[]string) {
	for f := range t.Fields() {
		tag, hasTag := f.Tag.Lookup("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		omitEmpty := slices.Contains(strings.Split(opts, ","), "omitempty")
		asString := slices.Contains(strings.Split(opts, ","), "string")

		if f.Anonymous && (!hasTag || name == "") {
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct && !hasCustomJSON(ft) {
				b.collect(ft, props, required) // embedded struct: its fields are promoted
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}

		var fs any
		ft := f.Type
		if omitEmpty && ft.Kind() == reflect.Pointer {
			ft = ft.Elem() // absent instead of null
		}
		if asString {
			fs = schema{"type": "string"}
		} else {
			fs = b.of(ft)
		}
		*props = append(*props, prop{name: name, schema: fs})
		if !omitEmpty {
			*required = append(*required, name)
		}
	}
}

// body describes a JSON request body: the struct's properties, with an explicit
// required list and per-property overrides (description, enum, ...). It fails the
// builder when an override or a required name is not a field of t, so a renamed field
// cannot leave stale documentation behind.
func (b *schemaBuilder) body(t reflect.Type, required []string, overrides map[string]schema) schema {
	s := b.object(t)
	props, _ := s["properties"].(propList)
	known := map[string]int{}
	for i, p := range props {
		known[p.name] = i
	}
	for name, o := range overrides {
		i, ok := known[name]
		if !ok {
			b.fail("%v has no JSON field %q to describe", t, name)
			continue
		}
		merged := schema{}
		if base, ok := asMap(props[i].schema); ok {
			for k, v := range base {
				merged[k] = v
			}
		}
		for k, v := range o {
			merged[k] = v
		}
		props[i].schema = merged
	}
	delete(s, "required")
	for _, name := range required {
		if _, ok := known[name]; !ok {
			b.fail("%v has no JSON field %q to require", t, name)
		}
	}
	if len(required) > 0 {
		s["required"] = slices.Clone(required)
	}
	return s
}
