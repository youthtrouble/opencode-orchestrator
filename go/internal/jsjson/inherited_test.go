package jsjson

import "testing"

// TestInheritedTextIsStringOfObjectPrototypeKeys pins String(({})[key]) for every own key of
// Object.prototype (Node v26.5.1) and undefined for others.
func TestInheritedTextIsStringOfObjectPrototypeKeys(t *testing.T) {
	want := map[string]string{
		"constructor":          "function Object() { [native code] }",
		"__defineGetter__":     "function __defineGetter__() { [native code] }",
		"__defineSetter__":     "function __defineSetter__() { [native code] }",
		"hasOwnProperty":       "function hasOwnProperty() { [native code] }",
		"__lookupGetter__":     "function __lookupGetter__() { [native code] }",
		"__lookupSetter__":     "function __lookupSetter__() { [native code] }",
		"isPrototypeOf":        "function isPrototypeOf() { [native code] }",
		"propertyIsEnumerable": "function propertyIsEnumerable() { [native code] }",
		"toString":             "function toString() { [native code] }",
		"valueOf":              "function valueOf() { [native code] }",
		"__proto__":            "[object Object]",
		"toLocaleString":       "function toLocaleString() { [native code] }",
	}
	for key, text := range want {
		if got, ok := InheritedText(key); !ok || got != text {
			t.Errorf("InheritedText(%q) = %q, %v; want %q", key, got, ok, text)
		}
	}
	for _, key := range []string{"", "Object", "openai", "at", "length", "toJSON", "__proto"} {
		if got, ok := InheritedText(key); ok {
			t.Errorf("InheritedText(%q) = %q, want not inherited", key, got)
		}
	}
}
