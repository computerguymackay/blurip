package setup

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/computerguymackay/blurip/internal/config"
	"github.com/computerguymackay/blurip/internal/handbrake"
	"github.com/computerguymackay/blurip/internal/prompt"
	"github.com/computerguymackay/blurip/internal/store"
)

// Menu lets the user change single settings: it lists every setting with
// its value, and the one picked by number is asked about on its own, as
// often as they like. Enter finishes; "a" goes through everything as the
// first setup did; "q" quits without saving (changed is false).
//
// drives are the drives in use now, kept unless the drives are changed.
// With encodeOnly, only the encoding settings are listed (blurip encode
// setup).
func Menu(ctx context.Context, p *prompt.Prompter, env Env, enc EncodeEnv, cur config.Config, drives []store.Drive, encodeOnly bool) (res Result, changed bool, err error) {
	cfg := cur
	res.Drives = drives
	presets := presetLoader(ctx, p, enc, &cfg)
	for {
		items := menuItems(ctx, p, env, enc, &cfg, &res.Drives, presets, encodeOnly)
		p.Println()
		if encodeOnly {
			p.Println("Encoding settings (blurip -Q, blurip encode)")
		} else {
			p.Println("blurip settings")
		}
		for i, it := range items {
			if it.section != "" {
				p.Printf("  %s\n", it.section)
			}
			p.Printf("  %2d) %-30s %s\n", i+1, it.name, it.value)
		}
		p.Println()
		finish := "Enter to finish"
		if changed {
			finish = "Enter to save and finish, q to quit without saving"
		}
		ans, err := p.String(fmt.Sprintf("Number to change, a to go through them all, %s", finish), "")
		if err != nil {
			return res, false, err
		}
		switch ans = strings.ToLower(strings.TrimSpace(ans)); ans {
		case "":
			res.Config = cfg
			return res, changed, cfg.Validate()
		case "q":
			return Result{Config: cur, Drives: drives}, false, nil
		case "a":
			p.Println()
			if encodeOnly {
				next, err := RunEncode(ctx, p, enc, cfg)
				return Result{Config: next, Drives: drives}, err == nil, err
			}
			r, err := Run(ctx, p, env, cfg)
			if err != nil {
				return res, false, err
			}
			if r.Config.EncodeReady() {
				p.Println()
				if r.Config, err = RunEncode(ctx, p, enc, r.Config); err != nil {
					return res, false, err
				}
			}
			return r, true, nil
		}
		n, err := strconv.Atoi(ans)
		if err != nil || n < 1 || n > len(items) {
			p.Printf("  Please enter a number from 1 to %d, a, or press Enter.\n", len(items))
			continue
		}
		p.Println()
		before := cfg
		if err := items[n-1].change(); err != nil {
			return res, false, err
		}
		changed = true
		if before == cfg && items[n-1].name != "Drives" {
			p.Println("  (unchanged)")
		}
	}
}

type menuItem struct {
	section string // printed above the item, starting a group
	name    string
	value   string
	change  func() error
}

func menuItems(ctx context.Context, p *prompt.Prompter, env Env, enc EncodeEnv, cfg *config.Config, drives *[]store.Drive,
	presets func() ([]handbrake.Category, error), encodeOnly bool) []menuItem {
	var items []menuItem
	if !encodeOnly {
		output := cfg.OutputDir
		if cfg.SplitByType {
			output += fmt.Sprintf(" (%s and %s folders)", config.DVDFolder, config.BluRayFolder)
		}
		items = append(items,
			menuItem{section: "Ripping", name: "MakeMKV", value: cfg.MakeMKVCon,
				change: func() error { return stepMakeMKV(ctx, p, env, cfg) }},
			menuItem{name: "Rips go to", value: output,
				change: func() error { return stepOutput(p, env, cfg) }},
			menuItem{name: "When a disc is inserted", value: ripModeLabel(cfg.RipMode),
				change: func() error { return stepRipMode(p, cfg) }},
			menuItem{name: "Eject after ripping", value: yesNo(cfg.Eject),
				change: func() error { return stepEject(p, cfg) }},
			menuItem{name: "Fill the rips disk up to", value: fmt.Sprintf("%d%%", cfg.MaxFillPercent),
				change: func() error { return stepMaxFill(p, cfg) }},
			menuItem{name: "Give up reading a disc after", value: readTimeoutLabel(cfg.ReadTimeout),
				change: func() error { return stepReadTimeout(p, cfg) }},
			menuItem{name: "Drives", value: drivesLabel(*drives, *cfg),
				change: func() error {
					d, err := askDrives(ctx, p, env, cfg)
					if err == nil {
						*drives = d
					}
					return err
				}},
		)
	}
	section := "Encoding with HandBrake (blurip -Q, blurip encode)"
	if !cfg.EncodeReady() {
		return append(items, menuItem{section: section, name: "Encoding", value: "not set up yet",
			change: func() error {
				next, err := RunEncode(ctx, p, enc, *cfg)
				if err == nil {
					*cfg = next
				}
				return err
			}})
	}
	return append(items,
		menuItem{section: section, name: "HandBrake", value: cfg.HandBrakeCLI,
			change: func() error { return stepHandBrake(ctx, p, enc, cfg) }},
		menuItem{name: "Preset for DVDs", value: presetLabel(cfg.PresetDVD),
			change: func() error { return stepPreset(p, presets, "DVD", cfg) }},
		menuItem{name: "Preset for Blu-rays", value: presetLabel(cfg.PresetBluRay),
			change: func() error { return stepPreset(p, presets, "Blu-ray", cfg) }},
		menuItem{name: "Encoded films go to", value: cfg.MoviesDir,
			change: func() error { return stepFolder(p, enc, "movies", cfg) }},
		menuItem{name: "Encoded TV box sets go to", value: cfg.ShowsDir,
			change: func() error { return stepFolder(p, enc, "shows", cfg) }},
		menuItem{name: "Delete rips once encoded", value: yesNo(cfg.DeleteAfterEncode),
			change: func() error { return stepDelete(p, false, cfg) }},
		menuItem{name: "Fill the films/TV disk up to", value: fmt.Sprintf("%d%%", cfg.EncodeMaxFillPercent),
			change: func() error { return stepEncodeMaxFill(p, cfg) }},
	)
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func ripModeLabel(m config.RipMode) string {
	if m == config.RipAsk {
		return "show what's on it and ask"
	}
	return "rip with the saved defaults"
}

func readTimeoutLabel(min int) string {
	if min == 0 {
		return "never"
	}
	return fmt.Sprintf("%d minutes", min)
}

func presetLabel(ref string) string {
	cat, name := handbrake.SplitRef(ref)
	if cat == "" {
		return name
	}
	return cat + " / " + name
}

func drivesLabel(drives []store.Drive, cfg config.Config) string {
	if len(drives) == 0 {
		return "none"
	}
	names := make([]string, len(drives))
	for i, d := range drives {
		names[i] = d.Label()
	}
	s := strings.Join(names, ", ")
	switch {
	case len(drives) == 1:
	case cfg.DriveMode == config.DriveParallel && cfg.MaxConcurrent > 0:
		s += fmt.Sprintf("; at the same time, up to %d", cfg.MaxConcurrent)
	case cfg.DriveMode == config.DriveParallel:
		s += "; all at the same time"
	default:
		s += "; one rip at a time"
	}
	return s
}
