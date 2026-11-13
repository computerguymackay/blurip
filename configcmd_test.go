package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/computerguymackay/blurip/internal/config"
	"github.com/computerguymackay/blurip/internal/store"
)

func TestConfigSetGetDrive(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "b.db")
	s, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	s.ReplaceDrives(ctx, []store.Drive{{Device: "/dev/sr0"}, {Device: "/dev/sr1"}})
	s.Close()

	out := t.TempDir()
	for _, args := range [][]string{
		{"config", "set", "output_dir", out},
		{"config", "set", "split_by_type", "yes"},
		{"config", "set", "drive_mode", "parallel"},
		{"config", "drive", "sr0", "off"},
		{"config", "drive", "sr1", "name", "Blu-ray", "drive"},
		{"config", "drive", "BLU-RAY DRIVE", "on"},
		{"config", "get", "split_by_type"},
		{"config"},
	} {
		if err := run(append([]string{"-db", db}, args...)); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	for _, sub := range []string{"DVD", "Blu-ray"} {
		if fi, err := os.Stat(filepath.Join(out, sub)); err != nil || !fi.IsDir() {
			t.Errorf("%s folder not created: %v", sub, err)
		}
	}

	s, err = store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, err := config.Load(ctx, s)
	if err != nil || c.OutputDir != out || !c.SplitByType || c.DriveMode != config.DriveParallel {
		t.Errorf("config = %+v, %v", c, err)
	}
	drives, _ := s.Drives(ctx)
	if len(drives) != 2 || drives[0].Enabled || !drives[1].Enabled || drives[1].Nickname != "Blu-ray drive" {
		t.Errorf("drives = %+v", drives)
	}

	for _, args := range [][]string{
		{"config", "set", "output_dir", "relative"},
		{"config", "set", "output_dir", filepath.Join(out, "missing")},
		{"config", "set", "max_fill_percent", "0"},
		{"config", "set", "bogus", "1"},
		{"config", "drive", "/dev/sr9", "on"},
		{"config", "get", "bogus"},
		{"config", "drive", "sr1", "name", " "},
		{"config", "drive", "kitchen", "on"},
	} {
		if err := run(append([]string{"-db", db}, args...)); err == nil {
			t.Errorf("%v succeeded", args)
		}
	}
}

func TestResolveDrive(t *testing.T) {
	known := []store.Drive{{Device: "/dev/sr1", Nickname: "Blu-ray drive"}}
	for ref, want := range map[string]string{
		"/dev/sr0": "/dev/sr0", "sr0": "/dev/sr0", "sr1": "/dev/sr1", "blu-ray drive": "/dev/sr1",
	} {
		if got, err := resolveDrive(ref, known); err != nil || got != want {
			t.Errorf("resolveDrive(%q) = %q, %v; want %q", ref, got, err, want)
		}
	}
	if _, err := resolveDrive("kitchen", known); err == nil {
		t.Error("unknown nickname resolved")
	}
}
