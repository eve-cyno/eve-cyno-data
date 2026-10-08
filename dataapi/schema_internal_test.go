package dataapi

import (
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type sampleBase struct {
	ID int `json:"id"`
}

type sampleInner struct {
	A int `json:"a"`
}

type sample struct {
	sampleBase
	Name   string             `json:"name"`
	Opt    string             `json:"opt,omitempty"`
	Ptr    *int               `json:"ptr"`
	PtrOpt *int               `json:"ptr_opt,omitempty"`
	Inner  sampleInner        `json:"inner"`
	InnerP *sampleInner       `json:"inner_p"`
	List   []sampleInner      `json:"list"`
	M      map[string]float64 `json:"m"`
	When   time.Time          `json:"when"`
	Any    any                `json:"any"`
	Skip   string             `json:"-"`
	hidden string
	NoTag  bool
	Num    int64  `json:"num,string"`
	Bytes  []byte `json:"bytes"`
	Uns    uint8  `json:"uns"`
}

func marshal(t *testing.T, v any) string {
	t.Helper()
	b, err := marshalPlain(v)
	require.NoError(t, err)
	return string(b)
}

func TestSchemaBuilder_DerivesFromEncodingJSONRules(t *testing.T) {
	_ = sample{}.hidden // the unexported field is part of the fixture
	b := newSchemaBuilder()
	ref := b.ref(reflect.TypeFor[sample](), "")
	require.NoError(t, b.err)
	require.Empty(t, b.opaque)
	require.JSONEq(t, `{"$ref":"#/components/schemas/Sample"}`, marshal(t, ref))

	require.JSONEq(t, `{
	  "type": "object",
	  "properties": {
	    "id":      {"type": "integer"},
	    "name":    {"type": "string"},
	    "opt":     {"type": "string"},
	    "ptr":     {"type": ["integer", "null"]},
	    "ptr_opt": {"type": "integer"},
	    "inner":   {"$ref": "#/components/schemas/SampleInner"},
	    "inner_p": {"oneOf": [{"$ref": "#/components/schemas/SampleInner"}, {"type": "null"}]},
	    "list":    {"type": "array", "items": {"$ref": "#/components/schemas/SampleInner"}},
	    "m":       {"type": "object", "additionalProperties": {"type": "number"}},
	    "when":    {"type": "string", "format": "date-time"},
	    "any":     {},
	    "NoTag":   {"type": "boolean"},
	    "num":     {"type": "string"},
	    "bytes":   {"type": "string", "contentEncoding": "base64"},
	    "uns":     {"type": "integer", "minimum": 0}
	  },
	  "required": ["id","name","ptr","inner","inner_p","list","m","when","any","NoTag","num","bytes","uns"]
	}`, marshal(t, b.components["Sample"]))

	require.JSONEq(t, `{"type":"object","properties":{"a":{"type":"integer"}},"required":["a"]}`,
		marshal(t, b.components["SampleInner"]))
}

func TestSchemaBuilder_PropertiesKeepDeclarationOrder(t *testing.T) {
	b := newSchemaBuilder()
	b.ref(reflect.TypeFor[sample](), "")
	props, ok := b.components["Sample"]["properties"].(propList)
	require.True(t, ok)
	var names []string
	for _, p := range props {
		names = append(names, p.name)
	}
	require.Equal(t, []string{"id", "name", "opt", "ptr", "ptr_opt", "inner", "inner_p", "list", "m", "when", "any", "NoTag", "num", "bytes", "uns"}, names,
		"the embedded struct's fields come where it is declared; declaration order, not alphabetical")
}

func TestSchema_MarshalsTypeBeforePropertiesBeforeRequired(t *testing.T) {
	out := marshal(t, schema{"required": []string{"a"}, "properties": propList{{name: "a", schema: schema{"type": "string"}}}, "type": "object", "description": "d"})
	require.Equal(t, `{"type":"object","description":"d","properties":{"a":{"type":"string"}},"required":["a"]}`, out)
}

type selfRef struct {
	Next *selfRef  `json:"next,omitempty"`
	Kids []selfRef `json:"kids"`
}

func TestSchemaBuilder_SelfReferenceTerminates(t *testing.T) {
	b := newSchemaBuilder()
	b.ref(reflect.TypeFor[selfRef](), "")
	require.NoError(t, b.err)
	require.JSONEq(t, `{"type":"object","properties":{
	  "next":{"$ref":"#/components/schemas/SelfRef"},
	  "kids":{"type":"array","items":{"$ref":"#/components/schemas/SelfRef"}}},"required":["kids"]}`,
		marshal(t, b.components["SelfRef"]))
}

type customJSON struct{ X int }

func (customJSON) MarshalJSON() ([]byte, error) { return []byte(`"x"`), nil }

func TestSchemaBuilder_CustomMarshalerIsReportedOpaque(t *testing.T) {
	b := newSchemaBuilder()
	got := b.of(reflect.TypeFor[customJSON]())
	require.Equal(t, schema{}, got)
	require.Equal(t, []reflect.Type{reflect.TypeFor[customJSON]()}, b.opaque)
}

func TestSchemaBuilder_NameCollisionIsAnError(t *testing.T) {
	type dup struct{ A int }
	b := newSchemaBuilder()
	b.ref(reflect.TypeFor[dup](), "Same")
	b.ref(reflect.TypeFor[sampleInner](), "Same")
	require.ErrorContains(t, b.err, `"Same" is claimed by both`)
}

func TestSchemaBuilder_BodyOverridesMustNameRealFields(t *testing.T) {
	b := newSchemaBuilder()
	s := b.body(reflect.TypeFor[fitDetailRequest](), []string{"eft"}, map[string]schema{
		"eft": {"description": "the fit"},
	})
	require.NoError(t, b.err)
	require.JSONEq(t, `{"type":"object","properties":{
	  "eft":{"type":"string","description":"the fit"},
	  "cost":{"type":"boolean"}},"required":["eft"]}`, marshal(t, s))

	b = newSchemaBuilder()
	b.body(reflect.TypeFor[fitDetailRequest](), []string{"eft"}, map[string]schema{"gone": {"description": "x"}})
	require.ErrorContains(t, b.err, `no JSON field "gone"`)

	b = newSchemaBuilder()
	b.body(reflect.TypeFor[fitDetailRequest](), []string{"gone"}, nil)
	require.ErrorContains(t, b.err, `no JSON field "gone" to require`)
}

func TestFirstSentence(t *testing.T) {
	require.Equal(t, "Get prices.", firstSentence("Get prices. Returns best sell.", 100))
	require.Equal(t, "Look up modules (e.g. 'Steel Plates', 'Heat Sink') by family.", firstSentence("Look up modules (e.g. 'Steel Plates', 'Heat Sink') by family. Next.", 100),
		"an abbreviation followed by a quote is not a sentence end")
	require.Equal(t, "No full stop here", firstSentence("No full stop here", 100))
	got := firstSentence("A very long sentence that keeps going and going well past the budget we allow it", 30)
	require.LessOrEqual(t, len([]rune(got)), 31)
	require.Equal(t, "A very long sentence that…", got)
}
