package flipcmd

import "github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"

// Status is what `status` prints (indent 2): flip.json's value, or {state: "never flipped"}
// when it is unreadable, unparsable or falsy (0, "", false, null). It never writes.
func (p Project) Status() any {
	if record := p.ReadFlip(); jsjson.Truthy(record) {
		return record
	}
	never := jsjson.NewObject()
	never.Set("state", "never flipped")
	return never
}
