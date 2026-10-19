package policy

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
)

func parse(t *testing.T, text string) any {
	t.Helper()
	v, err := jsjson.Unmarshal([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func marshal(t *testing.T, v any) string {
	t.Helper()
	data, err := jsjson.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestEffectiveOverridesKeepBasePositionAndDropTheComment(t *testing.T) {
	file := filepath.Join(t.TempDir(), "policy.json")
	registry := jsjson.Path(parse(t, `{"autonomy":{"$comment":"c","push_to_main":"auto","deploy":"ask"}}`), "autonomy")
	if got := marshal(t, Effective(registry, file)); got != `{"push_to_main":"auto","deploy":"ask"}` {
		t.Errorf("no file: %s", got)
	}
	for content, want := range map[string]string{
		`{"deploy":"auto","new":1,"3":"i"}`: `{"3":"i","push_to_main":"auto","deploy":"auto","new":1}`,
		`not json`:                          `{"push_to_main":"auto","deploy":"ask"}`,
		`null`:                              `{"push_to_main":"auto","deploy":"ask"}`,
		`"ab"`:                              `{"0":"a","1":"b","push_to_main":"auto","deploy":"ask"}`,
		`{"__proto__":{"x":1}}`:             `{"push_to_main":"auto","deploy":"ask","__proto__":{"x":1}}`,
	} {
		if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := marshal(t, Effective(registry, file)); got != want {
			t.Errorf("file %s: effective %s, want %s", content, got, want)
		}
	}
	if err := os.WriteFile(file, []byte(`{"deploy":"auto"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := marshal(t, Effective(nil, file)); got != `{"deploy":"auto"}` {
		t.Errorf("registry without autonomy: %s", got)
	}
	if got := marshal(t, Effective("ab", file)); got != `{"0":"a","1":"b","deploy":"auto"}` {
		t.Errorf("autonomy holding a string: %s", got)
	}
}

func TestSetFollowsJSAssignment(t *testing.T) {
	cases := []struct {
		cur   string
		pairs []string
		want  string
	}{
		{`{}`, []string{"a=b=c", "=x", "k="}, `{"a":"b","":"x","k":""}`},
		{`{"a":1}`, nil, `{"a":1}`},
		{`{}`, []string{"__proto__=x", "force_push=never"}, `{"force_push":"never"}`},
		{`{"__proto__":{"o":1}}`, []string{"__proto__=x"}, `{"__proto__":"x"}`},
		{`{"force_push":"auto"}`, []string{"force_push=never"}, `{"force_push":"never"}`},
	}
	for _, c := range cases {
		got, err := Set(parse(t, c.cur), c.pairs)
		if err != nil || marshal(t, got) != c.want {
			t.Errorf("Set(%s, %q) = %v, %v; want %s", c.cur, c.pairs, got, err, c.want)
		}
	}
}

func TestSetRefusals(t *testing.T) {
	var refused *RefusedError
	if _, err := Set(parse(t, `{}`), []string{"deploy=auto", "x=never"}); !errors.As(err, &refused) || err.Error() != "refusing x=never" {
		t.Errorf("never: %v", err)
	}
	for _, c := range []struct{ cur, pair string }{{`{}`, "force_push=auto"}, {`{"force_push":"auto"}`, "deploy=auto"}, {`{"force_push":null}`, "a=b"}} {
		if _, err := Set(parse(t, c.cur), []string{c.pair}); !errors.Is(err, ErrForcePush) {
			t.Errorf("%s %s: %v, want ErrForcePush", c.cur, c.pair, err)
		}
	}
}

// The TypeError messages Node v26.5.1 prints for a policy.json holding a primitive (C1).
func TestSetOnPrimitivesFailsWithV8Messages(t *testing.T) {
	cases := []struct {
		cur   string
		pairs []string
		want  string
	}{
		{`null`, []string{"a=b"}, "Cannot set properties of null (setting 'a')"},
		{`null`, nil, "Cannot use 'in' operator to search for 'force_push' in null"},
		{`5`, []string{"a=b"}, "Cannot create property 'a' on number '5'"},
		{`5`, []string{"__proto__=x"}, "Cannot use 'in' operator to search for 'force_push' in 5"},
		{`true`, []string{"length=1"}, "Cannot create property 'length' on boolean 'true'"},
		{`"ab"`, []string{"1=x"}, "Cannot assign to read only property '1' of string 'ab'"},
		{`"ab"`, []string{"length=1"}, "Cannot assign to read only property 'length' of string 'ab'"},
		{`"ab"`, []string{"2=x"}, "Cannot create property '2' on string 'ab'"},
		{`"ab"`, []string{"01=x"}, "Cannot create property '01' on string 'ab'"},
		{`"ab"`, nil, "Cannot use 'in' operator to search for 'force_push' in ab"},
	}
	for _, c := range cases {
		var typeError *TypeError
		if _, err := Set(parse(t, c.cur), c.pairs); !errors.As(err, &typeError) || err.Error() != c.want {
			t.Errorf("Set(%s, %q) error %v, want %q", c.cur, c.pairs, err, c.want)
		}
	}
	if _, err := Set(parse(t, `[1]`), []string{"a=b"}); err == nil {
		t.Error("an array policy.json should fail (PE-6)")
	}
}
