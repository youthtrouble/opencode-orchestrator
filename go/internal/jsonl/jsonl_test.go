package jsonl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
)

func TestReadDropsUnparsableAndFalsyLines(t *testing.T) {
	file := filepath.Join(t.TempDir(), "x.jsonl")
	lines := []string{`x`, `0`, `-0`, `null`, `false`, `""`, ``, `1`, `"s"`, `[]`, `{}`, `{"a":1}` + "\r", "\ufeff{}"}
	if err := os.WriteFile(file, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	values, err := Read(file)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := jsjson.Marshal(values); string(got) != `[1,"s",[],{},{"a":1}]` {
		t.Errorf("read kept %s", got)
	}
}

func TestReadMissingFileIsEmpty(t *testing.T) {
	values, err := Read(filepath.Join(t.TempDir(), "absent.jsonl"))
	if err != nil || values != nil {
		t.Errorf("Read = %v, %v; want nil, nil", values, err)
	}
}

func TestAppendCreatesDirectoriesAndAppendsLines(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a", "b", "x.jsonl")
	for _, n := range []float64{1, 2} {
		entry := jsjson.NewObject()
		entry.Set("n", n)
		if err := Append(file, entry); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{\"n\":1}\n{\"n\":2}\n" {
		t.Errorf("file = %q", data)
	}
}
