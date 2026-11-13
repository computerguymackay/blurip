package makemkv

import (
	"os"
	"reflect"
	"testing"
	"time"
)

func TestSplitFields(t *testing.T) {
	cases := map[string][]string{
		`0,9,0,"1:47:12"`:      {"0", "9", "0", "1:47:12"},
		`1,"a, b","c"`:         {"1", "a, b", "c"},
		`2,"say "hi" there",x`: {"2", `say "hi" there`, "x"},
		`3,"",""`:              {"3", "", ""},
		`4,"unterminated`:      {"4", "unterminated"},
		`5,`:                   {"5", ""},
		`7`:                    {"7"},
	}
	for in, want := range cases {
		if got := splitFields(in); !reflect.DeepEqual(got, want) {
			t.Errorf("splitFields(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseRecordRejectsNonRecords(t *testing.T) {
	for _, line := range []string{"", "hello world", "MakeMKV v2.0.0 started", `"x":1`} {
		if _, ok := ParseRecord(line); ok {
			t.Errorf("ParseRecord(%q) accepted a non-record", line)
		}
	}
}

func parseFile(t *testing.T, name string) *Info {
	t.Helper()
	f, err := os.Open("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, err := ParseInfo(f)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func TestParseDrives(t *testing.T) {
	info := parseFile(t, "drives.txt")

	if info.Version != "MakeMKV v2.0.0 linux(x64-release)" {
		t.Errorf("Version = %q", info.Version)
	}
	var real []Drive
	for _, d := range info.Drives {
		if d.Exists() {
			real = append(real, d)
		}
	}
	if len(real) != 2 {
		t.Fatalf("got %d real drives, want 2", len(real))
	}
	dvd, bd := real[0], real[1]
	if dvd.Device != "/dev/sr0" || dvd.HasDisc() || dvd.State != DriveEmptyClosed {
		t.Errorf("sr0 = %+v", dvd)
	}
	if bd.Device != "/dev/sr1" || !bd.HasDisc() || bd.DiscLabel != "THE_PREDATOR" || bd.DiscKind() != "Blu-ray" {
		t.Errorf("sr1 = %+v", bd)
	}
	if info.TitleCount != 0 || len(info.Titles) != 0 {
		t.Errorf("TitleCount = %d, titles = %d", info.TitleCount, len(info.Titles))
	}
}

func TestParseDiscInfo(t *testing.T) {
	info := parseFile(t, "bluray-synthetic.txt")

	if info.Disc.Type() != "Blu-ray disc" || info.Disc.VolumeName() != "EXAMPLE_MOVIE" {
		t.Errorf("disc = %+v", info.Disc)
	}
	if info.TitleCount != 3 || len(info.Titles) != 3 {
		t.Fatalf("TitleCount = %d, titles = %d", info.TitleCount, len(info.Titles))
	}
	t0 := info.Titles[0]
	if t0.Duration() != time.Hour+47*time.Minute+12*time.Second {
		t.Errorf("duration = %v", t0.Duration())
	}
	if t0.SizeBytes() != 34467127296 || t0.Chapters() != 16 || t0.SourceFile() != "00800.mpls" {
		t.Errorf("title 0 = %+v", t0.Attrs)
	}
	audio := t0.StreamsOfType("Audio")
	if len(audio) != 2 || audio[0].LangCode() != "eng" || audio[0].Channels() != 8 || audio[1].Codec() != "DD" {
		t.Errorf("audio = %+v", audio)
	}
	if subs := t0.StreamsOfType("Subtitles"); len(subs) != 1 || subs[0].ID != 3 {
		t.Errorf("subs = %+v", subs)
	}
}

func TestMainFeature(t *testing.T) {
	info := parseFile(t, "bluray-synthetic.txt")
	// Titles 0 and 2 are the same size; 2 is longer, so it wins.
	mf, ok := MainFeature(info.Titles)
	if !ok || mf.ID != 2 {
		t.Errorf("MainFeature = %d, want 2", mf.ID)
	}
	if _, ok := MainFeature(nil); ok {
		t.Error("MainFeature(nil) returned ok")
	}
}

func TestParseDuration(t *testing.T) {
	cases := map[string]time.Duration{
		"1:47:12": time.Hour + 47*time.Minute + 12*time.Second,
		"0:04:30": 4*time.Minute + 30*time.Second,
		"45":      45 * time.Second,
		"":        0,
		"x:00":    0,
	}
	for in, want := range cases {
		if got := parseDuration(in); got != want {
			t.Errorf("parseDuration(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestDiscKind(t *testing.T) {
	for typ, want := range map[string]string{
		"Blu-ray disc": "Blu-ray", "DVD disc": "DVD", "HD-DVD disc": "", "": "",
	} {
		if got := (Disc{Attrs: Attrs{AttrType: typ}}).Kind(); got != want {
			t.Errorf("Kind(%q) = %q, want %q", typ, got, want)
		}
	}
}
