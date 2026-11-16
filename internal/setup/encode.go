package setup

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/computerguymackay/blurip/internal/config"
	"github.com/computerguymackay/blurip/internal/handbrake"
	"github.com/computerguymackay/blurip/internal/prompt"
)

// EncodeEnv holds the encode wizard's external dependencies.
type EncodeEnv struct {
	FindHandBrake func(saved string) (string, error)
	Version       func(ctx context.Context, bin string) (string, error)
	ListPresets   func(ctx context.Context, bin string) ([]handbrake.Category, error)
	HomeDir       string
}

// DefaultEncodeEnv uses the real HandBrakeCLI.
func DefaultEncodeEnv() EncodeEnv {
	return EncodeEnv{
		FindHandBrake: handbrake.Find,
		Version:       handbrake.Version,
		ListPresets:   handbrake.ListPresets,
		HomeDir:       DefaultEnv().HomeDir,
	}
}

// DefaultPreset is suggested for both DVDs and Blu-rays when none is set.
// It never makes a DVD bigger than it is: HandBrake doesn't upscale.
const DefaultPreset = "General/Fast 1080p30"

// RunEncode sets up HandBrake encoding: HandBrakeCLI, a preset for DVDs
// and one for Blu-rays, the folders for films and TV shows, and whether
// rips are deleted once encoded. Nothing is saved.
func RunEncode(ctx context.Context, p *prompt.Prompter, env EncodeEnv, cur config.Config) (config.Config, error) {
	cfg := cur
	p.Println("HandBrake encoding")
	p.Println("==================")
	p.Println("With `blurip -Q`, every rip is queued, and `blurip encode` encodes the queue")
	p.Println("with HandBrake into the folders you choose here.")
	p.Println("Press Enter to accept the value in [brackets].")
	p.Println()

	bin, version, err := askHandBrake(ctx, p, env, cfg.HandBrakeCLI)
	if err != nil {
		return cur, err
	}
	cfg.HandBrakeCLI = bin
	p.Printf("Using %s (%s)\n\n", bin, version)

	p.Println("Reading HandBrake's presets (including the ones you made in the HandBrake app)...")
	cats, err := env.ListPresets(ctx, bin)
	if err != nil {
		return cur, err
	}
	p.Println()
	if cfg.PresetDVD, err = ChoosePreset(p, cats, "Preset for DVDs", orDefault(cfg.PresetDVD, DefaultPreset)); err != nil {
		return cur, err
	}
	p.Println()
	if cfg.PresetBluRay, err = ChoosePreset(p, cats, "Preset for Blu-rays", orDefault(cfg.PresetBluRay, DefaultPreset)); err != nil {
		return cur, err
	}
	p.Println()
	p.Println("Films marked as foreign keep every audio and subtitle track and are saved as MKV,")
	p.Println("because MP4 can't hold Blu-ray subtitles except burned into the picture.")
	p.Println()

	home := env.HomeDir
	if cfg.MoviesDir, err = askOutputDir(p, Env{HomeDir: home}, "Folder for encoded films",
		orDefault(cfg.MoviesDir, filepath.Join(home, "Videos", "Movies"))); err != nil {
		return cur, err
	}
	if cfg.ShowsDir, err = askOutputDir(p, Env{HomeDir: home}, "Folder for encoded TV box sets",
		orDefault(cfg.ShowsDir, filepath.Join(home, "Videos", "Shows"))); err != nil {
		return cur, err
	}
	p.Println()

	def := cfg.DeleteAfterEncode || !cur.EncodeReady()
	if cfg.DeleteAfterEncode, err = p.YesNo("Delete each rip once its encode has been made and checked?", def); err != nil {
		return cur, err
	}
	return cfg, cfg.Validate()
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func askHandBrake(ctx context.Context, p *prompt.Prompter, env EncodeEnv, saved string) (string, string, error) {
	if found, err := env.FindHandBrake(saved); err == nil {
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
		p.Println("Couldn't find HandBrakeCLI. Install HandBrake (the Flatpak fr.handbrake.ghb")
		p.Println("includes it), or give the path to HandBrakeCLI.")
	}
	for {
		path, err := p.String("Path to HandBrakeCLI (or a command line that runs it)", "")
		if err != nil {
			return "", "", err
		}
		if path == "" {
			continue
		}
		path = expandHome(path, env.HomeDir)
		v, err := env.Version(ctx, path)
		if err == nil {
			if path, err = handbrake.Resolve(path); err == nil {
				return path, v, nil
			}
		}
		p.Printf("  %v\n", err)
	}
}

// ChoosePreset asks for a preset with two numbered menus: the category,
// then the preset in it. Enter keeps cur ("Category/Name").
func ChoosePreset(p *prompt.Prompter, cats []handbrake.Category, question, cur string) (string, error) {
	if len(cats) == 0 {
		return cur, fmt.Errorf("HandBrake has no presets")
	}
	curPreset, found := handbrake.FindPreset(cats, cur)
	header := question
	if found {
		header = fmt.Sprintf("%s (now: %s / %s)", question, curPreset.Category, curPreset.Name)
	} else if cur != "" {
		header = fmt.Sprintf("%s (now: %s, which HandBrake doesn't have)", question, cur)
	}
	opts := make([]prompt.Option, len(cats))
	defCat := ""
	for i, c := range cats {
		label := fmt.Sprintf("%s (%d)", c.Name, len(c.Presets))
		if c.Custom {
			label += "  your own"
		}
		opts[i] = prompt.Option{Value: c.Name, Label: label}
		if found && strings.EqualFold(c.Name, curPreset.Category) {
			defCat = c.Name
		}
	}
	catName, err := p.Choice(header+"\nCategory:", opts, defCat)
	if err != nil {
		return cur, err
	}
	var cat handbrake.Category
	for _, c := range cats {
		if c.Name == catName {
			cat = c
		}
	}
	popts := make([]prompt.Option, len(cat.Presets))
	defPreset := ""
	for i, pr := range cat.Presets {
		label := pr.Name
		if pr.Format != "mp4" {
			label += "  (" + strings.ToUpper(pr.Format) + ")"
		}
		popts[i] = prompt.Option{Value: pr.Ref(), Label: label}
		if found && pr.Ref() == curPreset.Ref() {
			defPreset = pr.Ref()
		}
	}
	return p.Choice(cat.Name+":", popts, defPreset)
}
