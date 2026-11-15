package rip

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/computerguymackay/blurip/internal/makemkv"
	"github.com/computerguymackay/blurip/internal/space"
)

func script(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "makemkvcon")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// $7 is the output directory.
const writesOne = `printf 'mkvdata' > "$7/Wild_Card_t01.mkv"` + "\n"

func title(size int64) makemkv.Title { return titleID(1, size) }

func titleID(id int, size int64) makemkv.Title {
	return makemkv.Title{ID: id, Attrs: makemkv.Attrs{makemkv.AttrDiskSizeBytes: itoa(size)}}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func req(t *testing.T, bin, out string) Request {
	return Request{MakeMKVCon: bin, Device: "/dev/sr1", Titles: []makemkv.Title{title(1000)}, OutputDir: out,
		Name: "Wild Card", JobID: "job1", MaxFillPercent: 100}
}

func TestRunSuccess(t *testing.T) {
	out := t.TempDir()
	res, err := Run(context.Background(), req(t, script(t, writesOne), out))
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(out, "Wild Card", "Wild Card.mkv")
	if res.Dir != filepath.Join(out, "Wild Card") || len(res.Files) != 1 || res.Bytes != 7 {
		t.Errorf("res = %+v", res)
	}
	if _, err := os.Stat(want); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(TempDir(out, "job1")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("temp folder left behind: %v", err)
	}
}

func TestRunExistingFolderGetsSuffix(t *testing.T) {
	out := t.TempDir()
	os.MkdirAll(filepath.Join(out, "Wild Card"), 0o755)
	os.WriteFile(filepath.Join(out, "Wild Card", "keep.mkv"), []byte("old"), 0o644)
	res, err := Run(context.Background(), req(t, script(t, writesOne), out))
	if err != nil {
		t.Fatal(err)
	}
	if res.Dir != filepath.Join(out, "Wild Card (2)") {
		t.Errorf("Dir = %s", res.Dir)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "Wild Card", "keep.mkv")); string(b) != "old" {
		t.Error("existing rip was modified")
	}
}

func TestRunMultipleFilesKeepNames(t *testing.T) {
	out := t.TempDir()
	bin := script(t, `printf a > "$7/a_t00.mkv"; printf b > "$7/b_t01.mkv"`+"\n")
	res, err := Run(context.Background(), req(t, bin, out))
	if err != nil || len(res.Files) != 2 || res.Files[0] != "a_t00.mkv" {
		t.Errorf("res = %+v, %v", res, err)
	}
}

func TestRunFailureCleansUp(t *testing.T) {
	out := t.TempDir()
	bin := script(t, `printf partial > "$7/x.mkv"; echo 'MSG:5003,0,0,"Failed","Failed"'; exit 1`+"\n")
	_, err := Run(context.Background(), req(t, bin, out))
	var re *makemkv.RipError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v", err)
	}
	assertOnlyTempRoot(t, out)
}

func TestRunNoOutput(t *testing.T) {
	out := t.TempDir()
	if _, err := Run(context.Background(), req(t, script(t, "exit 0\n"), out)); !errors.Is(err, ErrNoOutput) {
		t.Fatalf("err = %v", err)
	}
	assertOnlyTempRoot(t, out)
}

func TestRunCancelCleansUp(t *testing.T) {
	out := t.TempDir()
	bin := script(t, `printf partial > "$7/x.mkv"; echo 'PRGV:1,1,10'; sleep 30`+"\n")
	ctx, cancel := context.WithCancel(context.Background())
	r := req(t, bin, out)
	r.OnProgress = func(makemkv.Progress) { cancel() }
	done := make(chan error, 1)
	go func() { _, err := Run(ctx, r); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	assertOnlyTempRoot(t, out)
}

func TestRunNoSpace(t *testing.T) {
	out := t.TempDir()
	u, _ := space.Stat(out)
	r := req(t, script(t, writesOne), out)
	r.Titles = []makemkv.Title{title(int64(u.Free) + 1)}
	_, err := Run(context.Background(), r)
	var nr *space.NoRoomError
	if !errors.As(err, &nr) {
		t.Fatalf("err = %v", err)
	}
	if entries, _ := os.ReadDir(out); len(entries) != 0 {
		t.Errorf("output folder not empty: %v", entries)
	}
}

// assertOnlyTempRoot checks that nothing but an empty .blurip-tmp is left.
func assertOnlyTempRoot(t *testing.T, out string) {
	t.Helper()
	entries, _ := os.ReadDir(out)
	for _, e := range entries {
		if e.Name() != TempDirName {
			t.Errorf("unexpected %s in output folder", e.Name())
		}
	}
	if left, _ := os.ReadDir(filepath.Join(out, TempDirName)); len(left) != 0 {
		t.Errorf("temp folder not cleaned: %v", left)
	}
}

func TestCleanupTemp(t *testing.T) {
	out := t.TempDir()
	for _, j := range []string{"old1", "old2", "active"} {
		os.MkdirAll(TempDir(out, j), 0o755)
	}
	if err := CleanupTemp(out, "active"); err != nil {
		t.Fatal(err)
	}
	left, _ := os.ReadDir(filepath.Join(out, TempDirName))
	if len(left) != 1 || left[0].Name() != "active" {
		t.Errorf("left = %v", left)
	}
	if err := CleanupTemp(t.TempDir()); err != nil {
		t.Errorf("no temp folder: %v", err)
	}
}

func TestFolderName(t *testing.T) {
	cases := []struct{ name, label, want string }{
		{"Wild Card", "WILD_CARD", "Wild Card"},
		{"", "THE_MATRIX", "The Matrix"},
		{"", "STAR_WARS-EP4  DISC_1", "Star Wars Ep4 Disc 1"},
		{"AC/DC: Live", "", "AC-DC - Live"},
		{"The Lost World: Jurassic Park", "", "The Lost World - Jurassic Park"},
		{"Star Wars:Episode IV", "", "Star Wars-Episode IV"},
		{`Who? "Me" <3 a|b*`, "", "Who 'Me' -3 a-b"},
		{"Back\\Slash", "", "Back-Slash"},
		{"Ends with dots...", "", "Ends with dots"},
		{"CON", "", "CON_"},
		{"com1", "", "com1_"},
		{"Console", "", "Console"},
		{strings.Repeat("é", 150), "", strings.Repeat("é", 100)},
		{"..hidden", "", "hidden"},
		{"  ", "", "Untitled disc"},
		{"", "", "Untitled disc"},
		{"Tab\tand\nnewline", "", "Tab and newline"},
	}
	for _, c := range cases {
		if got := FolderName(c.name, c.label); got != c.want {
			t.Errorf("FolderName(%q, %q) = %q, want %q", c.name, c.label, got, c.want)
		}
	}
}

func TestRunSeveralTitles(t *testing.T) {
	out := t.TempDir()
	// $6 is the title ID.
	bin := script(t, `printf "t$6" > "$7/Disc_t0$6.mkv"`+"\n")
	r := req(t, bin, out)
	r.Titles = []makemkv.Title{titleID(3, 10), titleID(0, 20)}
	var seen []string
	r.OnTitle = func(i, n int, tt makemkv.Title) { seen = append(seen, fmt.Sprintf("%d/%d:%d", i, n, tt.ID)) }
	res, err := Run(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(seen, " ") != "1/2:3 2/2:0" {
		t.Errorf("OnTitle = %v", seen)
	}
	// Several files keep MakeMKV's names.
	if len(res.Files) != 2 || res.Files[0] != "Disc_t00.mkv" || res.Files[1] != "Disc_t03.mkv" || res.Bytes != 4 {
		t.Errorf("res = %+v", res)
	}
	if r.EstimatedBytes() != 30 {
		t.Errorf("EstimatedBytes = %d", r.EstimatedBytes())
	}
}

func TestRunSecondTitleFails(t *testing.T) {
	out := t.TempDir()
	bin := script(t, `[ "$6" = 2 ] && exit 1; printf x > "$7/a_t0$6.mkv"`+"\n")
	r := req(t, bin, out)
	r.Titles = []makemkv.Title{titleID(1, 1), titleID(2, 1)}
	_, err := Run(context.Background(), r)
	if err == nil || !strings.Contains(err.Error(), "title 2") {
		t.Fatalf("err = %v", err)
	}
	assertOnlyTempRoot(t, out)
}

func TestRunNeedsTitles(t *testing.T) {
	r := req(t, "x", t.TempDir())
	r.Titles = nil
	if _, err := Run(context.Background(), r); err == nil {
		t.Error("expected error")
	}
}

func TestRunKeepsRipWhenNameIsRejected(t *testing.T) {
	out := t.TempDir()
	r := req(t, script(t, writesOne), out)
	// Too long for any Linux file system (255-byte limit): every rename
	// using it fails, like ":" did on the exFAT drive.
	r.Name = strings.Repeat("x", 300)
	res, err := Run(context.Background(), r)
	if err != nil {
		t.Fatalf("a finished rip must not fail on its name: %v", err)
	}
	if res.Dir != filepath.Join(out, "blurip job1") || len(res.Files) != 1 || res.Files[0] != "Wild_Card_t01.mkv" {
		t.Errorf("res = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(res.Dir, "Wild_Card_t01.mkv")); err != nil {
		t.Error(err)
	}
}

func TestCleanupTempKeepsMarkedFolders(t *testing.T) {
	out := t.TempDir()
	os.MkdirAll(TempDir(out, "job-7"), 0o755)
	os.WriteFile(filepath.Join(TempDir(out, "job-7"), KeepMarker), nil, 0o644)
	os.MkdirAll(TempDir(out, "job-8"), 0o755)
	CleanupTemp(out)
	left, _ := os.ReadDir(filepath.Join(out, TempDirName))
	if len(left) != 1 || left[0].Name() != "job-7" {
		t.Errorf("left = %v", left)
	}
}

func TestRunDestDir(t *testing.T) {
	out := t.TempDir()
	r := req(t, script(t, writesOne), out)
	r.DestDir = filepath.Join(out, "DVD") // doesn't exist yet
	res, err := Run(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(out, "DVD", "Wild Card"); res.Dir != want {
		t.Errorf("Dir = %q, want %q", res.Dir, want)
	}
	if _, err := os.Stat(filepath.Join(res.Dir, "Wild Card.mkv")); err != nil {
		t.Error(err)
	}
}

func TestRunMissingOutputDir(t *testing.T) {
	out := filepath.Join(t.TempDir(), "unmounted")
	_, err := Run(context.Background(), req(t, script(t, writesOne), out))
	if err == nil || !strings.Contains(err.Error(), "is its drive mounted?") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("output folder was created: %v", err)
	}
}

func TestRunFileNamesMergeIntoExistingFolder(t *testing.T) {
	out := t.TempDir()
	season := filepath.Join(out, "Friends", "Season 01")
	os.MkdirAll(season, 0o755)
	os.WriteFile(filepath.Join(season, "Friends - S01E01.mkv"), []byte("old"), 0o644)
	os.WriteFile(filepath.Join(season, "Friends - S01E02.mkv"), []byte("old"), 0o644)

	// $6 is the title ID, $7 the output directory.
	bin := script(t, `printf "t$6" > "$7/title_t0$6.mkv"`+"\n")
	r := req(t, bin, out)
	r.Titles = []makemkv.Title{titleID(3, 10), titleID(4, 10)}
	r.DestDir = season
	r.FileNames = []string{"Friends - S01E02", "Friends - S01E03"}
	res, err := Run(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if res.Dir != season || strings.Join(res.Files, "|") != "Friends - S01E02 (2).mkv|Friends - S01E03.mkv" {
		t.Errorf("res = %+v", res)
	}
	for name, want := range map[string]string{
		"Friends - S01E01.mkv": "old", "Friends - S01E02.mkv": "old",
		"Friends - S01E02 (2).mkv": "t3", "Friends - S01E03.mkv": "t4",
	} {
		if b, _ := os.ReadFile(filepath.Join(season, name)); string(b) != want {
			t.Errorf("%s = %q, want %q", name, b, want)
		}
	}
	if _, err := os.Stat(TempDir(out, "job1")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("temp folder left behind: %v", err)
	}
}

func TestNoOutputExplains(t *testing.T) {
	out := t.TempDir()
	body := `echo 'MSG:5010,0,0,"Failed to open disc","%1"'; echo 'MSG:5036,0,1,"Copy complete. 1 titles saved.","%1"'; exit 0` + "\n"
	r := req(t, script(t, body), out)
	_, err := Run(context.Background(), r)
	if !errors.Is(err, ErrNoOutput) || !strings.Contains(err.Error(), "MakeMKV said: Failed to open disc / Copy complete. 1 titles saved.") {
		t.Errorf("err = %v", err)
	}
	// A wrapper script for MakeMKV's Flatpak: say how to let it use the folder.
	wrapper := filepath.Join(t.TempDir(), "makemkvcon")
	os.WriteFile(wrapper, []byte("#!/bin/sh\nexit 0\nflatpak run --command=makemkvcon com.makemkv.MakeMKV \"$@\"\n"), 0o755)
	r.MakeMKVCon = wrapper
	_, err = Run(context.Background(), r)
	if err == nil || !strings.Contains(err.Error(), "flatpak override --user --filesystem="+out+" com.makemkv.MakeMKV") {
		t.Errorf("err = %v", err)
	}
}
