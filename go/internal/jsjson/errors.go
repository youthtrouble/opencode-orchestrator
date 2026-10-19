package jsjson

import (
	"errors"
	"fmt"
)

// TypeError is a TypeError the JS throws, with V8's message.
type TypeError struct {
	Message string
}

func (e *TypeError) Error() string { return e.Message }

// RangeError is a RangeError the JS throws, with V8's message.
type RangeError struct {
	Message string
}

func (e *RangeError) Error() string { return e.Message }

// ErrInvalidTime is `new Date(NaN).toISOString()`.
var ErrInvalidTime = &RangeError{"Invalid time value"}

// propertyAccess is what a strict-mode property access on a value without properties did.
type propertyAccess int

const (
	accessCreate propertyAccess = iota // an assignment to a primitive
	accessSet                          // an assignment to null
	accessRead                         // a read of null
)

// PropertyError is the TypeError strict-mode JS throws for a property access on a value that
// cannot take it: an assignment to a primitive (`(5).lane = 1`), or an assignment to or a read
// of null.
type PropertyError struct {
	access propertyAccess
	Key    string
	Target any
}

// CannotAssign is the TypeError of `target[key] = value` on null or a primitive (a number, a
// string or a boolean).
func CannotAssign(target any, key string) *PropertyError {
	if target == nil {
		return &PropertyError{accessSet, key, nil}
	}
	return &PropertyError{accessCreate, key, target}
}

// CannotReadOfNull is the TypeError of `null[key]`.
func CannotReadOfNull(key string) *PropertyError {
	return &PropertyError{accessRead, key, nil}
}

func (e *PropertyError) Error() string {
	switch e.access {
	case accessSet:
		return fmt.Sprintf("Cannot set properties of null (setting '%s')", e.Key)
	case accessRead:
		return fmt.Sprintf("Cannot read properties of null (reading '%s')", e.Key)
	}
	return fmt.Sprintf("Cannot create property '%s' on %s '%s'", e.Key, typeOf(e.Target), TemplateString(e.Target))
}

// typeOf is the JS typeof of a primitive JSON value.
func typeOf(v any) string {
	switch v.(type) {
	case float64:
		return "number"
	case string:
		return "string"
	case bool:
		return "boolean"
	}
	return "object"
}

// ErrorClass is the JS class of the errors this package models ("TypeError", "RangeError"),
// and false for any other error.
func ErrorClass(err error) (string, bool) {
	var property *PropertyError
	var typeError *TypeError
	var rangeError *RangeError
	switch {
	case errors.As(err, &property), errors.As(err, &typeError):
		return "TypeError", true
	case errors.As(err, &rangeError):
		return "RangeError", true
	}
	return "", false
}

// ErrorString is String(err): the class, ": " and the message ("Error" for an error this
// package does not model).
func ErrorString(err error) string {
	class, ok := ErrorClass(err)
	if !ok {
		class = "Error"
	}
	return class + ": " + err.Error()
}
