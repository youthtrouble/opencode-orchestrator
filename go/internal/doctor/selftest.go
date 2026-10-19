package doctor

import (
	"bytes"
	_ "embed"
	"fmt"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/registry"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/routing"
)

var (
	// jsonGolden holds what the JS serialiser is particular about (key order with an index-like
	// key, -0, 1e21, a small exponent, U+2028 and an astral character); jsonGoldenExpected is
	// Node's JSON.stringify(JSON.parse(jsonGolden)).
	//go:embed testdata/json-golden.json
	jsonGolden []byte
	//go:embed testdata/json-golden.expected
	jsonGoldenExpected []byte
	// greenUsage is a usage document with every feed green and far from reset.
	//go:embed testdata/green-usage.json
	greenUsage []byte
)

// SelfTest returns PE-18's in-process checks: the JSON round trip, the registry at
// registryFile resolving every role over an all-green usage document, and the executable
// path. It writes nothing and makes no network call.
func SelfTest(registryFile string, executable func() (string, error)) func() error {
	return func() error {
		if err := checkJSON(); err != nil {
			return fmt.Errorf("json: %w", err)
		}
		if err := checkRouting(registryFile); err != nil {
			return fmt.Errorf("routing: %w", err)
		}
		if _, err := executable(); err != nil {
			return fmt.Errorf("executable: %w", err)
		}
		return nil
	}
}

func checkJSON() error {
	value, err := jsjson.Unmarshal(jsonGolden)
	if err != nil {
		return err
	}
	got, err := jsjson.Marshal(value)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, jsonGoldenExpected) {
		return fmt.Errorf("re-serialised %s, want %s", got, jsonGoldenExpected)
	}
	return nil
}

func checkRouting(registryFile string) error {
	reg, err := registry.Load(registryFile)
	if err != nil {
		return err
	}
	value, err := jsjson.Unmarshal(greenUsage)
	if err != nil {
		return err
	}
	usage, _ := value.(*jsjson.Object)
	bothNative := map[string]bool{routing.Anthropic: true, routing.OpenAI: true}
	resolved := routing.ResolveRoles(usage, reg, time.Now(), bothNative)
	for _, a := range resolved.Roles {
		if a.Entry == nil {
			return fmt.Errorf("%s is PAUSED with every feed green", a.Role)
		}
	}
	return nil
}
