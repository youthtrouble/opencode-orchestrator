// Package policy is the effective autonomy policy of a project (bin/policy.mjs): the
// registry's `autonomy` block, overridden key by key by <project>/.orchestrator/policy.json. It
// says what a conductor may do without asking (push_to_main, release, ...); force_push is
// always "never".
package policy

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/paths"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/routing"
)

// UnattendedPairs are what `policy unattended` sets.
var UnattendedPairs = []string{"push_to_main=auto", "product_questions=continue_with_assumption"}

// ErrForcePush refuses a policy whose force_push is anything but "never".
var ErrForcePush = errors.New("force_push is always never")

// RefusedError refuses setting a key other than force_push to "never".
type RefusedError struct {
	Key string
}

func (e *RefusedError) Error() string { return "refusing " + e.Key + "=never" }

// InvalidValueError refuses a value its key does not take: openai_credits is off or on, and
// openai_credits_floor is a number, 0 or more.
type InvalidValueError struct {
	Message string
}

func (e *InvalidValueError) Error() string { return e.Message }

// invalidValue is the refusal of value for key, or nil when the key takes it.
func invalidValue(key, value string) error {
	if key == "openai_credits" && value != "off" && value != "on" {
		return &InvalidValueError{"openai_credits must be off or on"}
	}
	if floor, isNumber := routing.DecimalOrNull(value); key == "openai_credits_floor" && !(isNumber && floor >= 0) {
		return &InvalidValueError{"openai_credits_floor must be a number, 0 or more"}
	}
	return nil
}

// TypeError is the TypeError the JS throws when policy.json holds a primitive, with V8's
// message, or (PE-6, not mirrored: the JS rewrites the array) an array.
type TypeError struct {
	Message string
}

func (e *TypeError) Error() string { return e.Message }

// File is the project's override file.
func File(projectRoot string) string {
	return filepath.Join(paths.ProjectDir(projectRoot), "policy.json")
}

// Read mirrors read(): the parsed file, or {} when it cannot be read or parsed. The value may
// have any JSON shape.
func Read(file string) any {
	data, err := nodefs.ReadFile(file)
	if err != nil {
		return jsjson.NewObject()
	}
	doc, err := jsjson.Unmarshal([]byte(jsstr.DecodeUTF8(data)))
	if err != nil {
		return jsjson.NewObject()
	}
	return doc
}

// Effective mirrors effective(): `{...(autonomy || {})}` without its $comment, then every own
// property of the override file spread over it (overrides keep the base position, new keys
// come after). autonomy is the registry's `autonomy` value, of any shape; nil when absent.
func Effective(autonomy any, file string) *jsjson.Object {
	base := jsjson.Spread(autonomy)
	base.Delete("$comment")
	overrides := jsjson.Spread(Read(file))
	for _, key := range overrides.Keys() {
		value, _ := overrides.Get(key)
		base.Set(key, value)
	}
	return base
}

// Set applies pairs ("key=value"; the value ends at the next "=") to the override document cur
// in order, as `cur[k] = v` does, and returns it. A pair setting a key other than force_push
// to "never" is refused, as is a value its key does not take (InvalidValueError) and a result
// whose own force_push is not "never". A key __proto__
// is a no-op unless cur has it as an own key, as JS assignment through the prototype setter.
func Set(cur any, pairs []string) (any, error) {
	for _, pair := range pairs {
		parts := strings.Split(pair, "=")
		key, value := parts[0], parts[1]
		if value == "never" && key != "force_push" {
			return nil, &RefusedError{key}
		}
		if err := invalidValue(key, value); err != nil {
			return nil, err
		}
		if err := assign(cur, key, value); err != nil {
			return nil, err
		}
	}
	doc, isObject := cur.(*jsjson.Object)
	if !isObject {
		return nil, &TypeError{"Cannot use 'in' operator to search for 'force_push' in " + jsjson.TemplateString(cur)}
	}
	if forcePush, present := doc.Get("force_push"); present && forcePush != "never" {
		return nil, ErrForcePush
	}
	return doc, nil
}

func assign(cur any, key, value string) error {
	switch cur := cur.(type) {
	case *jsjson.Object:
		if _, own := cur.Get(key); key != "__proto__" || own {
			cur.Set(key, value)
		}
		return nil
	case []any:
		return &TypeError{"policy.json holding an array is not supported (PE-6)"}
	case nil:
		return &TypeError{fmt.Sprintf("Cannot set properties of null (setting '%s')", key)}
	}
	if key == "__proto__" {
		return nil // the setter ignores a primitive receiver given a non-object
	}
	if s, isString := cur.(string); isString && (key == "length" || isIndexOf(key, s)) {
		return &TypeError{fmt.Sprintf("Cannot assign to read only property '%s' of string '%s'", key, s)}
	}
	return &TypeError{fmt.Sprintf("Cannot create property '%s' on %s '%s'", key, typeOf(cur), jsjson.TemplateString(cur))}
}

// isIndexOf reports whether key is the canonical form of an index into s's UTF-16 code units.
func isIndexOf(key, s string) bool {
	n, err := strconv.Atoi(key)
	return err == nil && n >= 0 && strconv.Itoa(n) == key && n < jsstr.Len(s)
}

// typeOf is JS typeof for a primitive JSON value other than null.
func typeOf(v any) string {
	switch v.(type) {
	case float64:
		return "number"
	case bool:
		return "boolean"
	}
	return "string"
}
