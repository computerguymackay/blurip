package setup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/computerguymackay/blurip/internal/config"
	"github.com/computerguymackay/blurip/internal/makemkv"
	"github.com/computerguymackay/blurip/internal/prompt"
	"github.com/computerguymackay/blurip/internal/store"
)

// Env holds the wizard's external dependencies so tests can replace them.
type Env struct {
	FindMakeMKV func(saved string) (string, error)
	Version     func(ctx context.Context, path string) (string, error)
	ListDrives  func(ctx context.Context, bin string) ([]makemkv.Drive, error)
	HomeDir     string
}

// DefaultEnv uses the real MakeMKV and file system.
func DefaultEnv() Env {
	home, _ := os.UserHomeDir()
	return Env{
		FindMakeMKV: makemkv.Find,
		Version:     makemkv.Version,
		ListDrives:  makemkv.ListDrives,
		HomeDir:     home,
	}
}

// Result is what the wizard collected.
type Result struct {
	Config config.Config
	Drives []store.Drive
}

// Run walks the user through setup, starting from cur (the saved config or
// the defaults). Nothing is saved; the caller saves the result.
func Run(ctx context.Context, p *prompt.Prompter, env Env, cur config.Config) (Result, error) {
	cfg := cur
	p.Println("blurip setup")
	p.Println("============")
	p.Println("Press Enter to accept the value in [brackets].")
	p.Println()

	// MakeMKV.
	bin, version, err := askMakeMKV(ctx, p, env, cfg.MakeMKVCon)
	if err != nil {
		return Result{}, err
	}
	cfg.MakeMKVCon = bin
	p.Printf("Using %s (%s)\n\n", bin, version)

	// Output folder.
	defOut := cfg.OutputDir
	if defOut == "" {
		defOut = filepath.Join(env.HomeDir, "Videos", "blurip")
	}
	if cfg.OutputDir, err = askOutputDir(p, env, defOut); err != nil {
		return Result{}, err
	}
	p.Println()

	// Rip mode.
	mode, err := p.Choice("When a disc is inserted:", []prompt.Option{
		{Value: string(config.RipDefaults), Label: "Rip it with my saved defaults"},
		{Value: string(config.RipAsk), Label: "Show me what's on the disc and ask every time"},
	}, string(cfg.RipMode))
	if err != nil {
		return Result{}, err
	}
	cfg.RipMode = config.RipMode(mode)
	p.Println()

	// Titles and tracks: fixed for now.
	cfg.TitleMode = config.TitleMainFeature
	p.Println("Titles: the main feature (the largest title) is ripped.")
	p.Println("Tracks: MakeMKV's own track selection is used. To keep every audio track,")
	p.Println("        set MakeMKV's \"Default selection rule\" to +sel:all.")
	p.Println()

	// Eject and space.
	if cfg.Eject, err = p.YesNo("Eject the disc after ripping?", cfg.Eject); err != nil {
		return Result{}, err
	}
	if cfg.MaxFillPercent, err = p.Int("Never fill the output disk past this percentage", cfg.MaxFillPercent, 1, 100); err != nil {
		return Result{}, err
	}
	p.Println()

	// Drives.
	drives, err := askDrives(ctx, p, env, &cfg)
	if err != nil {
		return Result{}, err
	}

	cfg.SetupComplete = true
	if err := cfg.Validate(); err != nil {
		return Result{}, err
	}
	return Result{Config: cfg, Drives: drives}, nil
}

func askMakeMKV(ctx context.Context, p *prompt.Prompter, env Env, saved string) (string, string, error) {
	if found, err := env.FindMakeMKV(saved); err == nil {
		p.Printf("Checking %s...\n", found)
		if v, err := env.Version(ctx, found); err == nil {
			ok, err := p.YesNo(fmt.Sprintf("Found %s (%s). Use it?", found, v), true)
			if err != nil {
				return "", "", err
			}
			if ok {
				return found, v, nil
			}
		} else {
			p.Printf("Found %s, but it didn't work: %v\n", found, err)
		}
	} else {
		p.Println("Couldn't find makemkvcon automatically.")
	}
	for {
		path, err := p.String("Path to makemkvcon", "")
		if err != nil {
			return "", "", err
		}
		if path == "" {
			continue
		}
		path = expandHome(path, env.HomeDir)
		p.Printf("Checking %s...\n", path)
		v, err := env.Version(ctx, path)
		if err == nil {
			return path, v, nil
		}
		p.Printf("  %v\n", err)
	}
}

func askOutputDir(p *prompt.Prompter, env Env, def string) (string, error) {
	for {
		dir, err := p.String("Output folder for finished rips", def)
		if err != nil {
			return "", err
		}
		dir = expandHome(dir, env.HomeDir)
		if !filepath.IsAbs(dir) {
			p.Println("  Please give a full path (starting with / or ~).")
			continue
		}
		dir = filepath.Clean(dir)
		fi, err := os.Stat(dir)
		switch {
		case errors.Is(err, os.ErrNotExist):
			create, err := p.YesNo(fmt.Sprintf("%s doesn't exist. Create it?", dir), true)
			if err != nil {
				return "", err
			}
			if !create {
				continue
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				p.Printf("  Couldn't create it: %v\n", err)
				continue
			}
		case err != nil:
			p.Printf("  %v\n", err)
			continue
		case !fi.IsDir():
			p.Println("  That's a file, not a folder.")
			continue
		}
		if err := checkWritable(dir); err != nil {
			p.Printf("  Can't write there: %v\n", err)
			continue
		}
		return dir, nil
	}
}

func askDrives(ctx context.Context, p *prompt.Prompter, env Env, cfg *config.Config) ([]store.Drive, error) {
	p.Println("Looking for optical drives...")
	found, err := env.ListDrives(ctx, cfg.MakeMKVCon)
	if err != nil {
		return nil, fmt.Errorf("listing drives: %w", err)
	}
	if len(found) == 0 {
		return nil, errors.New("MakeMKV found no optical drives; connect a drive and run `blurip setup` again")
	}

	byDevice := map[string]makemkv.Drive{}
	opts := make([]prompt.Option, len(found))
	for i, d := range found {
		byDevice[d.Device] = d
		opts[i] = prompt.Option{Value: d.Device, Label: fmt.Sprintf("%s  %s", d.Device, d.DriveName)}
	}

	devices := []string{found[0].Device}
	if len(found) == 1 {
		p.Printf("Using %s (%s)\n", found[0].Device, found[0].DriveName)
	} else if devices, err = p.MultiChoice("Which drives should blurip use?", opts); err != nil {
		return nil, err
	}

	drives := make([]store.Drive, len(devices))
	for i, dev := range devices {
		drives[i] = store.Drive{Device: dev, Name: byDevice[dev].DriveName, Enabled: true}
	}

	if len(drives) == 1 {
		cfg.DriveMode = config.DriveSingle
		cfg.MaxConcurrent = 0
		return drives, nil
	}

	def := string(cfg.DriveMode)
	if cfg.DriveMode == config.DriveSingle {
		def = string(config.DriveSequential)
	}
	p.Println()
	mode, err := p.Choice("With several drives:", []prompt.Option{
		{Value: string(config.DriveSequential), Label: "Rip one disc at a time (sequential)"},
		{Value: string(config.DriveParallel), Label: "Rip from all drives at the same time (parallel)"},
	}, def)
	if err != nil {
		return nil, err
	}
	cfg.DriveMode = config.DriveMode(mode)
	prevMax := cfg.MaxConcurrent
	if prevMax > len(drives) {
		prevMax = 0
	}
	cfg.MaxConcurrent = 0
	if cfg.DriveMode == config.DriveParallel {
		n, err := p.Int("Maximum rips at once (0 = one per drive)", prevMax, 0, len(drives))
		if err != nil {
			return nil, err
		}
		cfg.MaxConcurrent = n
	}
	return drives, nil
}

func expandHome(path, home string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	return path
}

func checkWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".blurip-write-test-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

// Save writes the wizard's result to the store.
func Save(ctx context.Context, s *store.Store, r Result) error {
	if err := r.Config.Validate(); err != nil {
		return err
	}
	return s.SaveSetup(ctx, r.Config.ToMap(), r.Drives)
}
