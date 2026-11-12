package makemkv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
	// Real `-r info disc:9999` output shape from MakeMKV 2.0.0; exits non-zero
	// ("Failed to open disc") like the real thing.
	bin, argsFile := fakeMakeMKV(t, "drives.txt", 1)
	v, err := Version(context.Background(), bin)
	if err != nil || v != "MakeMKV v2.0.0 linux(x64-release)" {
		t.Errorf("Version = %q, %v", v, err)
	}
	args, _ := os.ReadFile(argsFile)
	if got := strings.TrimSpace(string(args)); got != "-r --cache=1 info disc:9999" {
		t.Errorf("args = %q (must not rely on a --version option)", got)
	}
}

func TestVersionRejectsUnknownOptions(t *testing.T) {
	// Like makemkvcon given an option it doesn't know: usage text, no banner.
	bin := writeScript(t, `case "$*" in *disc:9999*) cat `+mustAbs(t, "testdata/drives.txt")+`;; *) echo "Use: makemkvcon [switches] Command [Parameters]"; exit 1;; esac`+"\n")
	if _, err := Version(context.Background(), bin); err != nil {
		t.Errorf("Version failed although the drive listing works: %v", err)
	}
}

func TestVersionUnknownButRobotOutput(t *testing.T) {
	bin := writeScript(t, `echo 'DRV:0,0,999,0,"DVD","","/dev/sr0"'`+"\n")
	if v, err := Version(context.Background(), bin); err != nil || v != "MakeMKV (version unknown)" {
		t.Errorf("Version = %q, %v", v, err)
	}
}

func TestVersionRejectsOtherPrograms(t *testing.T) {
	bin := writeScript(t, "echo hello\n")
	_, err := Version(context.Background(), bin)
	if err == nil || !strings.Contains(err.Error(), "it printed: hello") {
		t.Errorf("err = %v", err)
	}
	if _, err := Version(context.Background(), "/nonexistent/makemkvcon"); err == nil {
		t.Error("expected error for a missing file")
	}
	silent := writeScript(t, "echo oops >&2\nexit 1\n")
	if _, err := Version(context.Background(), silent); err == nil || !strings.Contains(err.Error(), "oops") {
		t.Errorf("err = %v, want stderr in message", err)
	}
}

func mustAbs(t *testing.T, p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return a
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
