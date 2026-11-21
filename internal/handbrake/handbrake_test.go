package handbrake

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParsePresetList(t *testing.T) {
	f, err := os.Open("testdata/preset-list.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cats := ParsePresetList(f)
	var names []string
	for _, c := range cats {
		names = append(names, c.Name)
	}
	want := []string{"My Presets", "General", "Web", "Devices", "Matroska", "Hardware", "Professional"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("categories = %q, want %q", names, want)
	}
	my := cats[0]
	if !my.Custom || len(my.Presets) != 1 || my.Presets[0].Name != "720pFast" || my.Presets[0].Ref() != "My Presets/720pFast" {
		t.Errorf("My Presets = %+v", my)
	}
	p, ok := FindPreset(cats, "General/Fast 1080p30")
	if !ok || p.Format != "mp4" || !strings.HasPrefix(p.Description, "H.264 video (up to 1080p30) and AAC stereo audio, in an MP4 container") {
		t.Errorf("Fast 1080p30 = %+v, %v", p, ok)
	}
	if p, ok := FindPreset(cats, "Matroska/H.265 MKV 1080p30"); !ok || p.Format != "mkv" {
		t.Errorf("H.265 MKV 1080p30 = %+v, %v", p, ok)
	}
	if _, ok := FindPreset(cats, "720pFast"); !ok {
		t.Error("a plain name should be found in any category")
	}
	if _, ok := FindPreset(cats, "CLI Defaults/CLI Default"); ok {
		t.Error("CLI Defaults should be left out")
	}
	if cats[1].Custom {
		t.Error("General is built in")
	}
}

func TestParseProgress(t *testing.T) {
	for line, want := range map[string]Progress{
		"Encoding: task 1 of 1, 45.32 % (120.51 fps, avg 110.22 fps, ETA 00h12m30s)": {Task: "Encoding", Fraction: 0.4532},
		"Encoding: task 2 of 2, 50.00 %":                                             {Task: "Encoding", Fraction: 0.75},
		"Scanning title 1 of 1, preview 5, 50.00 %":                                  {},
	} {
		got, ok := ParseProgress(line)
		if want.Task == "" {
			if ok {
				t.Errorf("%q: got %+v, want no progress", line, got)
			}
			continue
		}
		if !ok || got.Task != want.Task || got.Fraction < want.Fraction-1e-9 || got.Fraction > want.Fraction+1e-9 {
			t.Errorf("%q = %+v, %v; want %+v", line, got, ok, want)
		}
	}
}

func TestArgs(t *testing.T) {
	r := Request{Input: "in.mkv", Output: "out.mp4", Preset: "My Presets/720pFast", Format: "mp4"}
	want := "-i in.mkv -o out.mp4 --preset-import-gui --preset 720pFast --format av_mp4"
	if got := strings.Join(r.Args(), " "); got != want {
		t.Errorf("standard: %s\nwant      %s", got, want)
	}
	r.AllTracks, r.Format = true, "mkv"
	got := strings.Join(r.Args(), " ")
	for _, s := range []string{"--format av_mkv", "--audio-lang-list any --all-audio", "--subtitle-lang-list any --all-subtitles", "--subtitle-burned=none"} {
		if !strings.Contains(got, s) {
			t.Errorf("all tracks: %s lacks %s", got, s)
		}
	}
}

func TestArgsChapters(t *testing.T) {
	r := Request{Input: "in.mkv", Output: "out.mp4", Preset: "720pFast", Format: "mp4", Chapters: "6-10"}
	if got := strings.Join(r.Args(), " "); !strings.Contains(got, "--chapters 6-10") {
		t.Errorf("args: %s", got)
	}
}

func TestParseScan(t *testing.T) {
	out := []byte(`[12:00:01] scan: decoding previews for title 1
+ title 1:
  + stream: /rips/Spaced - S01E01-E07.mkv
  + duration: 02:50:28
  + size: 720x576, pixel aspect: 64/45, display aspect: 1.78, 25.000 fps
  + chapters:
    + 1: duration 00:04:52
    + 2: duration 00:05:01
    + 3: Chapter 3, duration 00:00:31
  + audio tracks:
    + 1, English (AC3) (2.0 ch) (iso639-2: eng)
`)
	r, err := parseScan(out, "x.mkv", nil)
	if err != nil || r.Duration != 2*time.Hour+50*time.Minute+28*time.Second || len(r.Chapters) != 3 || r.Chapters[1] != 5*time.Minute+time.Second {
		t.Fatalf("scan = %+v, %v", r, err)
	}
	if d, err := ChapterLength(r.Chapters, 2, 3); err != nil || d != 5*time.Minute+32*time.Second {
		t.Errorf("chapters 2-3 = %v, %v", d, err)
	}
	if _, err := ChapterLength(r.Chapters, 3, 4); err == nil {
		t.Error("chapter 4 of 3 accepted")
	}
}

func TestCheckLength(t *testing.T) {
	if err := CheckLength(time.Hour, time.Hour-3*time.Second); err != nil {
		t.Error(err)
	}
	if err := CheckLength(2*time.Hour, 2*time.Hour-time.Minute); err != nil {
		t.Errorf("1 minute in 2 hours is within 1%%: %v", err)
	}
	if err := CheckLength(time.Hour, 40*time.Minute); err == nil {
		t.Error("a short encode should fail")
	}
}

// fakeHandBrake writes a HandBrakeCLI that prints progress and copies its
// input to its output, or fails if the input is called fail.mkv.
func fakeHandBrake(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "HandBrakeCLI")
	os.WriteFile(bin, []byte(`#!/bin/sh
if [ "$1" = --version ]; then echo "HandBrake 1.8.2"; exit 0; fi
if [ "$1" = --scan ]; then echo "  + duration: 01:30:00" >&2; exit 0; fi
case "$2" in *fail.mkv) echo "ERROR: no title found" >&2; exit 3;; esac
printf 'Encoding: task 1 of 1, 10.00 %%\rEncoding: task 1 of 1, 99.50 %% (100 fps)\r\n'
cp "$2" "$4"
`), 0o755)
	return bin
}

func TestEncode(t *testing.T) {
	bin := fakeHandBrake(t)
	dir := t.TempDir()
	in := filepath.Join(dir, "in.mkv")
	os.WriteFile(in, []byte("video"), 0o644)
	var got []Progress
	err := Encode(context.Background(), Request{Bin: bin, Input: in, Output: filepath.Join(dir, "out.mp4"), Preset: "General/Fast 1080p30", Format: "mp4",
		OnProgress: func(p Progress) { got = append(got, p) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].Fraction < 0.99 {
		t.Errorf("progress = %+v", got)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "out.mp4")); string(b) != "video" {
		t.Errorf("output = %q", b)
	}
	fail := filepath.Join(dir, "fail.mkv")
	os.WriteFile(fail, nil, 0o644)
	err = Encode(context.Background(), Request{Bin: bin, Input: fail, Output: filepath.Join(dir, "x.mp4"), Preset: "x", Format: "mp4"})
	if err == nil || !strings.Contains(err.Error(), "ERROR: no title found") {
		t.Errorf("failing encode: %v", err)
	}
	if v, err := Version(context.Background(), bin); err != nil || v != "HandBrake 1.8.2" {
		t.Errorf("Version = %q, %v", v, err)
	}
	if d, err := Duration(context.Background(), bin, in); err != nil || d != 90*time.Minute {
		t.Errorf("Duration = %v, %v", d, err)
	}
}
