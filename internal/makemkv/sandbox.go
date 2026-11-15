package makemkv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// appIDRE matches a Flatpak app ID such as com.makemkv.MakeMKV.
var appIDRE = regexp.MustCompile(`\b[A-Za-z][\w-]*(?:\.[\w-]+){2,}\b`)

// FlatpakAppOf returns the Flatpak app that the makemkvcon setting runs,
// or "" if it doesn't run one. It recognises a `flatpak run ... APP`
// command line and a small wrapper script that calls one.
func FlatpakAppOf(bin string) string {
	a := argv(bin)
	if filepath.Base(a[0]) == "flatpak" {
		for i := len(a) - 1; i > 0; i-- {
			if !strings.HasPrefix(a[i], "-") && a[i] != "run" {
				return a[i]
			}
		}
		return ""
	}
	fi, err := os.Stat(a[0])
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > 64*1024 {
		return "" // not a wrapper script (makemkvcon itself is much bigger)
	}
	body, err := os.ReadFile(a[0])
	if err != nil || bytes.IndexByte(body, 0) >= 0 {
		return ""
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || !strings.Contains(line, "flatpak") {
			continue
		}
		if _, rest, ok := strings.Cut(line, "flatpak"); ok && strings.Contains(rest, "run") {
			if id := appIDRE.FindString(rest); id != "" {
				return id
			}
		}
	}
	return ""
}

// CheckSandbox checks that MakeMKV can write into dir. It only matters when
// MakeMKV runs as a Flatpak: a Flatpak can only use the folders it has been
// given, and writing anywhere else goes into its sandbox and is lost when
// it exits, so every rip would "finish" without a file. It writes a test
// file into dir from inside the sandbox and looks for it outside.
func CheckSandbox(ctx context.Context, bin, dir string) error {
	app := FlatpakAppOf(bin)
	if app == "" {
		return nil
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return nil // a missing folder (drive not mounted) is reported when ripping
	}
	fix := fmt.Sprintf("allow it with:\n  flatpak override --user --filesystem=%s %s", dir, app)
	probe := filepath.Join(dir, ".blurip-sandbox-check-"+strconv.Itoa(os.Getpid()))
	os.Remove(probe)
	defer os.Remove(probe)

	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "flatpak", "run", "--command=sh", app, "-c", `: > "$1"`, "sh", probe)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return fmt.Errorf("checking that MakeMKV (Flatpak %s) can use %s: %w", app, dir, ctx.Err())
	}
	if _, err := os.Stat(probe); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	why := "a test file it wrote there never arrived"
	if runErr != nil {
		why = fmt.Sprintf("it couldn't write there (%v: %s)", runErr, firstLine(stderr.String()))
	}
	return fmt.Errorf("MakeMKV runs as a Flatpak (%s) and can't use the output folder %s: %s.\nEvery rip would fail with \"produced no MKV file\"; %s", app, dir, why, fix)
}
