package dataapi

import (
	_ "embed"
	"encoding/json"
	"path"
	"reflect"
	"sync"

	"eve-cyno.dev/go/data/internal/fielddocs"
)

// fielddocs.json maps "pkg.Type.Field" to the doc comment of that struct field. It is
// generated from the source by internal/fielddocs (go generate); the schema deriver puts
// each entry into the `description` of the property, in the OpenAPI document and on the
// typed-result tables of the docs pages.
//
//go:embed fielddocs.json
var fieldDocsJSON []byte

// fieldDocs returns the parsed fielddocs.json (parsed once; the map is read-only). A broken
// embedded file is a generation bug caught by TestFieldDocsAreCurrent: the schemas then
// simply carry no descriptions.
var fieldDocs = sync.OnceValue(func() map[string]string {
	var m map[string]string
	if err := json.Unmarshal(fieldDocsJSON, &m); err != nil {
		return map[string]string{}
	}
	return m
})

// fieldDocKey is the fielddocs.json key of field f of struct type t.
func fieldDocKey(t reflect.Type, f reflect.StructField) string {
	return fielddocs.Key(path.Base(t.PkgPath()), t.Name(), f.Name)
}

// describeField returns the property schema fs with the description d, leaving an
// existing description alone. fs is never mutated.
func describeField(fs any, d string) any {
	m, ok := asMap(fs)
	if !ok {
		return fs
	}
	if _, has := m["description"]; has {
		return fs
	}
	out := make(schema, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	out["description"] = d
	return out
}
