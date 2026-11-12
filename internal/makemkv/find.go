package makemkv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
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

// Version checks that path is a working makemkvcon and returns its version,
// e.g. "MakeMKV v2.0.0 linux(x64-release)".
//
// It runs the drive listing (`-r info disc:9999`) rather than a version
// option, because makemkvcon has none; the listing always starts with a
// version message. It takes a few seconds because MakeMKV checks the drives.
func Version(ctx context.Context, path string) (string, error) {
	if !isExecutable(path) {
		return "", fmt.Errorf("%s is not an executable file", path)
	}
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	var raw bytes.Buffer
	info, err := RunInfo(ctx, path, ListDrivesSource, &raw)
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("%s did not respond within %s", path, versionTimeout)
	}
	if err != nil {
		return "", fmt.Errorf("%s didn't work as makemkvcon: %w", path, err)
	}
	if info.Version != "" {
		return info.Version, nil
	}
	if v := versionRE.Find(raw.Bytes()); v != nil {
		return string(v), nil
	}
	if len(info.Drives) > 0 || len(info.Messages) > 0 {
		return "MakeMKV (version unknown)", nil // speaks robot mode, so it is makemkvcon
	}
	return "", fmt.Errorf("%s does not look like makemkvcon; it printed: %s", path, firstLine(raw.String()))
}

const versionTimeout = 60 * time.Second

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	if s == "" {
		return "(nothing)"
	}
	return s
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
