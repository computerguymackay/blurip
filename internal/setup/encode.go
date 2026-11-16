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

	if err := stepHandBrake(ctx, p, env, &cfg); err != nil {
		return cur, err
	}
	p.Println()
	presets := presetLoader(ctx, p, env, &cfg)
	if err := stepPreset(p, presets, "DVD", &cfg); err != nil {
		return cur, err
	}
	p.Println()
	if err := stepPreset(p, presets, "Blu-ray", &cfg); err != nil {
		return cur, err
	}
	p.Println()
	p.Println("Films marked as foreign keep every audio and subtitle track. From a Blu-ray they")
	p.Println("are saved as MKV, because MP4 can't hold Blu-ray subtitles except burned into the")
	p.Println("picture; DVDs keep the preset's format, since MP4 can hold DVD subtitles.")
	p.Println()
	if err := stepFolder(p, env, "movies", &cfg); err != nil {
		return cur, err
	}
	if err := stepFolder(p, env, "shows", &cfg); err != nil {
		return cur, err
	}
	p.Println()
	if err := stepDelete(p, !cur.EncodeReady(), &cfg); err != nil {
		return cur, err
	}
	return cfg, cfg.Validate()
}

func stepHandBrake(ctx context.Context, p *prompt.Prompter, env EncodeEnv, cfg *config.Config) error {
	bin, version, err := askHandBrake(ctx, p, env, cfg.HandBrakeCLI)
	if err != nil {
		return err
	}
	cfg.HandBrakeCLI = bin
	p.Printf("Using %s (%s)\n", bin, version)
	return nil
}

// presetLoader returns a function that lists HandBrake's presets the
// first time it is called.
func presetLoader(ctx context.Context, p *prompt.Prompter, env EncodeEnv, cfg *config.Config) func() ([]handbrake.Category, error) {
	var cats []handbrake.Category
	var bin string
	return func() ([]handbrake.Category, error) {
		if cats != nil && bin == cfg.HandBrakeCLI {
			return cats, nil
		}
		p.Println("Reading HandBrake's presets (including the ones you made in the HandBrake app)...")
		c, err := env.ListPresets(ctx, cfg.HandBrakeCLI)
		if err != nil {
			return nil, err
		}
		cats, bin = c, cfg.HandBrakeCLI
		p.Println()
		return cats, nil
	}
}

// stepPreset asks for the preset for "DVD" or "Blu-ray" discs.
func stepPreset(p *prompt.Prompter, presets func() ([]handbrake.Category, error), kind string, cfg *config.Config) error {
	cats, err := presets()
	if err != nil {
		return err
	}
	dst := &cfg.PresetDVD
	if kind == "Blu-ray" {
		dst = &cfg.PresetBluRay
	}
	*dst, err = ChoosePreset(p, cats, "Preset for "+kind+"s", orDefault(*dst, DefaultPreset))
	return err
}

// stepFolder asks for the "movies" or "shows" folder.
func stepFolder(p *prompt.Prompter, env EncodeEnv, which string, cfg *config.Config) (err error) {
	home := env.HomeDir
	if which == "shows" {
		cfg.ShowsDir, err = askOutputDir(p, Env{HomeDir: home}, "Folder for encoded TV box sets",
			orDefault(cfg.ShowsDir, filepath.Join(home, "Videos", "Shows")))
		return err
	}
	cfg.MoviesDir, err = askOutputDir(p, Env{HomeDir: home}, "Folder for encoded films",
		orDefault(cfg.MoviesDir, filepath.Join(home, "Videos", "Movies")))
	return err
}

// stepDelete asks whether rips are deleted once encoded; firstTime
// suggests yes.
func stepDelete(p *prompt.Prompter, firstTime bool, cfg *config.Config) (err error) {
	cfg.DeleteAfterEncode, err = p.YesNo("Delete each rip once its encode has been made and checked?", cfg.DeleteAfterEncode || firstTime)
	return err
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
