package makemkv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"time"
)

// commonPaths are checked after $PATH when looking for makemkvcon.
var commonPaths = []string{
	"/usr/bin/makemkvcon",
	"/usr/local/bin/makemkvcon",
	"/opt/makemkv/bin/makemkvcon",
}

// Find returns the first usable makemkvcon: saved (if non-empty), then $PATH,
// then common install locations.
func Find(saved string) (string, error) {
	var candidates []string
	if saved != "" {
		candidates = append(candidates, saved)
	}
	if p, err := exec.LookPath("makemkvcon"); err == nil {
		candidates = append(candidates, p)
	}
	candidates = append(candidates, commonPaths...)
	for _, c := range candidates {
		if isExecutable(c) {
			return c, nil
		}
	}
	return "", errors.New("makemkvcon not found")
}

func isExecutable(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

var versionRE = regexp.MustCompile(`MakeMKV v\S+(?: \w+\([^)]*\))?`)

// Version runs makemkvcon and returns its version banner, e.g.
// "MakeMKV v2.0.0 linux(x64-release)". It is also the check that path really
// is a working makemkvcon.
func Version(ctx context.Context, path string) (string, error) {
	if !isExecutable(path) {
		return "", fmt.Errorf("%s is not an executable file", path)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// makemkvcon prints its "MakeMKV vX started" banner before handling
	// arguments, so --version works even though it is not a real option.
	out, _ := exec.CommandContext(ctx, path, "--version").CombinedOutput()
	if v := versionRE.Find(out); v != nil {
		return string(v), nil
	}
	if ctx.Err() != nil {
		return "", fmt.Errorf("%s did not respond within 30s", path)
	}
	return "", fmt.Errorf("%s does not look like makemkvcon (no MakeMKV version in its output)", path)
}

// ListDrives returns the drives makemkvcon can see that actually exist.
func ListDrives(ctx context.Context, bin string) ([]Drive, error) {
	info, err := RunInfo(ctx, bin, ListDrivesSource, nil)
	if err != nil {
		return nil, err
	}
	var out []Drive
	for _, d := range info.Drives {
		if d.Exists() {
			out = append(out, d)
		}
	}
	return out, nil
}
