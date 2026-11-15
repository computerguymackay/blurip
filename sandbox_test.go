package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/computerguymackay/blurip/internal/config"
	"github.com/computerguymackay/blurip/internal/makemkv"
	"github.com/computerguymackay/blurip/internal/prompt"
)

// fakeFlatpakOverride puts a `flatpak` on the PATH whose sandbox can't see
// any folder until `flatpak override` has been run. It returns the file
// the override's arguments are written to.
func fakeFlatpakOverride(t *testing.T) string {
	t.Helper()
	bin, private := t.TempDir(), t.TempDir()
	allowed := filepath.Join(bin, "allowed")
	body := `#!/bin/sh
if [ "$1" = override ]; then echo "$@" > "` + allowed + `"; exit 0; fi
shift 3
if [ -e "` + allowed + `" ]; then exec sh "$@"; fi
exec sh -c "$2" sh "` + private + `/$(basename "$4")"
`
	os.WriteFile(filepath.Join(bin, "flatpak"), []byte(body), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return allowed
}

func TestCheckSandboxOffersToAllow(t *testing.T) {
	allowed := fakeFlatpakOverride(t)
	cfg := config.Config{MakeMKVCon: makemkv.FlatpakCommand, OutputDir: t.TempDir()}
	var out bytes.Buffer
	ask := prompt.NewPrompter(strings.NewReader("\n"), &out) // Enter = yes
	if err := checkSandbox(context.Background(), cfg, &out, ask); err != nil {
		t.Fatalf("%v\n%s", err, &out)
	}
	got, _ := os.ReadFile(allowed)
	if want := "override --user --filesystem=" + cfg.OutputDir + " com.makemkv.MakeMKV"; strings.TrimSpace(string(got)) != want {
		t.Errorf("ran flatpak %q, want %q", got, want)
	}
	if !strings.Contains(out.String(), "Allow it now? (runs: flatpak override --user --filesystem=") ||
		!strings.Contains(out.String(), "Done: MakeMKV can now use the output folder.") {
		t.Errorf("output:\n%s", &out)
	}
}

func TestCheckSandboxDeclinedOrNoTerminal(t *testing.T) {
	allowed := fakeFlatpakOverride(t)
	cfg := config.Config{MakeMKVCon: makemkv.FlatpakCommand, OutputDir: t.TempDir()}
	var out bytes.Buffer
	err := checkSandbox(context.Background(), cfg, &out, prompt.NewPrompter(strings.NewReader("n\n"), &out))
	if err == nil || !strings.Contains(err.Error(), "flatpak override --user") {
		t.Errorf("declined: err = %v", err)
	}
	// No terminal: nothing is asked, the error says what to run.
	if err := checkSandbox(context.Background(), cfg, &out, nil); err == nil {
		t.Error("no terminal: expected the error")
	}
	if _, err := os.Stat(allowed); err == nil {
		t.Error("flatpak override ran without a yes")
	}
}
