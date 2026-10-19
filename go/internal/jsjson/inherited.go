package jsjson

// inheritedMethods maps the names Object.prototype holds a method under to the name in that
// method's source text.
var inheritedMethods = map[string]string{
	"constructor": "Object", "__defineGetter__": "__defineGetter__", "__defineSetter__": "__defineSetter__",
	"hasOwnProperty": "hasOwnProperty", "__lookupGetter__": "__lookupGetter__", "__lookupSetter__": "__lookupSetter__",
	"isPrototypeOf": "isPrototypeOf", "propertyIsEnumerable": "propertyIsEnumerable", "toString": "toString",
	"valueOf": "valueOf", "toLocaleString": "toLocaleString",
}

// InheritedText is String(({})[key]) for a key a plain object inherits from Object.prototype:
// a method's source text ("function toString() { [native code] }", "function Object() {
// [native code] }" for constructor) or "[object Object]" for __proto__. ok is false for every
// other key, which a plain object without that own key reads as undefined.
func InheritedText(key string) (text string, ok bool) {
	if key == "__proto__" {
		return "[object Object]", true
	}
	if name, isMethod := inheritedMethods[key]; isMethod {
		return "function " + name + "() { [native code] }", true
	}
	return "", false
}
