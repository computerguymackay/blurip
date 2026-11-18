package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/computerguymackay/blurip/internal/config"
	"github.com/computerguymackay/blurip/internal/drive"
	"github.com/computerguymackay/blurip/internal/prompt"
	"github.com/computerguymackay/blurip/internal/store"
)

// queueing turns on -Q, with the foreign-language question answered.
func (h *fixHarness) queueing() {
	h.sess.queue = true
	h.store.SetShowAllTracks(context.Background(), "Friends", false)
}

// fixHarness rips box set discs from fakeBoxSetMakeMKV (titles 1–3 are
// episodes, title 4 a short extra), with a terminal and a shows folder.
type fixHarness struct {
	*harness
	t      *testing.T
	labels string
}

func newFixHarness(t *testing.T) *fixHarness {
	h := newHarness(t, mkvOK, "")
	h.sess.canAsk = true
	dir := t.TempDir()
	h.sess.cfg.MakeMKVCon = fakeBoxSetMakeMKV(t, dir)
	h.sess.cfg.ShowsDir = t.TempDir()
	h.sess.boxSet = &boxSetState{}
	return &fixHarness{harness: h, t: t, labels: dir}
}

// disc puts in the disc labelled label and answers the questions.
func (h *fixHarness) disc(label, answers string) (string, error) {
	h.t.Helper()
	os.WriteFile(filepath.Join(h.labels, "sr1"), []byte(label), 0o644)
	h.out.Reset()
	h.sess.ask = prompt.NewPrompter(strings.NewReader(answers), h.out)
	h.drive.status = drive.DiscOK
	err := h.sess.handleDisc(context.Background(), "/dev/sr1")
	return h.out.String(), err
}

func (h *fixHarness) mustDisc(label, answers string) string {
	h.t.Helper()
	out, err := h.disc(label, answers)
	if err != nil {
		h.t.Fatalf("%s: %v\n%s", label, err, out)
	}
	return out
}

func (h *fixHarness) season() string {
	return filepath.Join(h.sess.cfg.OutputDir, "Friends", "Season 01")
}

func (h *fixHarness) shows() string {
	return filepath.Join(h.sess.cfg.ShowsDir, "Friends", "Season 01")
}

// files lists "name=content" for every file in dir.
func files(t *testing.T, dir string) string {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	var out []string
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		out = append(out, strings.TrimPrefix(e.Name(), "Friends - ")+"="+string(b))
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

// encoded pretends the queue entry for episode code was encoded: its
// file (and a subtitle beside it) is in the shows folder.
func (h *fixHarness) encoded(code string) store.Encode {
	h.t.Helper()
	ctx := context.Background()
	src := filepath.Join(h.season(), "Friends - "+code+".mkv")
	es, err := h.store.EncodesForSource(ctx, src)
	if err != nil || len(es) != 1 {
		h.t.Fatalf("encodes for %s: %v, %v", code, es, err)
	}
	e := es[0]
	os.MkdirAll(h.shows(), 0o755)
	out := filepath.Join(h.shows(), "Friends - "+code+".mp4")
	os.WriteFile(out, []byte("enc "+code), 0o644)
	os.WriteFile(filepath.Join(h.shows(), "Friends - "+code+".en.srt"), []byte("srt "+code), 0o644)
	e.Status, e.OutputPath, e.FinishedAt = store.EncodeDone, out, time.Now()
	if err := h.store.FinishEncode(ctx, e); err != nil {
		h.t.Fatal(err)
	}
	return e
}

func (h *fixHarness) discID(label string) int64 {
	h.t.Helper()
	for _, j := range h.history(h.t) {
		if j.DiscName == strings.ReplaceAll(label, "_", " ") || strings.Contains(j.DiscName, label) {
			return j.DiscID
		}
	}
	h.t.Fatalf("no job for %s", label)
	return 0
}

func episodesOf(t *testing.T, s *store.Store, discID int64) string {
	t.Helper()
	d, ok, err := s.EpisodesForDisc(context.Background(), discID)
	if err != nil || !ok {
		t.Fatalf("EpisodesForDisc: %v %v", ok, err)
	}
	var out []string
	for _, e := range d.Episodes {
		out = append(out, strings.Join([]string{itoa(e.TitleID), itoa(e.Episode)}, ":"))
	}
	return strings.Join(out, " ")
}

func itoa(n int) string { return strconv.Itoa(n) }

// The extra was ripped as an episode and an episode missed; fixing the
// disc rips only the missed one, deletes the extra and leaves the next
// disc alone (the count is the same).
func TestFixDiscSwapExtraForEpisode(t *testing.T) {
	h := newFixHarness(t)
	h.queueing()
	h.mustDisc("FRIENDS_S1_D1", "2\n\n\n\n1,2,4\n") // Change: titles 1, 2 and the extra
	h.mustDisc("FRIENDS_S1_D2", "\n")
	h.encoded("S01E03")

	out := h.mustDisc("FRIENDS_S1_D1", "3\n1,2,3\ny\n")
	for _, want := range []string{"was ripped on", "title 4", "→ S01E03  rip ✓  encoded ✓", "Skip it",
		"S01E01  title 1", "unchanged", "S01E03  title 3", "rip now",
		"delete  title 4", "(was S01E03)", "Later discs: no change"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if got := files(t, h.season()); got != "S01E01.mkv=title1 S01E02.mkv=title2 S01E03.mkv=title3 S01E04.mkv=title1 S01E05.mkv=title2 S01E06.mkv=title3" {
		t.Errorf("rips: %s", got)
	}
	if got := files(t, h.shows()); got != "" {
		t.Errorf("the extra's encode wasn't deleted: %s", got)
	}
	if got := episodesOf(t, h.store, h.discID("S1 D1")); got != "1:1 2:2 3:3" {
		t.Errorf("numbering: %s", got)
	}
	b, _, _ := h.store.FindBoxSet(context.Background(), "Friends", 1)
	if b.NextEpisode != 7 {
		t.Errorf("next episode = %d", b.NextEpisode)
	}
	// The new episode is queued; the extra's finished encode is history.
	es, _ := h.store.EncodesForSource(context.Background(), filepath.Join(h.season(), "Friends - S01E03.mkv"))
	if len(es) != 2 || es[0].Status != store.EncodeDone || es[1].Status != store.EncodeWaiting {
		t.Errorf("queue: %+v", es)
	}
	j := h.history(t)[0]
	if j.Status != store.StatusSucceeded || !strings.Contains(j.Note, "title 3 ripped as S01E03") || !strings.Contains(j.Note, "S01E03 (title 4) deleted") {
		t.Errorf("job: %+v", j)
	}
}

// A missed episode makes the disc one longer: every later episode moves
// on by one (rips, encoded files, subtitles and queue entries).
func TestFixDiscShiftsLaterEpisodes(t *testing.T) {
	h := newFixHarness(t)
	h.queueing()
	h.mustDisc("FRIENDS_S1_D1", "2\n\n\n\n1,2\n") // title 3 missed
	h.mustDisc("FRIENDS_S1_D2", "\n")             // S01E03–S01E05
	done := h.encoded("S01E03")

	out := h.mustDisc("FRIENDS_S1_D1", "3\n1,2,3\ny\n")
	if !strings.Contains(out, "Later discs: S01E03–S01E05 become S01E04–S01E06") {
		t.Errorf("plan:\n%s", out)
	}
	if got := files(t, h.season()); got != "S01E01.mkv=title1 S01E02.mkv=title2 S01E03.mkv=title3 S01E04.mkv=title1 S01E05.mkv=title2 S01E06.mkv=title3" {
		t.Errorf("rips: %s", got)
	}
	if got := files(t, h.shows()); got != "S01E04.en.srt=srt S01E03 S01E04.mp4=enc S01E03" {
		t.Errorf("encodes: %s", got)
	}
	e, _ := h.store.GetEncode(context.Background(), done.ID)
	if e.SourcePath != filepath.Join(h.season(), "Friends - S01E04.mkv") || e.OutputPath != filepath.Join(h.shows(), "Friends - S01E04.mp4") ||
		e.Title != "Friends - S01E04" || e.DestName != filepath.Join("Friends", "Season 01", "Friends - S01E04") {
		t.Errorf("encode entry: %+v", e)
	}
	waiting, _ := h.store.EncodesForSource(context.Background(), filepath.Join(h.season(), "Friends - S01E06.mkv"))
	if len(waiting) != 1 || waiting[0].Title != "Friends - S01E06" || waiting[0].Status != store.EncodeWaiting {
		t.Errorf("waiting entry for the old S01E05: %+v", waiting)
	}
	if got := episodesOf(t, h.store, h.discID("S1 D2")); got != "1:4 2:5 3:6" {
		t.Errorf("disc 2 numbering: %s", got)
	}
	b, _, _ := h.store.FindBoxSet(context.Background(), "Friends", 1)
	if b.NextEpisode != 7 {
		t.Errorf("next episode = %d", b.NextEpisode)
	}

	// And the next new disc carries on after it.
	if out := h.mustDisc("FRIENDS_S1_D3", "\n"); !strings.Contains(out, "Friends S01E07–S01E09") {
		t.Errorf("disc 3:\n%s", out)
	}
}

// Titles in the wrong order are swapped by renaming; nothing is ripped.
func TestFixDiscReorder(t *testing.T) {
	h := newFixHarness(t)
	h.mustDisc("FRIENDS_S1_D1", "\n")
	out := h.mustDisc("FRIENDS_S1_D1", "3\n2,1,3\ny\n")
	if !strings.Contains(out, "rename (was S01E02)") || strings.Contains(out, "rip now") {
		t.Errorf("plan:\n%s", out)
	}
	if got := files(t, h.season()); got != "S01E01.mkv=title2 S01E02.mkv=title1 S01E03.mkv=title3" {
		t.Errorf("rips: %s", got)
	}
	if j := h.history(t)[0]; j.RipsAtOnce != 0 || j.BytesWritten != 0 {
		t.Errorf("something was ripped: %+v", j)
	}
}

// A failed rip changes nothing.
func TestFixDiscRipFails(t *testing.T) {
	h := newFixHarness(t)
	h.mustDisc("FRIENDS_S1_D1", "2\n\n\n\n1,2\n")
	h.mustDisc("FRIENDS_S1_D2", "\n")
	before := files(t, h.season())
	t.Setenv("FAIL_MKV", "1")
	if _, err := h.disc("FRIENDS_S1_D1", "3\n1,2,3\ny\n"); !errors.Is(err, errRipFailed) {
		t.Fatalf("err = %v", err)
	}
	if got := files(t, h.season()); got != before {
		t.Errorf("files changed:\n%s\n%s", before, got)
	}
	if got := episodesOf(t, h.store, h.discID("S1 D2")); got != "1:3 2:4 3:5" {
		t.Errorf("disc 2 renumbered: %s", got)
	}
	if j := h.history(t)[0]; j.Status != store.StatusFailed || !strings.Contains(j.FailureReason, "nothing was renamed or deleted") {
		t.Errorf("job: %+v", j)
	}
}

// Ripping the whole disc again replaces the rips (no "(2)" copies) and
// doesn't queue an episode twice.
func TestFixDiscRipAgain(t *testing.T) {
	h := newFixHarness(t)
	h.queueing()
	h.mustDisc("FRIENDS_S1_D1", "\n")
	h.encoded("S01E01")
	os.WriteFile(filepath.Join(h.season(), "Friends - S01E02.mkv"), []byte("damaged"), 0o644)
	h.mustDisc("FRIENDS_S1_D1", "2\n")
	if got := files(t, h.season()); got != "S01E01.mkv=title1 S01E02.mkv=title2 S01E03.mkv=title3" {
		t.Errorf("rips: %s", got)
	}
	all, _ := h.store.Encodes(context.Background())
	if len(all) != 4 { // 3 first time (1 done) + S01E01 again
		t.Errorf("queue: %+v", all)
	}
}

// Skip is the default; without -B a box set disc is skipped with a hint;
// a disc left in the drive (not ejected) gets no menu.
func TestFixDiscSkips(t *testing.T) {
	h := newFixHarness(t)
	h.mustDisc("FRIENDS_S1_D1", "\n")
	if out := h.mustDisc("FRIENDS_S1_D1", "\n"); !strings.Contains(out, "skipped at your request") {
		t.Errorf("default:\n%s", out)
	}

	h.sess.boxSet = nil
	if out := h.mustDisc("FRIENDS_S1_D1", ""); !strings.Contains(out, "use blurip -B") || strings.Contains(out, "What now?") {
		t.Errorf("without -B:\n%s", out)
	}
	h.sess.boxSet = &boxSetState{}

	// The eject fails: the disc is recorded as left in the drive.
	h.drive.ejectErr = errors.New("jammed")
	h.mustDisc("FRIENDS_S1_D1", "\n")
	h.drive.ejectErr = nil
	if out := h.mustDisc("FRIENDS_S1_D1", ""); !strings.Contains(out, "still in the drive") || strings.Contains(out, "What now?") {
		t.Errorf("left in the drive:\n%s", out)
	}
	// That eject worked, so put back in, it gets the menu again.
	if out := h.mustDisc("FRIENDS_S1_D1", "\n"); !strings.Contains(out, "What now?") {
		t.Errorf("after an eject:\n%s", out)
	}
}

// fixWithoutDisc runs blurip boxset fix with these answers.
func (h *fixHarness) fixWithoutDisc(answers string, args ...string) (string, error) {
	h.t.Helper()
	ctx := context.Background()
	if err := config.Save(ctx, h.store, h.sess.cfg); err != nil {
		h.t.Fatal(err)
	}
	var out strings.Builder
	err := boxSetFix(ctx, h.store.Path(), args, strings.NewReader(answers), &out, false, true)
	return out.String(), err
}

// Without the disc: the encoded episodes (rips deleted) are put in the
// right order by renaming.
func TestBoxSetFixReorderWithoutDisc(t *testing.T) {
	h := newFixHarness(t)
	h.queueing()
	h.mustDisc("FRIENDS_S1_D1", "\n")
	h.mustDisc("FRIENDS_S1_D2", "\n")
	for _, c := range []string{"S01E01", "S01E02", "S01E03"} {
		h.encoded(c)
		os.Remove(filepath.Join(h.season(), "Friends - "+c+".mkv"))
	}
	out, err := h.fixWithoutDisc("2,1,3\ny\n", "friends", "1", "1")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"→ S01E01  rip –  encoded ✓", "S01E01  title 2", "rename (was S01E02)", "Later discs: no change", "S01E01 renamed S01E02"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	got := files(t, h.shows())
	want := "S01E01.en.srt=srt S01E02 S01E01.mp4=enc S01E02 S01E02.en.srt=srt S01E01 S01E02.mp4=enc S01E01 S01E03.en.srt=srt S01E03 S01E03.mp4=enc S01E03"
	if got != want {
		t.Errorf("encodes:\n%s\nwant\n%s", got, want)
	}
	if got := episodesOf(t, h.store, h.discID("S1 D1")); got != "2:1 1:2 3:3" {
		t.Errorf("numbering: %s", got)
	}
	if j := h.history(t)[0]; j.Status != store.StatusSucceeded || !strings.HasPrefix(j.Note, "fixed:") {
		t.Errorf("job: %+v", j)
	}
}

// Without the disc: an extra is deleted and the next disc moves back.
func TestBoxSetFixDropExtraWithoutDisc(t *testing.T) {
	h := newFixHarness(t)
	h.mustDisc("FRIENDS_S1_D1", "2\n\n\n\n1,2,4\n") // the extra as S01E03
	h.mustDisc("FRIENDS_S1_D2", "\n")               // S01E04–S01E06
	// No disc number: asked which.
	out, err := h.fixWithoutDisc("1\n1,2\ny\n", "Friends", "1")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "Which disc of Friends season 1?") || !strings.Contains(out, "S01E04–S01E06 become S01E03–S01E05") {
		t.Errorf("output:\n%s", out)
	}
	if got := files(t, h.season()); got != "S01E01.mkv=title1 S01E02.mkv=title2 S01E03.mkv=title1 S01E04.mkv=title2 S01E05.mkv=title3" {
		t.Errorf("rips: %s", got)
	}
	b, _, _ := h.store.FindBoxSet(context.Background(), "Friends", 1)
	if b.NextEpisode != 6 {
		t.Errorf("next episode = %d", b.NextEpisode)
	}
}

// A title with nothing left has to be ripped: that needs the disc.
func TestBoxSetFixNeedsDisc(t *testing.T) {
	h := newFixHarness(t)
	h.mustDisc("FRIENDS_S1_D1", "\n")
	os.Remove(filepath.Join(h.season(), "Friends - S01E02.mkv"))
	before := files(t, h.season())
	out, err := h.fixWithoutDisc("2,1,3\n", "Friends", "1", "1")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "title 2 has no rip or encoded file left") || strings.Contains(out, "Go ahead?") {
		t.Errorf("output:\n%s", out)
	}
	if got := files(t, h.season()); got != before {
		t.Errorf("files changed: %s", got)
	}

	if _, err := h.fixWithoutDisc("", "Friends", "1", "7"); err == nil || !strings.Contains(err.Error(), "has no disc 7") {
		t.Errorf("unknown disc: %v", err)
	}
	if _, err := h.fixWithoutDisc("", "Nope", "1"); err == nil {
		t.Error("unknown box set accepted")
	}
	lock, err := store.AcquireLock(h.store.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if _, err := h.fixWithoutDisc("", "Friends", "1", "1"); err == nil || !strings.Contains(err.Error(), "stop blurip") {
		t.Errorf("while blurip runs: %v", err)
	}
}
