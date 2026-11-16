// Package handbrake drives HandBrakeCLI: finding it, listing its presets
// (including the user's own from the HandBrake app) and encoding a file.
package handbrake

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/computerguymackay/blurip/internal/makemkv"
)

// FlatpakApp is HandBrake's app ID on Flathub.
const FlatpakApp = "fr.handbrake.ghb"

// FlatpakCommand runs the HandBrakeCLI inside HandBrake's Flatpak.
const FlatpakCommand = "flatpak run --command=HandBrakeCLI " + FlatpakApp

// commonPaths are checked after $PATH when looking for HandBrakeCLI.
var commonPaths = []string{"/usr/bin/HandBrakeCLI", "/usr/local/bin/HandBrakeCLI"}

// Find returns the first usable HandBrakeCLI: saved (if non-empty), then
// $PATH, then common install locations, then HandBrake's Flatpak.
func Find(saved string) (string, error) {
	var candidates []string
	if saved != "" {
		candidates = append(candidates, saved)
	}
	if p, err := exec.LookPath("HandBrakeCLI"); err == nil {
		candidates = append(candidates, p)
	}
	candidates = append(candidates, commonPaths...)
	for _, c := range candidates {
		if makemkv.Check(c) == nil {
			return c, nil
		}
	}
	if flatpakInstalled() {
		return FlatpakCommand, nil
	}
	return "", errors.New("HandBrakeCLI not found")
}

func flatpakInstalled() bool {
	if _, err := exec.LookPath("flatpak"); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "flatpak", "info", FlatpakApp).Run() == nil
}

func commandContext(ctx context.Context, bin string, args ...string) *exec.Cmd {
	a := makemkv.Argv(bin)
	return exec.CommandContext(ctx, a[0], append(a[1:], args...)...)
}

var versionRE = regexp.MustCompile(`HandBrake \d[\w.-]*`)

// Version checks that bin is a working HandBrakeCLI and returns its
// version, e.g. "HandBrake 1.8.2".
func Version(ctx context.Context, bin string) (string, error) {
	if err := makemkv.Check(bin); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	out, err := commandContext(ctx, bin, "--version").CombinedOutput()
	if v := versionRE.Find(out); v != nil {
		return string(v), nil
	}
	if err != nil {
		return "", fmt.Errorf("%s didn't work as HandBrakeCLI: %w: %s", bin, makemkv.ExplainStartError(err), firstLine(string(out)))
	}
	return "", fmt.Errorf("%s does not look like HandBrakeCLI; it printed: %s", bin, firstLine(string(out)))
}

// Resolve checks the HandBrakeCLI setting and returns it in the form to
// save (see makemkv.Resolve).
func Resolve(bin string) (string, error) { return makemkv.Resolve(bin) }

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

// lastLines returns the last n non-empty lines of b, joined with " / ".
func lastLines(b []byte, n int) string {
	var keep []string
	for _, l := range strings.Split(string(bytes.ReplaceAll(b, []byte("\r"), []byte("\n"))), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			keep = append(keep, l)
		}
	}
	if len(keep) > n {
		keep = keep[len(keep)-n:]
	}
	return strings.Join(keep, " / ")
}

var execCommand = exec.Command
