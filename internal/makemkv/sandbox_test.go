package makemkv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFlatpakAppOf(t *testing.T) {
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "makemkvcon")
	os.WriteFile(wrapper, []byte("#!/bin/bash\n# flatpak run org.example.NotThis\nexec flatpak run --command=makemkvcon com.makemkv.MakeMKV \"$@\"\n"), 0o755)
	plain := filepath.Join(dir, "plain")
	os.WriteFile(plain, []byte("#!/bin/sh\nexec /usr/bin/makemkvcon \"$@\"\n"), 0o755)
	for bin, want := range map[string]string{
		FlatpakCommand:                   "com.makemkv.MakeMKV",
		"/usr/bin/flatpak run my.app.Id": "my.app.Id",
		wrapper:                          "com.makemkv.MakeMKV",
		plain:                            "",
		"/nonexistent/makemkvcon":        "",
	} {
		if got := FlatpakAppOf(bin); got != want {
			t.Errorf("FlatpakAppOf(%q) = %q, want %q", bin, got, want)
		}
	}
}

// fakeFlatpak puts a `flatpak` on the PATH that runs `run --command=sh APP
// -c SCRIPT sh FILE` either for real (the folder is shared) or with FILE
// redirected into a private folder (the sandbox can't see it).
func fakeFlatpak(t *testing.T, shared bool) {
	t.Helper()
	bin := t.TempDir()
	private := t.TempDir()
	body := "#!/bin/sh\nshift 3\n" // drop: run --command=sh APP
	if shared {
		body += `exec sh "$@"` + "\n"
	} else {
		body += `script="$2"; file="$4"; exec sh -c "$script" sh "` + private + `/$(basename "$file")"` + "\n"
	}
	os.WriteFile(filepath.Join(bin, "flatpak"), []byte(body), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestCheckSandbox(t *testing.T) {
	out := t.TempDir()
	fakeFlatpak(t, true)
	if err := CheckSandbox(context.Background(), FlatpakCommand, out); err != nil {
		t.Errorf("shared folder: %v", err)
	}
	if left, _ := os.ReadDir(out); len(left) != 0 {
		t.Errorf("test file left behind: %v", left)
	}

	fakeFlatpak(t, false)
	err := CheckSandbox(context.Background(), FlatpakCommand, out)
	if err == nil || !strings.Contains(err.Error(), "flatpak override --user --filesystem="+out+" com.makemkv.MakeMKV") {
		t.Errorf("private folder: %v", err)
	}
	// Not a Flatpak, or no folder yet: nothing to check.
	if err := CheckSandbox(context.Background(), "/usr/bin/makemkvcon", out); err != nil {
		t.Error(err)
	}
	if err := CheckSandbox(context.Background(), FlatpakCommand, filepath.Join(out, "missing")); err != nil {
		t.Error(err)
	}
}
