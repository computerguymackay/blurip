package makemkv

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "makemkvcon")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestVersion(t *testing.T) {
	// Matches the real output captured from MakeMKV 2.0.0.
	bin := writeScript(t, "echo 'MakeMKV v2.0.0 linux(x64-release) started'\nexit 1\n")
	v, err := Version(context.Background(), bin)
	if err != nil || v != "MakeMKV v2.0.0 linux(x64-release)" {
		t.Errorf("Version = %q, %v", v, err)
	}
}

func TestVersionRejectsOtherPrograms(t *testing.T) {
	bin := writeScript(t, "echo hello\n")
	if _, err := Version(context.Background(), bin); err == nil {
		t.Error("expected error for a non-MakeMKV program")
	}
	if _, err := Version(context.Background(), "/nonexistent/makemkvcon"); err == nil {
		t.Error("expected error for a missing file")
	}
}

func TestFindPrefersSaved(t *testing.T) {
	bin := writeScript(t, "")
	t.Setenv("PATH", "")
	if got, err := Find(bin); err != nil || got != bin {
		t.Errorf("Find = %q, %v", got, err)
	}
}

func TestFindUsesPath(t *testing.T) {
	bin := writeScript(t, "")
	t.Setenv("PATH", filepath.Dir(bin))
	if got, err := Find("/nonexistent"); err != nil || got != bin {
		t.Errorf("Find = %q, %v", got, err)
	}
}

func TestListDrives(t *testing.T) {
	bin, _ := fakeMakeMKV(t, "drives.txt", 1)
	drives, err := ListDrives(context.Background(), bin)
	if err != nil || len(drives) != 2 || drives[1].Device != "/dev/sr1" {
		t.Errorf("ListDrives = %+v, %v", drives, err)
	}
}
