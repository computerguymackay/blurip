package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/computerguymackay/blurip/internal/boxset"
	"github.com/computerguymackay/blurip/internal/handbrake"
	"github.com/computerguymackay/blurip/internal/makemkv"
	"github.com/computerguymackay/blurip/internal/prompt"
	"github.com/computerguymackay/blurip/internal/store"
)

// fakeSplitMakeMKV answers `info` with a disc like Spaced's: two short
// extras and one long title (2) holding every episode, 35 chapters. The
// volume label is read from dir/sr1.
func fakeSplitMakeMKV(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "makemkvcon")
	script := `#!/bin/sh
for a in "$@"; do last3="$last2"; last2="$last1"; last1="$a"; done
case " $* " in
*" info "*)
echo "CINFO:32,0,\"$(cat "` + dir + `/sr1")\""
cat <<'EOF2'
CINFO:1,6209,"DVD disc"
TCOUNT:3
TINFO:0,9,0,"0:14:51"
TINFO:0,8,0,"11"
TINFO:0,11,0,"427000"
TINFO:1,9,0,"0:03:09"
TINFO:1,8,0,"6"
TINFO:1,11,0,"111000"
TINFO:2,9,0,"2:50:28"
TINFO:2,8,0,"35"
TINFO:2,11,0,"6600000"
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

type splitHarness struct {
	*harness
	t      *testing.T
	labels string
}

func newSplitHarness(t *testing.T) *splitHarness {
	h := newHarness(t, mkvOK, "")
	h.sess.cfg = withEncodeConfig(t, h.sess.cfg)
	dir := t.TempDir()
	h.sess.cfg.MakeMKVCon = fakeSplitMakeMKV(t, dir)
	h.sess.canAsk, h.sess.queue = true, true
	h.sess.boxSet = &boxSetState{}
	return &splitHarness{harness: h, t: t, labels: dir}
}

func (h *splitHarness) disc(label, answers string) string {
	h.t.Helper()
	os.WriteFile(filepath.Join(h.labels, "sr1"), []byte(label), 0o644)
	h.out.Reset()
	h.sess.ask = prompt.NewPrompter(strings.NewReader(answers), h.out)
	h.drive.status = 4
	if err := h.sess.handleDisc(context.Background(), "/dev/sr1"); err != nil {
		h.t.Fatalf("%s: %v\n%s", label, err, h.out)
	}
	return h.out.String()
}

// A disc with every episode in one long title: blurip asks how many
// episodes it holds, shares the chapters out, rips the title as one file
// and queues an encode of each episode's chapters.
func TestBoxSetSplitTitle(t *testing.T) {
	ctx := context.Background()
	h := newSplitHarness(t)
	// 7 episodes (the guess), split evenly, rip, not a foreign show.
	out := h.disc("SPACED_S1_D1", "\n\n\n\n")
	for _, want := range []string{
		"title 2 is the only episode-length title, and is 2h50m28s long with 35 chapters",
		"How many episodes are in title 2 (2h50m28s, 35 chapters)?", "1) 1 episode (don't split it)",
		"2) 5 episodes of 7 chapters, about 34m6s each", "3) 7 episodes of 5 chapters, about 24m21s each",
		"4) 35 episodes, one chapter each, about 4m52s each", "5) Another number of episodes", "Choose [3]:",
		"7 episodes of about 24m21s:", "episode 1: chapters 1-5", "episode 7: chapters 31-35",
		"Episodes (7), going to Spaced/Season 01", "→ Spaced - S01E01-E07, split into 7 episodes when encoded",
		"chapters 6-10    → Spaced - S01E02", "Yes, rip as Spaced S01E01–S01E07", "queued for encoding: 7 encodes",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, boxset.OnlyOneEpisode) {
		t.Errorf("warned of only one episode:\n%s", out)
	}
	rip := filepath.Join(h.sess.cfg.OutputDir, "Spaced", "Season 01", "Spaced - S01E01-E07.mkv")
	if b, err := os.ReadFile(rip); err != nil || string(b) != "title2" {
		t.Errorf("rip = %q, %v", b, err)
	}
	es, _ := h.store.Encodes(ctx)
	if len(es) != 7 {
		t.Fatalf("queue = %+v", es)
	}
	for i, e := range es {
		name := boxset.FileName("Spaced", 1, i+1)
		ch := boxset.SplitEvenly(35, 7)[i].String()
		if e.SourcePath != rip || e.Title != name || e.Chapters != ch || e.DestDir != h.sess.cfg.ShowsDir ||
			e.DestName != filepath.Join("Spaced", "Season 01", name) {
			t.Errorf("entry %d = %+v", i, e)
		}
	}

	// The next disc carries on after the seventh episode.
	out = h.disc("SPACED_S1_D2", "\n\n\n")
	if !strings.Contains(out, "Yes, rip as Spaced S01E08–S01E14") {
		t.Errorf("second disc:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(h.sess.cfg.OutputDir, "Spaced", "Season 01", "Spaced - S01E08-E14.mkv")); err != nil {
		t.Error(err)
	}
	if b, _, _ := h.store.FindBoxSet(ctx, "Spaced", 1); b.NextEpisode != 15 {
		t.Errorf("next episode = %d", b.NextEpisode)
	}
	if out := runHistoryDetail(t, h.store, h.history(t)[0].ID); !strings.Contains(out, "split by chapters (1-5,6-10") {
		t.Errorf("history:\n%s", out)
	}

	// Added to the queue again by hand (encode -U): split the same way.
	add, err := splitAdded(ctx, h.store, nil, h.out, []store.Encode{queueEntryFor(h.sess.cfg, rip, "DVD")})
	if err != nil || len(add) != 7 || add[6].Chapters != "31-35" || add[6].Title != "Spaced - S01E07" {
		t.Errorf("added = %+v, %v", add, err)
	}
}

func runHistoryDetail(t *testing.T, s *store.Store, id int64) string {
	t.Helper()
	j, err := s.Job(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	d, _, err := s.EpisodesForDisc(context.Background(), j.DiscID)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	printJob(&b, j, &d)
	return b.String()
}

// Saying it's one episode rips it as one, as before.
func TestBoxSetSplitDeclined(t *testing.T) {
	h := newSplitHarness(t)
	out := h.disc("SPACED_S1_D1", "1\n\n\n")
	if !strings.Contains(out, "Yes, rip as Spaced S01E01\n") && !strings.Contains(out, "Yes, rip as Spaced S01E01 ") {
		t.Errorf("output:\n%s", out)
	}
	es, _ := h.store.Encodes(context.Background())
	if len(es) != 1 || es[0].Chapters != "" || es[0].Title != "Spaced - S01E01" {
		t.Errorf("queue = %+v", es)
	}
}

// The chapters of each episode can be changed; ranges that don't follow
// on are refused.
func TestAskSplitChange(t *testing.T) {
	title := makemkv.Title{ID: 2, Attrs: makemkv.Attrs{makemkv.AttrDuration: "2:50:28", makemkv.AttrChapterCount: "35"}}
	var out strings.Builder
	ask := prompt.NewPrompter(strings.NewReader("other\n3\n2\n1-10,12-20\n1-11,12-20,21-35\n\n"), &out)
	cs, err := askSplit(ask, &out, title, nil)
	if err != nil || boxset.FormatChapters(cs) != "1-11,12-20,21-35" {
		t.Errorf("split = %v, %v\n%s", cs, err, &out)
	}
	if !strings.Contains(out.String(), "should start at chapter 11") {
		t.Errorf("output:\n%s", &out)
	}
	// "Don't split it".
	cs, err = askSplit(prompt.NewPrompter(strings.NewReader("\n3\n"), &out), &out, title, nil)
	if err != nil || cs != nil {
		t.Errorf("declined: %v, %v", cs, err)
	}
}

// Angry Kid: 25 one-minute episodes, one chapter each, in a 29-minute
// title. No usual episode length fits, so it defaults to one episode, but
// one chapter each is on the list.
func TestAskSplitShortEpisodes(t *testing.T) {
	title := makemkv.Title{ID: 0, Attrs: makemkv.Attrs{makemkv.AttrDuration: "0:29:32", makemkv.AttrChapterCount: "25"}}
	var out strings.Builder
	cs, err := askSplit(prompt.NewPrompter(strings.NewReader("3\n\n"), &out), &out, title, nil)
	if err != nil || len(cs) != 25 || cs[0] != (boxset.Chapters{First: 1, Last: 1}) || cs[24] != (boxset.Chapters{First: 25, Last: 25}) {
		t.Errorf("split = %v, %v\n%s", cs, err, &out)
	}
	for _, want := range []string{"2) 5 episodes of 5 chapters, about 5m54s each",
		"3) 25 episodes, one chapter each, about 1m11s each", "Choose [1]:", "episode 25: chapters 25"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, &out)
		}
	}
	// The default: not split.
	if cs, err := askSplit(prompt.NewPrompter(strings.NewReader("\n"), &out), &out, title, nil); err != nil || cs != nil {
		t.Errorf("default = %v, %v", cs, err)
	}
}

// One episode of a title holding several: HandBrake encodes its chapters,
// the encode is checked against their length, and the rip is kept until
// its last episode is encoded.
func TestEncoderChapters(t *testing.T) {
	ctx := context.Background()
	h := newEncoderHarness(t)
	e := h.get(t)
	e.Title, e.Chapters = "Spaced - S01E02", "6-10"
	h.store.UpdateEncode(ctx, e)
	other := e
	other.Title, other.Chapters, other.DestName = "Spaced - S01E03", "11-15", "Spaced/Spaced - S01E03"
	otherID, _ := h.store.AddEncode(ctx, other)

	h.enc.scan = func(context.Context, string, string) (handbrake.ScanResult, error) {
		cs := make([]time.Duration, 15)
		for i := range cs {
			cs[i] = 4 * time.Minute
		}
		return handbrake.ScanResult{Duration: time.Hour, Chapters: cs}, nil
	}
	var scanned []string
	h.enc.duration = func(_ context.Context, _, file string) (time.Duration, error) {
		scanned = append(scanned, file)
		return 20 * time.Minute, nil // five 4-minute chapters
	}
	if err := h.enc.run(ctx); err != nil {
		t.Fatalf("%v\n%s", err, h.out)
	}
	if len(h.reqs) != 2 || h.reqs[0].Chapters != "6-10" || h.reqs[1].Chapters != "11-15" {
		t.Errorf("requests = %+v", h.reqs)
	}
	if len(scanned) != 2 {
		t.Errorf("only the encodes are checked, not the rip: %v", scanned)
	}
	first := h.get(t)
	if first.Status != store.EncodeDone || first.SourceDeleted || first.Length != 20*time.Minute || first.BytesIn != int64(len("a big rip"))/3 {
		t.Errorf("first = %+v\n%s", first, h.out)
	}
	last, _ := h.store.GetEncode(ctx, otherID)
	if last.Status != store.EncodeDone || !last.SourceDeleted {
		t.Errorf("last = %+v\n%s", last, h.out)
	}
	if !strings.Contains(h.out.String(), "rip kept: it is queued again") {
		t.Errorf("output:\n%s", h.out)
	}
}

// An encode shorter than its chapters fails, keeping the rip.
func TestEncoderChaptersTooShort(t *testing.T) {
	ctx := context.Background()
	h := newEncoderHarness(t)
	e := h.get(t)
	e.Chapters = "1-5"
	h.store.UpdateEncode(ctx, e)
	h.enc.scan = func(context.Context, string, string) (handbrake.ScanResult, error) {
		return handbrake.ScanResult{Duration: time.Hour, Chapters: []time.Duration{20 * time.Minute, 20 * time.Minute, 20 * time.Minute}}, nil
	}
	h.enc.run(ctx)
	if e := h.get(t); e.Status != store.EncodeFailed || !strings.Contains(e.FailureReason, "no chapters 1-5") {
		t.Errorf("entry = %+v", e)
	}
}

// A split disc put in again can't be fixed, but its season can be
// renamed: the one rip, the episodes' encodes and their queue entries all
// follow.
func TestSplitDiscRename(t *testing.T) {
	ctx := context.Background()
	h := newSplitHarness(t)
	h.disc("SPACED_S1_D1", "\n\n\n\n")
	es, _ := h.store.Encodes(ctx)
	shows := filepath.Join(h.sess.cfg.ShowsDir, "Spaced", "Season 01")
	os.MkdirAll(shows, 0o755)
	os.WriteFile(filepath.Join(shows, "Spaced - S01E02.mp4"), []byte("enc"), 0o644)
	e2 := es[1]
	e2.Status, e2.OutputPath, e2.FinishedAt = store.EncodeDone, filepath.Join(shows, "Spaced - S01E02.mp4"), time.Now()
	h.store.FinishEncode(ctx, e2)

	out := h.disc("SPACED_S1_D1", "2\nSpaced\n2\ny\n1\n")
	if !strings.Contains(out, "can't be fixed or ripped again") || !strings.Contains(out, "as Spaced S02E01–S02E07, with a title split") {
		t.Errorf("output:\n%s", out)
	}
	rip := filepath.Join(h.sess.cfg.OutputDir, "Spaced", "Season 02", "Spaced - S02E01-E07.mkv")
	if _, err := os.Stat(rip); err != nil {
		t.Error("rip not renamed:", err)
	}
	enc := filepath.Join(h.sess.cfg.ShowsDir, "Spaced", "Season 02", "Spaced - S02E02.mp4")
	if _, err := os.Stat(enc); err != nil {
		t.Error("encode not renamed:", err)
	}
	es, _ = h.store.Encodes(ctx)
	for i, e := range es {
		name := boxset.FileName("Spaced", 2, i+1)
		if e.SourcePath != rip || e.Title != name || e.DestName != filepath.Join("Spaced", "Season 02", name) {
			t.Errorf("entry %d = %+v", i, e)
		}
	}
	if es[1].OutputPath != enc {
		t.Errorf("output = %s", es[1].OutputPath)
	}
}

// boxset fix refuses a split disc, and won't renumber a split disc after
// the one being fixed.
func TestSplitDiscFix(t *testing.T) {
	h := newFixHarness(t)
	h.mustDisc("FRIENDS_S1_D1", "\n") // S01E01–S01E03
	ctx := context.Background()
	// Disc 2 of the season: one title split into three.
	d2, _ := h.store.UpsertDisc(ctx, store.Disc{Fingerprint: "split", VolumeLabel: "FRIENDS_S1_D2"}, time.Now())
	if _, err := h.store.AssignEpisodes(ctx, "Friends", 1, 2, d2.ID, []store.Episode{{TitleID: 0, Episode: 4, Chapters: "1-3,4-6"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	out, err := h.fixWithoutDisc("1\n2\n", "Friends", "1")
	if err == nil || !strings.Contains(err.Error(), "split into episodes by chapters") {
		t.Errorf("fixing the split disc: %v\n%s", err, out)
	}
	// Dropping an episode from disc 1 would renumber disc 2's.
	out, err = h.fixWithoutDisc("1\n1\n\n1,2\n", "Friends", "1")
	if err != nil || !strings.Contains(out, "can't be renumbered: S01E04 onwards are one title split by chapters") {
		t.Errorf("renumbering: %v\n%s", err, out)
	}
}
