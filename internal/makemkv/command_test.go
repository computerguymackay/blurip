package makemkv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArgv(t *testing.T) {
	spaced := filepath.Join(t.TempDir(), "Make MKV")
	os.WriteFile(spaced, []byte("#!/bin/sh\n"), 0o755)
	for bin, want := range map[string]string{
		"/usr/bin/makemkvcon": "/usr/bin/makemkvcon",
		FlatpakCommand:        "flatpak|run|--command=makemkvcon|com.makemkv.MakeMKV",
		spaced:                spaced, // an existing file is never split
	} {
		if got := strings.Join(argv(bin), "|"); got != want {
			t.Errorf("argv(%q) = %q, want %q", bin, got, want)
		}
	}
}

// A wrapper script (as used for MakeMKV's Flatpak) works like makemkvcon.
func TestVersionThroughWrapperScript(t *testing.T) {
	real := writeScript(t, `cat `+mustAbs(t, "testdata/drives.txt")+"\n")
	wrapper := filepath.Join(t.TempDir(), "makemkvcon")
	os.WriteFile(wrapper, []byte("#!/bin/bash\nexec "+real+` "$@"`+"\n"), 0o755)
	if v, err := Version(context.Background(), wrapper); err != nil || !strings.HasPrefix(v, "MakeMKV v2.0.0") {
		t.Errorf("Version = %q, %v", v, err)
	}
}

// The setting may be a command line, like FlatpakCommand.
func TestVersionCommandLine(t *testing.T) {
	script := filepath.Join(t.TempDir(), "mk.sh")
	os.WriteFile(script, []byte(`cat `+mustAbs(t, "testdata/drives.txt")+"\n"), 0o644) // not executable: run by sh
	if v, err := Version(context.Background(), "/bin/sh "+script); err != nil || !strings.HasPrefix(v, "MakeMKV v2.0.0") {
		t.Errorf("Version = %q, %v", v, err)
	}
	ripper := filepath.Join(t.TempDir(), "rip.sh")
	os.WriteFile(ripper, []byte(`for a; do out="$a"; done; printf x > "$out/t.mkv"`+"\n"), 0o644)
	out := t.TempDir()
	if _, err := Rip(context.Background(), RipOptions{Bin: "/bin/sh " + ripper, Source: "dev:/dev/sr0", TitleID: 1, OutDir: out}); err != nil {
		t.Fatalf("Rip through a command line: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "t.mkv")); err != nil {
		t.Error(err)
	}
}

func TestCheckExplains(t *testing.T) {
	dir := t.TempDir()
	noExec := filepath.Join(dir, "makemkvcon")
	os.WriteFile(noExec, []byte("#!/bin/bash\n"), 0o644)
	noShebang := filepath.Join(dir, "noshebang")
	os.WriteFile(noShebang, []byte("flatpak run --command=makemkvcon com.makemkv.MakeMKV \"$@\"\n"), 0o755)

	for bin, want := range map[string]string{
		noExec:                     "chmod +x " + noExec,
		dir:                        "is a folder",
		"/nonexistent/makemkvcon":  "doesn't exist",
		"no-such-program-anywhere": "no such program on the PATH",
	} {
		if err := Check(bin); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Check(%q) = %v, want %q", bin, err, want)
		}
	}
	// Linux won't run a script without a #! line itself.
	if _, err := Version(context.Background(), noShebang); err == nil || !strings.Contains(err.Error(), "#!/bin/bash") {
		t.Errorf("Version(no #! line) = %v", err)
	}
}

func TestResolve(t *testing.T) {
	if got, err := Resolve("sh"); err != nil || !filepath.IsAbs(got) {
		t.Errorf("Resolve(sh) = %q, %v", got, err)
	}
	if got, err := Resolve(" /bin/sh -c x "); err != nil || got != "/bin/sh -c x" {
		t.Errorf("Resolve(command) = %q, %v", got, err)
	}
}
