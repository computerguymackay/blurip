package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/computerguymackay/blurip/internal/config"
)

// probe runs the makemkvcon chosen in setup (a Flatpak command line, say),
// not just one on the PATH.
func TestProbeMakeMKVUsesSetting(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := filepath.Join(dir, "blurip.db")
	t.Setenv("PATH", dir) // no makemkvcon here
	bin := filepath.Join(dir, "my makemkv wrapper")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	s, cfg, err := openStore(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MakeMKVCon = bin
	if err := config.Save(ctx, s, cfg); err != nil {
		t.Fatal(err)
	}
	s.Close()

	got, err := probeMakeMKV(ctx, db, "")
	if err != nil || got != bin {
		t.Fatalf("probeMakeMKV = %q, %v; want the saved %q", got, err, bin)
	}
	if _, err := probeMakeMKV(ctx, db, filepath.Join(dir, "missing")); err == nil {
		t.Error("a missing -makemkvcon was accepted")
	}
}
