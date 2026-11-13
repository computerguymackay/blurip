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
	if len(drives) != 2 || drives[0].Enabled || !drives[1].Enabled {
		t.Errorf("drives = %+v", drives)
	}

	for _, args := range [][]string{
		{"config", "set", "output_dir", "relative"},
		{"config", "set", "output_dir", filepath.Join(out, "missing")},
		{"config", "set", "max_fill_percent", "0"},
		{"config", "set", "bogus", "1"},
		{"config", "drive", "/dev/sr9", "on"},
		{"config", "get", "bogus"},
	} {
		if err := run(append([]string{"-db", db}, args...)); err == nil {
			t.Errorf("%v succeeded", args)
		}
	}
}
