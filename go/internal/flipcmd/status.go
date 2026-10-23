package flipcmd

import "github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"

// Status is what `status` prints (indent 2): flip.json's value, or {state: "never flipped"}
// when absent or falsy (0, "", false, null). Unreadable state is explicit. It never writes.
func (p Project) Status() any {
	record, err := p.Read()
	if err != nil {
		unreadable := jsjson.NewObject()
		unreadable.Set("state", "unreadable")
		unreadable.Set("reason", err.(*StateError).GateReason())
		return unreadable
	}
	if jsjson.Truthy(record.Value) {
		return record.Value
	}
	never := jsjson.NewObject()
	never.Set("state", "never flipped")
	return never
}
