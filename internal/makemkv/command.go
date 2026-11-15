package makemkv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// FlatpakApp is MakeMKV's app ID on Flathub.
const FlatpakApp = "com.makemkv.MakeMKV"

// FlatpakCommand runs the makemkvcon inside MakeMKV's Flatpak (Bazzite,
// Fedora Silverblue and other systems that install apps as Flatpaks).
const FlatpakCommand = "flatpak run --command=makemkvcon " + FlatpakApp

// argv splits the makemkvcon setting into a program and its first
// arguments. The setting is normally a path to makemkvcon or to a wrapper
// script, but may be a command line such as FlatpakCommand. A path to an
// existing file is never split, even if it contains spaces.
func argv(bin string) []string {
	if fileExists(bin) {
		return []string{bin}
	}
	if f := strings.Fields(bin); len(f) > 1 {
		return f
	}
	return []string{bin}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// command builds the command that runs makemkvcon (see argv) with args.
func command(bin string, args ...string) *exec.Cmd {
	a := argv(bin)
	return exec.Command(a[0], append(a[1:], args...)...)
}

// commandContext is command, killed when ctx is cancelled.
func commandContext(ctx context.Context, bin string, args ...string) *exec.Cmd {
	a := argv(bin)
	return exec.CommandContext(ctx, a[0], append(a[1:], args...)...)
}

// Check reports why the makemkvcon setting can't be run, or nil if it can.
func Check(bin string) error {
	a := argv(bin)
	prog := a[0]
	if !strings.Contains(prog, "/") {
		if _, err := exec.LookPath(prog); err != nil {
			return fmt.Errorf("%s: no such program on the PATH (give the full path, e.g. /home/you/bin/%s)", prog, prog)
		}
		return nil
	}
	fi, err := os.Stat(prog)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("%s doesn't exist", prog)
	case err != nil:
		return err
	case fi.IsDir():
		return fmt.Errorf("%s is a folder, not a program", prog)
	case fi.Mode().Perm()&0o111 == 0:
		return fmt.Errorf("%s isn't executable; make it so with: chmod +x %s", prog, prog)
	}
	return nil
}

// Resolve checks the makemkvcon setting and returns it in the form to
// save: a bare program name ("makemkvcon") becomes its full path.
func Resolve(bin string) (string, error) {
	bin = strings.TrimSpace(bin)
	if err := Check(bin); err != nil {
		return "", err
	}
	if a := argv(bin); len(a) == 1 && !strings.Contains(bin, "/") {
		p, err := exec.LookPath(bin)
		if err != nil {
			return "", err
		}
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		return p, nil
	}
	return bin, nil
}

// explainStartError turns "exec format error" (a script without a #! line)
// into advice.
func explainStartError(err error) error {
	if errors.Is(err, syscall.ENOEXEC) {
		return fmt.Errorf("%w: if it is a script, its first line must be #!/bin/bash", err)
	}
	return err
}

// flatpakInstalled reports whether MakeMKV is installed as a Flatpak.
func flatpakInstalled() bool {
	if _, err := exec.LookPath("flatpak"); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "flatpak", "info", FlatpakApp).Run() == nil
}
