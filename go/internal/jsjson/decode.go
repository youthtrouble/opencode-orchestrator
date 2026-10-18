package jsjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// ErrTrailingData reports input that holds more than one JSON value, which JSON.parse rejects.
var ErrTrailingData = errors.New("jsjson: unexpected data after the top-level value")

// Unmarshal parses data the way JSON.parse does and returns a tree of jsjson values. Any
// value is accepted at the top level; duplicate keys keep their first position and take the
// last value; every number becomes a float64 (so integers beyond 2^53 lose precision and
// out-of-range magnitudes become ±Infinity or 0, exactly as in JS).
//
// Grammar checks come from encoding/json's tokenizer, so syntax errors are *json.SyntaxError
// (wrapped). One documented divergence: a lone UTF-16 surrogate escape such as "\ud800"
// decodes to U+FFFD, because a Go string cannot hold it.
func Unmarshal(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	value, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		if err != nil {
			return nil, fmt.Errorf("jsjson: %w", err)
		}
		return nil, ErrTrailingData
	}
	return value, nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	token, err := nextToken(dec)
	if err != nil {
		return nil, err
	}
	switch token := token.(type) {
	case json.Delim:
		if token == '{' {
			return decodeObject(dec)
		}
		return decodeArray(dec)
	case json.Number:
		return parseNumber(token)
	default: // string, bool or nil
		return token, nil
	}
}

func decodeObject(dec *json.Decoder) (*Object, error) {
	object := NewObject()
	for dec.More() {
		keyToken, err := nextToken(dec)
		if err != nil {
			return nil, err
		}
		value, err := decodeValue(dec)
		if err != nil {
			return nil, err
		}
		// The tokenizer only yields strings in key position.
		object.Set(keyToken.(string), value)
	}
	return object, consumeClosingDelim(dec)
}

func decodeArray(dec *json.Decoder) ([]any, error) {
	items := []any{}
	for dec.More() {
		item, err := decodeValue(dec)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, consumeClosingDelim(dec)
}

func consumeClosingDelim(dec *json.Decoder) error {
	_, err := nextToken(dec)
	return err
}

// nextToken reads the token a value or container still needs, so running out of input is
// io.ErrUnexpectedEOF (truncated JSON), never a clean io.EOF.
func nextToken(dec *json.Decoder) (json.Token, error) {
	token, err := dec.Token()
	if err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	if err != nil {
		return nil, fmt.Errorf("jsjson: %w", err)
	}
	return token, nil
}

// parseNumber converts a grammar-checked JSON number. A range error is not a failure: JS
// turns 1e400 into Infinity, and ParseFloat already returns ±Inf for it.
func parseNumber(n json.Number) (float64, error) {
	f, err := strconv.ParseFloat(string(n), 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return 0, fmt.Errorf("jsjson: %w", err)
	}
	return f, nil
}
