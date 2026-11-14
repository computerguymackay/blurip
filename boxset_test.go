package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/computerguymackay/blurip/internal/prompt"
	"github.com/computerguymackay/blurip/internal/store"
)

// fakeBoxSetMakeMKV answers `info` with a TV disc whose volume label is read
// from dir/<device name> (e.g. dir/sr1): a "play all", three episodes and a
// trailer. If dir/<device name>.slow exists, reading takes a second.
func fakeBoxSetMakeMKV(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "makemkvcon")
	script := `#!/bin/sh
for a in "$@"; do last3="$last2"; last2="$last1"; last1="$a"; done
case " $* " in
*" info "*)
dev="` + dir + `/${last1##*/}"
[ -e "$dev.slow" ] && sleep 1
echo "CINFO:32,0,\"$(cat "$dev")\""
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
if [ -n "$FAIL_MKV" ]; then echo 'MSG:5010,0,0,"Failed to save title"'; exit 1; fi
printf "title$last2" > "$last1/title_t0$last2.mkv"
if [ -n "$TAG_DEVICE" ]; then printf " ${last3##*/}" >> "$last1/title_t0$last2.mkv"; fi
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
	dir := t.TempDir()
	labelFile := filepath.Join(dir, "sr1")
	h.sess.cfg.MakeMKVCon = fakeBoxSetMakeMKV(t, dir)
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
	if !strings.Contains(out, "disc 4, but the last disc of Friends season 1 was disc 2 (episodes up to S01E06)") {
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
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "sr1"), []byte("FRIENDS_S1_D1"), 0o644)
	h.sess.cfg.MakeMKVCon = fakeBoxSetMakeMKV(t, dir)
	h.sess.boxSet = &boxSetState{}
	h.sess.ask = prompt.NewPrompter(strings.NewReader("3\n"), h.out)
	if err := h.sess.handleDisc(context.Background(), "/dev/sr1"); err != nil {
		t.Fatal(err)
	}
	if jobs := h.history(t); len(jobs) != 1 || jobs[0].Status != "skipped" {
		t.Errorf("jobs: %+v", jobs)
	}
	if len(h.sess.boxSet.ripping) != 0 {
		t.Error("a skipped first disc shouldn't start the box set")
	}
}

// With two drives, disc 2 read first must wait for disc 1 so that disc 1
// gets S01E01.
func TestBoxSetTwoDrivesNumberInDiscOrder(t *testing.T) {
	h := newHarness(t, mkvOK, "")
	ctx := context.Background()
	h.store.ReplaceDrives(ctx, []store.Drive{{Device: "/dev/sr0", Name: "DVD", Nickname: "DVD drive"}, {Device: "/dev/sr1", Name: "BD", Nickname: "Blu-ray drive"}})
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "sr0"), []byte("FRIENDS_S1_D1"), 0o644)
	os.WriteFile(filepath.Join(dir, "sr0.slow"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, "sr1"), []byte("FRIENDS_S1_D2"), 0o644)
	h.sess.cfg.MakeMKVCon = fakeBoxSetMakeMKV(t, dir)
	t.Setenv("TAG_DEVICE", "1") // each file records the drive it came from

	var askMu sync.Mutex
	bs := &boxSetState{}
	b := newBoard(h.out, false)
	ask := prompt.NewPrompter(strings.NewReader("\n"), h.out) // only disc 1 is confirmed
	mk := func(dev, label string) *session {
		return &session{cfg: h.sess.cfg, store: h.store, ctl: &fakeDrive{status: 4}, ask: ask, out: h.out,
			view: b.Row(dev, label), now: h.sess.now, boxSet: bs,
			askLock: func() func() { askMu.Lock(); return askMu.Unlock }}
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, dev := range []string{"/dev/sr0", "/dev/sr1"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = mk(dev, dev).handleDisc(ctx, dev)
		}()
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("%v %v\n%s", errs[0], errs[1], h.out)
	}
	out := h.out.String()
	if !strings.Contains(out, "Disc 2: waiting") || !strings.Contains(out, "Box set disc FRIENDS_S1_D1") ||
		strings.Contains(out, "Box set disc FRIENDS_S1_D2") || !strings.Contains(out, "Yes, rip as Friends S01E01–S01E03") {
		t.Errorf("output:\n%s", out)
	}
	season := filepath.Join(h.sess.cfg.OutputDir, "Friends", "Season 01")
	for ep, want := range map[string]string{"S01E01": "title1 sr0", "S01E03": "title3 sr0", "S01E04": "title1 sr1", "S01E06": "title3 sr1"} {
		if b, err := os.ReadFile(filepath.Join(season, "Friends - "+ep+".mkv")); err != nil || string(b) != want {
			t.Errorf("%s: %q %v", ep, b, err)
		}
	}
	if jobs := h.history(t); len(jobs) != 2 {
		t.Fatalf("jobs: %+v", jobs)
	}
}

// Two box sets at once keep their own episode numbers.
func TestBoxSetTwoShows(t *testing.T) {
	h := newHarness(t, mkvOK, "")
	dir := t.TempDir()
	labelFile := filepath.Join(dir, "sr1")
	h.sess.cfg.MakeMKVCon = fakeBoxSetMakeMKV(t, dir)
	h.sess.boxSet = &boxSetState{}
	disc := func(label, answers string) string {
		t.Helper()
		os.WriteFile(labelFile, []byte(label), 0o644)
		h.out.Reset()
		h.sess.ask = prompt.NewPrompter(strings.NewReader(answers), h.out)
		h.drive.status = 4
		if err := h.sess.handleDisc(context.Background(), "/dev/sr1"); err != nil {
			t.Fatalf("%s: %v\n%s", label, err, h.out)
		}
		return h.out.String()
	}
	disc("FRIENDS_S1_D1", "\n")
	if out := disc("LOST_S2_D1", "\n"); !strings.Contains(out, "Yes, rip as Lost S02E01–S02E03") {
		t.Errorf("Lost disc 1:\n%s", out)
	}
	if out := disc("FRIENDS_S1_D2", ""); strings.Contains(out, "Rip this disc") || !strings.Contains(out, "Friends S01E04–S01E06") {
		t.Errorf("Friends disc 2 should carry on without asking:\n%s", out)
	}
	if out := disc("LOST_S2_D2", ""); !strings.Contains(out, "Lost S02E04–S02E06") {
		t.Errorf("Lost disc 2:\n%s", out)
	}
	// No show in the label while two shows are being ripped: asked, and
	// naming the show and season picks up that season's numbering.
	out := disc("BOX_SET_DISC_3", "2\nLost\n2\n\n\n")
	if !strings.Contains(out, "doesn't say which show") || !strings.Contains(out, "First episode number on this disc (1-999) [7]") {
		t.Errorf("unlabelled disc:\n%s", out)
	}
	for _, f := range []string{"Friends/Season 01/Friends - S01E06.mkv", "Lost/Season 02/Lost - S02E06.mkv", "Lost/Season 02/Lost - S02E09.mkv"} {
		if _, err := os.Stat(filepath.Join(h.sess.cfg.OutputDir, f)); err != nil {
			t.Error(err)
		}
	}
}

// Progress is saved: after a restart the first disc shows where the season
// had got to and carries on from there; later discs don't ask.
func TestBoxSetCarriesOnAfterRestart(t *testing.T) {
	h := newHarness(t, mkvOK, "")
	dir := t.TempDir()
	labelFile := filepath.Join(dir, "sr1")
	h.sess.cfg.MakeMKVCon = fakeBoxSetMakeMKV(t, dir)
	disc := func(label, answers string) (string, error) {
		t.Helper()
		os.WriteFile(labelFile, []byte(label), 0o644)
		h.out.Reset()
		h.sess.ask = prompt.NewPrompter(strings.NewReader(answers), h.out)
		h.drive.status = 4
		err := h.sess.handleDisc(context.Background(), "/dev/sr1")
		return h.out.String(), err
	}

	h.sess.boxSet = &boxSetState{} // first run
	disc("FRIENDS_S1_D1", "\n")
	disc("FRIENDS_S1_D2", "")

	// Disc 3 fails.
	t.Setenv("FAIL_MKV", "1")
	if _, err := disc("FRIENDS_S1_D3", ""); err == nil {
		t.Fatal("disc 3 should have failed")
	}
	os.Unsetenv("FAIL_MKV")

	h.sess.boxSet = &boxSetState{} // blurip restarted
	out, err := disc("FRIENDS_S1_D4", "\n")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Carrying on Friends season 1 from S01E10 (last ripped: disc 3,") ||
		!strings.Contains(out, "Yes, rip as Friends S01E10–S01E12") {
		t.Errorf("after restart:\n%s", out)
	}
	// The failed disc goes back in: same numbers, no questions.
	out, err = disc("FRIENDS_S1_D3", "")
	if err != nil || !strings.Contains(out, "numbered before; keeping Friends S01E07–S01E09") {
		t.Errorf("disc 3 again: %v\n%s", err, out)
	}
	out, err = disc("FRIENDS_S1_D5", "")
	if err != nil || !strings.Contains(out, "Friends S01E13–S01E15") {
		t.Errorf("disc 5: %v\n%s", err, out)
	}
	entries, _ := os.ReadDir(filepath.Join(h.sess.cfg.OutputDir, "Friends", "Season 01"))
	if len(entries) != 15 {
		t.Errorf("%d files in the season folder, want 15", len(entries))
	}
}

// History shows each box-set disc's episodes; `blurip boxset` lists,
// corrects and forgets box sets.
func TestBoxSetHistoryAndCommand(t *testing.T) {
	h := newHarness(t, mkvOK, "")
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "sr1"), []byte("FRIENDS_S1_D2"), 0o644)
	h.sess.cfg.MakeMKVCon = fakeBoxSetMakeMKV(t, dir)
	h.sess.boxSet = &boxSetState{}
	h.sess.ask = prompt.NewPrompter(strings.NewReader("2\n\n\n4\n\n"), h.out) // part way through: starts at E04
	ctx := context.Background()
	if err := h.sess.handleDisc(ctx, "/dev/sr1"); err != nil {
		t.Fatalf("%v\n%s", err, h.out)
	}

	jobs := h.history(t)
	var out bytes.Buffer
	printHistory(&out, jobs, map[int64]string{jobs[0].DiscID: episodeSummary(ctx, h.store, jobs[0].DiscID)})
	if !strings.Contains(out.String(), "(Friends S01E04–S01E06)") {
		t.Errorf("history:\n%s", out.String())
	}
	d, _, _ := h.store.EpisodesForDisc(ctx, jobs[0].DiscID)
	out.Reset()
	printJob(&out, jobs[0], &d)
	for _, want := range []string{"Friends season 1, disc 2", "title 1 → Friends - S01E04", "title 3 → Friends - S01E06"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("job lacks %q:\n%s", want, out.String())
		}
	}

	db := h.store.Path()
	list := func() string {
		t.Helper()
		var b bytes.Buffer
		l, err := h.store.BoxSets(ctx)
		if err != nil {
			t.Fatal(err)
		}
		printBoxSets(&b, l)
		return b.String()
	}
	if got := list(); !strings.Contains(got, "Friends  1       1      3         2          S01E07") {
		t.Errorf("list:\n%s", got)
	}
	if err := runBoxSet(ctx, db, []string{"set", "friends", "1", "next", "10"}); err != nil {
		t.Fatal(err)
	}
	if got := list(); !strings.Contains(got, "S01E10") {
		t.Errorf("after set:\n%s", got)
	}
	if err := runBoxSet(ctx, db, []string{"set", "Lost", "1", "next", "2"}); err == nil || !strings.Contains(err.Error(), "blurip boxset") {
		t.Errorf("unknown show: %v", err)
	}
	if err := runBoxSet(ctx, db, []string{"forget", "Friends", "1"}); err != nil {
		t.Fatal(err)
	}
	if got := list(); !strings.Contains(got, "No box sets yet") {
		t.Errorf("after forget:\n%s", got)
	}
}

// A disc labelled with two seasons is checked every time; once the later
// season has been started, later discs default to it.
func TestBoxSetTwoSeasonLabel(t *testing.T) {
	h := newHarness(t, mkvOK, "")
	dir := t.TempDir()
	labelFile := filepath.Join(dir, "sr1")
	h.sess.cfg.MakeMKVCon = fakeBoxSetMakeMKV(t, dir)
	h.sess.boxSet = &boxSetState{}
	disc := func(label, answers string) string {
		t.Helper()
		os.WriteFile(labelFile, []byte(label), 0o644)
		h.out.Reset()
		h.sess.ask = prompt.NewPrompter(strings.NewReader(answers), h.out)
		h.drive.status = 4
		if err := h.sess.handleDisc(context.Background(), "/dev/sr1"); err != nil {
			t.Fatalf("%s: %v\n%s", label, err, h.out)
		}
		return h.out.String()
	}
	out := disc("COLUMBO_SEASON6AND7_DISC1", "\n")
	if !strings.Contains(out, "read as: Columbo, season 6, disc 1") || !strings.Contains(out, "covers seasons 6 and 7") ||
		!strings.Contains(out, "Yes, rip as Columbo S06E01–S06E03") {
		t.Errorf("disc 1:\n%s", out)
	}
	// Season 7 starts on disc 2: Change, keep the show, season 7, episode 1.
	out = disc("COLUMBO_SEASON6AND7_DISC2", "2\n\n7\n\n\n")
	if !strings.Contains(out, "Yes, rip as Columbo S06E04–S06E06") || !strings.Contains(out, "(1-999) [1]") {
		t.Errorf("disc 2:\n%s", out)
	}
	out = disc("COLUMBO_SEASON6AND7_DISC3", "\n")
	if !strings.Contains(out, "numbered as season 7") || !strings.Contains(out, "Yes, rip as Columbo S07E04–S07E06") {
		t.Errorf("disc 3:\n%s", out)
	}
	for _, f := range []string{"Season 06/Columbo - S06E03.mkv", "Season 07/Columbo - S07E01.mkv", "Season 07/Columbo - S07E06.mkv"} {
		if _, err := os.Stat(filepath.Join(h.sess.cfg.OutputDir, "Columbo", f)); err != nil {
			t.Error(err)
		}
	}
}
