// Package cliargs reproduces the argv parsers of the JS scripts, one function per variant,
// because each script parses differently (whether a value may start with "--", whether bare
// keys are flags, which keys repeat) and orch must accept exactly the same command lines.
package cliargs

// Opts mirrors the JS `opts` object built by the argv loops: each key holds either a string
// value or the boolean true (a key given without a value). A later occurrence of a key
// replaces an earlier one, as a JS property assignment does.
type Opts map[string]Opt

// Opt is one entry of Opts. Flag is true when the key was given without a value.
type Opt struct {
	Value string
	Flag  bool
}

func valueOpt(value string) Opt { return Opt{Value: value} }

var flagOpt = Opt{Flag: true}

// Value returns the string value of key; ok is false when the key is absent or a bare flag.
func (o Opts) Value(key string) (value string, ok bool) {
	opt, present := o[key]
	if !present || opt.Flag {
		return "", false
	}
	return opt.Value, true
}

// IsFlag reports whether key was given without a value (JS `opts[key] === true`).
func (o Opts) IsFlag(key string) bool {
	return o[key].Flag
}

// Truthy mirrors JS truthiness of `opts[key]`: a bare flag or a non-empty value.
func (o Opts) Truthy(key string) bool {
	opt := o[key]
	return opt.Flag || opt.Value != ""
}
