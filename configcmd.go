package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/computerguymackay/blurip/internal/config"
	"github.com/computerguymackay/blurip/internal/store"
)

const configUsage = `Usage:
  blurip config                       Show all settings and drives
  blurip config get KEY               Show one setting
  blurip config set KEY VALUE         Change a setting
  blurip config drive DEVICE on|off   Use or stop using a configured drive

Settings:
  output_dir        Folder for finished rips (with split_by_type, the base folder)
  split_by_type     true: DVDs go to <output_dir>/DVD, Blu-rays to <output_dir>/Blu-ray
  rip_mode          defaults | ask
  eject             true | false
  max_fill_percent  1-100: never fill the output disk past this
  drive_mode        single | sequential | parallel
  max_concurrent    parallel mode: rips at once (0 = one per drive)
  makemkvcon_path   Path to makemkvcon
  title_mode        main_feature (the only choice for now)
  setup_complete    false makes blurip run setup again next time

A running blurip keeps the settings it started with; restart it to use new ones.
`

func runConfig(ctx context.Context, dbPath string, args []string) error {
	if len(args) == 0 {
		return showConfig(ctx, dbPath)
	}
	switch args[0] {
	case "-h", "help", "--help":
		fmt.Print(configUsage)
		return nil
	case "get":
		if len(args) != 2 {
			return usageError(configUsage, "usage: blurip config get KEY")
		}
		s, cfg, err := openStore(ctx, dbPath)
		if err != nil {
			return err
		}
		defer s.Close()
		v, ok := cfg.ToMap()[args[1]]
		if !ok {
			return fmt.Errorf("unknown setting %q (settings: %s)", args[1], strings.Join(config.Keys(), ", "))
		}
		fmt.Println(v)
		return nil
	case "set":
		if len(args) != 3 {
			return usageError(configUsage, "usage: blurip config set KEY VALUE")
		}
		return setConfig(ctx, dbPath, args[1], args[2])
	case "drive":
		if len(args) != 3 || (args[2] != "on" && args[2] != "off") {
			return usageError(configUsage, "usage: blurip config drive DEVICE on|off")
		}
		return setDrive(ctx, dbPath, args[1], args[2] == "on")
	}
	return usageError(configUsage, fmt.Sprintf("unknown config command %q", args[0]))
}

func usageError(usage, msg string) error {
	fmt.Fprint(os.Stderr, usage)
	return errors.New(msg)
}

func setConfig(ctx context.Context, dbPath, key, value string) error {
	s, cfg, err := openStore(ctx, dbPath)
	if err != nil {
		return err
	}
	defer s.Close()

	value, err = checkSettingValue(key, value)
	if err != nil {
		return err
	}
	next, err := cfg.Set(key, value)
	if err != nil {
		return err
	}
	if next.SplitByType && (key == config.KeySplitByType || key == config.KeyOutputDir) && next.OutputDir != "" {
		for _, kind := range []string{"DVD", "Blu-ray"} {
			if err := os.MkdirAll(next.DestDir(kind), 0o755); err != nil {
				return fmt.Errorf("creating the %s folder: %w", kind, err)
			}
		}
	}
	if err := config.Save(ctx, s, next); err != nil {
		return err
	}
	fmt.Printf("%s = %s\n", key, next.ToMap()[key])
	if next.SplitByType && (key == config.KeySplitByType || key == config.KeyOutputDir) {
		fmt.Printf("  DVDs go to %s\n  Blu-rays go to %s\n", next.DestDir("DVD"), next.DestDir("Blu-ray"))
	}
	noteIfRunning(dbPath)
	return nil
}

// checkSettingValue checks paths, which config.Validate can't, and expands ~.
func checkSettingValue(key, value string) (string, error) {
	switch key {
	case config.KeyOutputDir:
		value = expandHome(value)
		if !filepath.IsAbs(value) {
			return "", errors.New("output_dir must be a full path (starting with / or ~)")
		}
		value = filepath.Clean(value)
		fi, err := os.Stat(value)
		if err != nil {
			return "", fmt.Errorf("output_dir: %w", err)
		}
		if !fi.IsDir() {
			return "", fmt.Errorf("output_dir: %s is not a folder", value)
		}
	case config.KeyMakeMKVCon:
		value = expandHome(value)
		p, err := exec.LookPath(value)
		if err != nil {
			return "", fmt.Errorf("makemkvcon_path: %w", err)
		}
		value = p
	}
	return value, nil
}

func expandHome(p string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

func setDrive(ctx context.Context, dbPath, device string, on bool) error {
	s, _, err := openStore(ctx, dbPath)
	if err != nil {
		return err
	}
	defer s.Close()
	if !strings.HasPrefix(device, "/") {
		device = "/dev/" + device // allow "sr0"
	}
	if err := s.SetDriveEnabled(ctx, device, on); err != nil {
		if errors.Is(err, store.ErrUnknownDrive) {
			return fmt.Errorf("%s is not a configured drive; run `blurip setup` to add drives", device)
		}
		return err
	}
	state := "off"
	if on {
		state = "on"
	}
	fmt.Printf("%s is now %s\n", device, state)
	noteIfRunning(dbPath)
	return nil
}

// noteIfRunning tells the user when a running blurip won't see a change.
func noteIfRunning(dbPath string) {
	lock, err := store.AcquireLock(dbPath)
	if errors.Is(err, store.ErrLocked) {
		fmt.Println("blurip is running; restart it to use the new setting.")
		return
	}
	if err == nil {
		lock.Release()
	}
}

func showConfig(ctx context.Context, dbPath string) error {
	s, _, err := openStore(ctx, dbPath)
	if err != nil {
		return err
	}
	defer s.Close()

	settings, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	version, err := s.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("Database: %s (schema v%d)\n\n", s.Path(), version)

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SETTING\tVALUE")
	defaults := config.Default().ToMap()
	for _, k := range config.Keys() {
		v, ok := settings[k]
		if !ok {
			v = defaults[k] + "  (default)"
		}
		fmt.Fprintf(tw, "%s\t%s\n", k, dash(v))
	}
	tw.Flush()

	drives, err := s.Drives(ctx)
	if err != nil {
		return err
	}
	fmt.Println()
	if len(drives) == 0 {
		fmt.Println("No drives configured.")
		return nil
	}
	tw = tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "DRIVE\tENABLED\tNAME")
	for _, d := range drives {
		fmt.Fprintf(tw, "%s\t%t\t%s\n", d.Device, d.Enabled, d.Name)
	}
	return tw.Flush()
}
