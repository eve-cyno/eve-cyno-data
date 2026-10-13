package catalog

import (
	"errors"
	"sort"
	"strings"
)

// ErrInvalidArgs is what errors.Is matches for an argument error from CheckArgs.
var ErrInvalidArgs = errors.New("invalid arguments")

// ArgsError says which required arguments a call lacks and which names it sent that the
// tool does not declare (usually misspellings of the missing ones).
type ArgsError struct {
	// Missing lists the absent required arguments, in the order the schema declares them.
	Missing []string
	// Unexpected lists the sent names the schema does not declare, sorted.
	Unexpected []string
}

func (e *ArgsError) Error() string {
	msg := "missing required argument(s): " + strings.Join(e.Missing, ", ")
	if len(e.Unexpected) > 0 {
		msg += "; unexpected argument(s): " + strings.Join(e.Unexpected, ", ")
	}
	return msg
}

// Is makes errors.Is(err, ErrInvalidArgs) true for an *ArgsError.
func (e *ArgsError) Is(target error) bool { return target == ErrInvalidArgs }

// CheckArgs reports an *ArgsError when a required argument of the tool's schema is missing
// from args: absent, JSON null, or a string that is blank. Unknown names alone are not an
// error (older clients may send extras); they are only named when something is missing.
// A schema without "required" always passes.
func (t Tool) CheckArgs(args map[string]any) error {
	var required []string
	switch r := t.Parameters["required"].(type) {
	case []string:
		required = r
	case []any:
		for _, v := range r {
			if s, ok := v.(string); ok {
				required = append(required, s)
			}
		}
	}
	var missing []string
	for _, name := range required {
		if blank(args[name]) {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	props, _ := t.Parameters["properties"].(map[string]any)
	var unexpected []string
	for name := range args {
		if _, declared := props[name]; declared {
			continue
		}
		isRequired := false
		for _, r := range required {
			isRequired = isRequired || r == name
		}
		if !isRequired {
			unexpected = append(unexpected, name)
		}
	}
	sort.Strings(unexpected)
	return &ArgsError{Missing: missing, Unexpected: unexpected}
}

func blank(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(x) == ""
	}
	return false
}
