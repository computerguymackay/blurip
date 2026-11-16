package setup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/computerguymackay/blurip/internal/config"
	"github.com/computerguymackay/blurip/internal/handbrake"
	"github.com/computerguymackay/blurip/internal/prompt"
)

func encodeEnv(t *testing.T) EncodeEnv {
	f, err := os.Open("../handbrake/testdata/preset-list.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cats := handbrake.ParsePresetList(f)
	return EncodeEnv{
		FindHandBrake: func(string) (string, error) { return handbrake.FlatpakCommand, nil },
		Version:       func(context.Context, string) (string, error) { return "HandBrake 1.8.2", nil },
		ListPresets:   func(context.Context, string) ([]handbrake.Category, error) { return cats, nil },
		HomeDir:       t.TempDir(),
	}
}

func TestRunEncode(t *testing.T) {
	env := encodeEnv(t)
	// Use the Flatpak; DVD: keep the suggested General / Fast 1080p30;
	// Blu-ray: My Presets (1) / 720pFast (1); create both folders; delete rips.
	input := "\n" + "\n\n" + "1\n1\n" + "\ny\n" + "\ny\n" + "\n"
	var out bytes.Buffer
	cfg, err := RunEncode(context.Background(), prompt.NewPrompter(strings.NewReader(input), &out), env, config.Default())
	if err != nil {
		t.Fatalf("%v\n%s", err, &out)
	}
	if cfg.HandBrakeCLI != handbrake.FlatpakCommand || cfg.PresetDVD != "General/Fast 1080p30" || cfg.PresetBluRay != "My Presets/720pFast" ||
		cfg.MoviesDir != filepath.Join(env.HomeDir, "Videos", "Movies") || cfg.ShowsDir != filepath.Join(env.HomeDir, "Videos", "Shows") ||
		!cfg.DeleteAfterEncode || !cfg.EncodeReady() {
		t.Errorf("config = %+v\n%s", cfg, &out)
	}
	for _, want := range []string{"Preset for DVDs (now: General / Fast 1080p30)", "My Presets (1)  your own", "  1) 720pFast"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, &out)
		}
	}
}

func TestChoosePresetKeepsCurrent(t *testing.T) {
	env := encodeEnv(t)
	cats, _ := env.ListPresets(context.Background(), "")
	var out bytes.Buffer
	got, err := ChoosePreset(prompt.NewPrompter(strings.NewReader("\n\n"), &out), cats, "Preset", "My Presets/720pFast")
	if err != nil || got != "My Presets/720pFast" {
		t.Errorf("got %q, %v\n%s", got, err, &out)
	}
	// Another category, then a preset in it by number.
	got, _ = ChoosePreset(prompt.NewPrompter(strings.NewReader("Matroska\n1\n"), &out), cats, "Preset", "My Presets/720pFast")
	if !strings.HasPrefix(got, "Matroska/") {
		t.Errorf("got %q", got)
	}
}
