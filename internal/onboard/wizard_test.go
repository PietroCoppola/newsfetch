package onboard

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/PietroCoppola/newsfetch/internal/config"
	"github.com/PietroCoppola/newsfetch/internal/defaults"
)

// TestDefaultInitAnswers_ValidatesCleanly pins the regression where the
// interactive --init wizard persisted zero-valued count/ticker fields.
// The wizard's form binds only Topics and Style, and renderConfigTOML
// persists count/ticker_marker/ticker_boxed unconditionally, so the
// wizard's starting Answers — written as-is, the "accept everything"
// path — must round-trip through config.Load + config.Validate with no
// warnings and no clamping.
func TestDefaultInitAnswers_ValidatesCleanly(t *testing.T) {
	a := defaultInitAnswers()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := WriteConfig(path, a); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	var buf bytes.Buffer
	got := config.Validate(cfg, config.FieldSources{Style: "config", Count: "config"}, &buf)
	if buf.Len() != 0 {
		t.Errorf("wizard-default config produced validation warnings: %s", buf.String())
	}
	if got.Count != defaults.Count {
		t.Errorf("Count = %d, want default %d", got.Count, defaults.Count)
	}
	if got.TickerMarker != defaults.TickerMarker {
		t.Errorf("TickerMarker = %q, want default %q", got.TickerMarker, defaults.TickerMarker)
	}
	if got.Style != defaults.Style {
		t.Errorf("Style = %q, want default %q", got.Style, defaults.Style)
	}
}
