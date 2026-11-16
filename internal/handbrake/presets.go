package handbrake

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Preset is one HandBrake preset.
type Preset struct {
	Category    string // "General", or a folder of the user's own ("My Presets")
	Name        string // "Fast 1080p30"
	Description string
	Custom      bool   // made by the user in the HandBrake app
	Format      string // "mp4", "mkv" or "webm"
}

// Ref is how a preset is saved: "General/Fast 1080p30".
func (p Preset) Ref() string { return p.Category + "/" + p.Name }

// SplitRef splits a saved preset into category and name. A plain name
// ("Fast 1080p30") has no category.
func SplitRef(ref string) (category, name string) {
	if c, n, ok := strings.Cut(ref, "/"); ok {
		return c, n
	}
	return "", ref
}

// Category is a group of presets as HandBrake lists them.
type Category struct {
	Name    string
	Custom  bool
	Presets []Preset
}

// builtIn are HandBrake's own categories; any other holds the user's
// presets. "CLI Defaults" only holds HandBrakeCLI's fallback and is left
// out.
var builtIn = map[string]bool{
	"General": true, "Web": true, "Devices": true, "Matroska": true,
	"Hardware": true, "Professional": true, "Production": true,
}

const cliDefaults = "CLI Defaults"

// ParsePresetList parses `HandBrakeCLI --preset-list` output:
//
//	General/
//	    Fast 1080p30
//	        H.264 video (up to 1080p30) and AAC stereo audio, in an MP4
//	        container.
//
// Log lines before the list ("[11:51:41] ...") are ignored.
func ParsePresetList(r io.Reader) []Category {
	var cats []Category
	var cur *Category
	var p *Preset
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t\r")
		indent := len(line) - len(strings.TrimLeft(line, " "))
		text := strings.TrimSpace(line)
		switch {
		case text == "":
		case indent == 0 && strings.HasSuffix(text, "/"):
			name := strings.TrimSuffix(text, "/")
			cats = append(cats, Category{Name: name, Custom: !builtIn[name]})
			cur, p = &cats[len(cats)-1], nil
		case indent == 0:
			cur, p = nil, nil // a log line or the end of the list
		case cur != nil && indent <= 4:
			cur.Presets = append(cur.Presets, Preset{Category: cur.Name, Name: text, Custom: cur.Custom})
			p = &cur.Presets[len(cur.Presets)-1]
		case p != nil:
			if p.Description != "" {
				p.Description += " "
			}
			p.Description += text
		}
	}
	out := cats[:0]
	for _, c := range cats {
		if c.Name == cliDefaults || len(c.Presets) == 0 {
			continue
		}
		for i := range c.Presets {
			c.Presets[i].Format = formatFromDescription(c.Presets[i].Description)
		}
		out = append(out, c)
	}
	// The user's own categories first: they are the ones most often wanted.
	var custom, rest []Category
	for _, c := range out {
		if c.Custom {
			custom = append(custom, c)
		} else {
			rest = append(rest, c)
		}
	}
	return append(custom, rest...)
}

var containerRE = regexp.MustCompile(`(?i)\b(MP4|MKV|WebM)\s+container`)

func formatFromDescription(d string) string {
	if m := containerRE.FindStringSubmatch(d); m != nil {
		return strings.ToLower(m[1])
	}
	return "mp4"
}

// ListPresets runs HandBrakeCLI's preset list, including the presets the
// user made in the HandBrake app. The format of the user's own presets is
// taken from the app's presets file when it can be read, since their
// descriptions are often copied from the preset they started from.
func ListPresets(ctx context.Context, bin string) ([]Category, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	out, err := commandContext(ctx, bin, "--preset-import-gui", "--preset-list").CombinedOutput()
	cats := ParsePresetList(strings.NewReader(string(out)))
	if len(cats) == 0 {
		if err != nil {
			return nil, fmt.Errorf("listing HandBrake presets: %w: %s", err, lastLines(out, 3))
		}
		return nil, fmt.Errorf("HandBrakeCLI listed no presets: %s", lastLines(out, 3))
	}
	if formats := guiFormats(); len(formats) > 0 {
		for i := range cats {
			for j := range cats[i].Presets {
				p := &cats[i].Presets[j]
				if f, ok := formats[p.Ref()]; ok && p.Custom {
					p.Format = f
				}
			}
		}
	}
	return cats, nil
}

// FindPreset returns the preset saved as ref ("General/Fast 1080p30", or just a
// name).
func FindPreset(cats []Category, ref string) (Preset, bool) {
	cat, name := SplitRef(ref)
	for _, c := range cats {
		if cat != "" && !strings.EqualFold(c.Name, cat) {
			continue
		}
		for _, p := range c.Presets {
			if strings.EqualFold(p.Name, name) {
				return p, true
			}
		}
	}
	return Preset{}, false
}

// guiPresetFiles are where the HandBrake app keeps the user's presets.
func guiPresetFiles() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, ".var/app", FlatpakApp, "config/ghb/presets.json"),
		filepath.Join(home, ".config/ghb/presets.json"),
	}
}

type guiPreset struct {
	PresetName    string
	Folder        bool
	FileFormat    string
	ChildrenArray []guiPreset
}

// guiFormats maps "Category/Name" of the user's own presets to their
// format ("mp4", "mkv", "webm").
func guiFormats() map[string]string {
	out := map[string]string{}
	for _, f := range guiPresetFiles() {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var doc struct{ PresetList []guiPreset }
		if json.Unmarshal(b, &doc) != nil {
			continue
		}
		for _, folder := range doc.PresetList {
			if !folder.Folder {
				continue
			}
			for _, p := range folder.ChildrenArray {
				if f := strings.TrimPrefix(p.FileFormat, "av_"); f != "" {
					out[folder.PresetName+"/"+p.PresetName] = f
				}
			}
		}
	}
	return out
}
