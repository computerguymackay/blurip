package main

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/computerguymackay/blurip/internal/prompt"
)

// fakeBoxSetMakeMKV answers `info` with a TV disc whose volume label is read
// from labelFile: a "play all", three episodes and a trailer.
func fakeBoxSetMakeMKV(t *testing.T, labelFile string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "makemkvcon")
	script := `#!/bin/sh
for a in "$@"; do last2="$last1"; last1="$a"; done
case " $* " in
*" info "*)
echo "CINFO:32,0,\"$(cat ` + labelFile + `)\""
cat <<'EOF2'
CINFO:1,6209,"DVD disc"
TCOUNT:5
TINFO:0,9,0,"1:07:10"
TINFO:0,11,0,"3000"
TINFO:1,9,0,"0:22:10"
TINFO:1,11,0,"1000"
TINFO:2,9,0,"0:22:40"
TINFO:2,11,0,"1001"
TINFO:3,9,0,"0:22:20"
TINFO:3,11,0,"1002"
TINFO:4,9,0,"0:02:30"
TINFO:4,11,0,"50"
EOF2
;;
*" mkv "*)
printf "title$last2" > "$last1/title_t0$last2.mkv"
;;
esac
`
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBoxSetAcrossDiscs(t *testing.T) {
	h := newHarness(t, mkvOK, "")
	labelFile := filepath.Join(t.TempDir(), "label")
	h.sess.cfg.MakeMKVCon = fakeBoxSetMakeMKV(t, labelFile)
	h.sess.boxSet = &boxSetState{}
	ctx := context.Background()
	disc := func(label, answers string) string {
		t.Helper()
		os.WriteFile(labelFile, []byte(label), 0o644)
		h.out.Reset()
		h.sess.ask = prompt.NewPrompter(strings.NewReader(answers), h.out)
		h.drive.status = 4
		if err := h.sess.handleDisc(ctx, "/dev/sr1"); err != nil {
			t.Fatalf("%s: %v\n%s", label, err, h.out)
		}
		return h.out.String()
	}

	// First disc: shown and confirmed with Enter.
	out := disc("FRIENDS_S1_D1", "\n")
	for _, want := range []string{"read as: Friends, season 1, disc 1", "title 1", "→ Friends - S01E01", "→ Friends - S01E03",
		`all 3 episodes in one ("play all")`, "too short", "Yes, rip as Friends S01E01–S01E03"} {
		if !strings.Contains(out, want) {
			t.Errorf("first disc output lacks %q:\n%s", want, out)
		}
	}

	// Next disc: no questions (there are no answers to give).
	out = disc("FRIENDS_S1_D2", "")
	if strings.Contains(out, "Rip this disc") {
		t.Errorf("second disc asked:\n%s", out)
	}

	// Disc 4 after disc 2: warned and asked; renumbered by hand.
	out = disc("FRIENDS_S1_D4", "2\n\n\n10\n\n")
	if !strings.Contains(out, "disc 4, but the last disc was disc 2 (episodes up to S01E06)") {
		t.Errorf("no out-of-order warning:\n%s", out)
	}

	season := filepath.Join(h.sess.cfg.OutputDir, "Friends", "Season 01")
	entries, _ := os.ReadDir(season)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	want := []string{"Friends - S01E01.mkv", "Friends - S01E02.mkv", "Friends - S01E03.mkv",
		"Friends - S01E04.mkv", "Friends - S01E05.mkv", "Friends - S01E06.mkv",
		"Friends - S01E10.mkv", "Friends - S01E11.mkv", "Friends - S01E12.mkv"}
	if strings.Join(names, "|") != strings.Join(want, "|") {
		t.Errorf("files:\n%s", strings.Join(names, "\n"))
	}
	if b, _ := os.ReadFile(filepath.Join(season, "Friends - S01E05.mkv")); string(b) != "title2" {
		t.Errorf("S01E05 holds %q, want title2", b)
	}

	// New season: asked again, starting at episode 1.
	out = disc("FRIENDS_S2_D1", "\n")
	if !strings.Contains(out, "Yes, rip as Friends S02E01–S02E03") {
		t.Errorf("new season:\n%s", out)
	}
	jobs := h.history(t)
	if len(jobs) != 4 || jobs[0].OutputPath != filepath.Join(h.sess.cfg.OutputDir, "Friends", "Season 02") {
		t.Errorf("jobs: %+v", jobs)
	}
}

func TestBoxSetSkipDisc(t *testing.T) {
	h := newHarness(t, mkvOK, "")
	labelFile := filepath.Join(t.TempDir(), "label")
	os.WriteFile(labelFile, []byte("FRIENDS_S1_D1"), 0o644)
	h.sess.cfg.MakeMKVCon = fakeBoxSetMakeMKV(t, labelFile)
	h.sess.boxSet = &boxSetState{}
	h.sess.ask = prompt.NewPrompter(strings.NewReader("3\n"), h.out)
	if err := h.sess.handleDisc(context.Background(), "/dev/sr1"); err != nil {
		t.Fatal(err)
	}
	if jobs := h.history(t); len(jobs) != 1 || jobs[0].Status != "skipped" {
		t.Errorf("jobs: %+v", jobs)
	}
	if h.sess.boxSet.started {
		t.Error("a skipped first disc shouldn't start the box set")
	}
}
