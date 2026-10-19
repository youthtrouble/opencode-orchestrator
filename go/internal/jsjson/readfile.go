package jsjson

import (
	"os"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
)

// ReadFileOrNull is `try { return JSON.parse(fs.readFileSync(path, "utf8")) } catch { return
// null }`: any JSON value the file holds, or nil when it cannot be read (missing, a
// directory) or parsed (a BOM, bad JSON). Malformed UTF-8 is decoded as Node decodes it.
func ReadFileOrNull(path string) any {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	value, err := Unmarshal([]byte(jsstr.DecodeUTF8(data)))
	if err != nil {
		return nil
	}
	return value
}
