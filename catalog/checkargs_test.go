package catalog_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/catalog"
)

func schemaTool(t *testing.T, schema string) catalog.Tool {
	t.Helper()
	var params map[string]any
	require.NoError(t, json.Unmarshal([]byte(schema), &params))
	return catalog.Tool{Name: "x", Parameters: params}
}

func TestCheckArgs(t *testing.T) {
	jumps := schemaTool(t, `{"type":"object","properties":{"from_system":{"type":"string"},"to_system":{"type":"string"},"prefer_high_sec":{"type":"boolean"}},"required":["from_system","to_system"]}`)
	mixed := schemaTool(t, `{"type":"object","properties":{"n":{"type":"integer"},"flag":{"type":"boolean"}},"required":["n","flag"]}`)
	free := schemaTool(t, `{"type":"object","properties":{"a":{"type":"string"}}}`)

	cases := []struct {
		name string
		tool catalog.Tool
		args map[string]any
		want string // "" = no error
	}{
		{"all present", jumps, map[string]any{"from_system": "Jita", "to_system": "Amarr"}, ""},
		{"one missing", jumps, map[string]any{"from_system": "Jita"}, "missing required argument(s): to_system"},
		{"null", jumps, map[string]any{"from_system": nil, "to_system": "Amarr"}, "missing required argument(s): from_system"},
		{"empty string", jumps, map[string]any{"from_system": "", "to_system": "Amarr"}, "missing required argument(s): from_system"},
		{"whitespace string", jumps, map[string]any{"from_system": "Jita", "to_system": " \t "}, "missing required argument(s): to_system"},
		{"nil map", jumps, nil, "missing required argument(s): from_system, to_system"},
		{"misspelled keys", jumps, map[string]any{"to": "Amarr", "from": "Jita"},
			"missing required argument(s): from_system, to_system; unexpected argument(s): from, to"},
		{"extra key, nothing missing", jumps, map[string]any{"from_system": "Jita", "to_system": "Amarr", "extra": 1}, ""},
		{"no required", free, map[string]any{}, ""},
		{"numeric and bool present", mixed, map[string]any{"n": float64(0), "flag": false}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.tool.CheckArgs(c.args)
			if c.want == "" {
				require.NoError(t, err)
				return
			}
			require.EqualError(t, err, c.want)
			require.ErrorIs(t, err, catalog.ErrInvalidArgs)
			var ae *catalog.ArgsError
			require.True(t, errors.As(err, &ae))
		})
	}
}

func TestCheckArgs_StringSliceRequired(t *testing.T) {
	tl := catalog.Tool{Parameters: map[string]any{"required": []string{"a", "b"}}}
	require.EqualError(t, tl.CheckArgs(map[string]any{"a": "x"}), "missing required argument(s): b")
}
