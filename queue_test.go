package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/computerguymackay/blurip/internal/config"
	"github.com/computerguymackay/blurip/internal/handbrake"
	"github.com/computerguymackay/blurip/internal/prompt"
	"github.com/computerguymackay/blurip/internal/store"
)

func withEncodeConfig(t *testing.T, c config.Config) config.Config {
	c.HandBrakeCLI = "HandBrakeCLI"
	c.PresetDVD, c.PresetBluRay = "General/Fast 1080p30", "My Presets/720pFast"
	c.MoviesDir, c.ShowsDir = t.TempDir(), t.TempDir()
	c.DeleteAfterEncode = true
	return c
}

func queueHarness(t *testing.T, answers string, canAsk bool) (*harness, *syncBuffer) {
	h := newHarness(t, mkvOK, answers)
	out := &syncBuffer{}
	h.sess.out, h.sess.ask = out, prompt.NewPrompter(strings.NewReader(answers), out)
	h.sess.view = newBoard(out, false).Row("/dev/sr1", "Blu-ray drive (sr1)")
	h.sess.cfg = withEncodeConfig(t, h.sess.cfg)
	h.sess.queue, h.sess.canAsk, h.sess.pending = true, canAsk, &sync.WaitGroup{}
	return h, out
}

func TestQueueFilmForeign(t *testing.T) {
	h, out := queueHarness(t, "y\n", true)
	if err := h.sess.handleDisc(context.Background(), "/dev/sr1"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	h.sess.pending.Wait()
	es, err := h.store.Encodes(context.Background())
	if err != nil || len(es) != 1 {
		t.Fatalf("queue = %+v, %v\n%s", es, err, h.out)
	}
	e := es[0]
	want := filepath.Join(h.sess.cfg.OutputDir, "Wild Card", "Wild Card.mkv")
	if e.SourcePath != want || e.Title != "Wild Card" || e.DestDir != h.sess.cfg.MoviesDir ||
		e.DestName != filepath.Join("Wild Card", "Wild Card") || e.Preset != "My Presets/720pFast" || e.DiscKind != "Blu-ray" {
		t.Errorf("entry = %+v", e)
	}
	if !e.AllTracks || !e.TracksAnswered {
		t.Errorf("answered yes: AllTracks %v, answered %v", e.AllTracks, e.TracksAnswered)
	}
	if !strings.Contains(out.String(), "Is Wild Card a foreign film? [y/N]") || !strings.Contains(out.String(), "queued for encoding: 1 file") {
		t.Errorf("output:\n%s", out)
	}
}

func TestQueueFilmNoTerminal(t *testing.T) {
	h, out := queueHarness(t, "", false)
	if err := h.sess.handleDisc(context.Background(), "/dev/sr1"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	es, _ := h.store.Encodes(context.Background())
	if len(es) != 1 || es[0].AllTracks || es[0].TracksAnswered {
		t.Errorf("queue = %+v", es)
	}
}

// A code-named disc: the name and the foreign-film question are both
// answered after the rip was queued, so the queued entry follows them.
func TestQueueFollowsLateAnswers(t *testing.T) {
	h, out, w := codeNameHarness(t, mkvOK)
	h.sess.cfg = withEncodeConfig(t, h.sess.cfg)
	h.sess.queue = true
	if err := h.sess.handleDisc(context.Background(), "/dev/sr1"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	es, _ := h.store.Encodes(context.Background())
	if len(es) != 1 || es[0].Title != "SBK0X7AJ" || es[0].TracksAnswered {
		t.Fatalf("before the answers: %+v", es)
	}
	io.WriteString(w, "Ip Man\n")
	waitFor(t, out, "Is Ip Man a foreign film?")
	io.WriteString(w, "y\n")
	h.sess.pending.Wait()
	es, _ = h.store.Encodes(context.Background())
	e := es[0]
	if e.Title != "Ip Man" || e.SourcePath != filepath.Join(h.sess.cfg.OutputDir, "Ip Man", "Ip Man.mkv") ||
		e.DestName != filepath.Join("Ip Man", "Ip Man") || !e.AllTracks || !e.TracksAnswered {
		t.Errorf("after the answers: %+v\n%s", e, out)
	}
	if _, err := os.Stat(e.SourcePath); err != nil {
		t.Error(err)
	}
}

func TestNotQueuedWithoutQ(t *testing.T) {
	h := newHarness(t, mkvOK, "")
	if err := h.sess.handleDisc(context.Background(), "/dev/sr1"); err != nil {
		t.Fatal(err)
	}
	if es, _ := h.store.Encodes(context.Background()); len(es) != 0 {
		t.Errorf("queued without -Q: %+v", es)
	}
}

func TestBoxSetTracksAskedOncePerShow(t *testing.T) {
	h, out := queueHarness(t, "y\n", true) // only one answer: a second question would fail
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		all, err := h.sess.boxSetTracks(ctx, "Killjoys", func() {})
		if err != nil || all == nil || !*all {
			t.Fatalf("call %d: %v, %v\n%s", i, all, err, out)
		}
	}
	if n := strings.Count(out.String(), "foreign-language show?"); n != 1 {
		t.Errorf("asked %d times", n)
	}
	h.sess.queue = false
	if all, _ := h.sess.boxSetTracks(ctx, "Other", func() {}); all != nil {
		t.Error("asked without -Q")
	}
}

func TestQueueEntryFor(t *testing.T) {
	cfg := config.Config{OutputDir: "/rips", MoviesDir: "/m", ShowsDir: "/s", PresetDVD: "d", PresetBluRay: "b"}
	for src, want := range map[string][3]string{
		"/rips/Ip Man/Ip Man.mkv":                        {"Ip Man", "/m", "Ip Man/Ip Man"},
		"/rips/Killjoys/Season 01/Killjoys - S01E02.mkv": {"Killjoys - S01E02", "/s", "Killjoys/Season 01/Killjoys - S01E02"},
		"/rips/Extras Disc/title_t03.mkv":                {"Extras Disc - title_t03", "/m", "Extras Disc/title_t03"},
		"/rips/Loose.mkv":                                {"Loose", "/m", "Loose/Loose"},
	} {
		e := queueEntryFor(cfg, src, "Blu-ray")
		if got := [3]string{e.Title, e.DestDir, e.DestName}; got != want || e.Preset != "b" {
			t.Errorf("%s: %q, preset %q; want %q", src, got, e.Preset, want)
		}
	}
}

func TestAddAsksForeign(t *testing.T) {
	h := newEncoderHarness(t)
	ctx := context.Background()
	dir := filepath.Join(h.enc.cfg.OutputDir, "Killjoys", "Season 01")
	os.MkdirAll(dir, 0o755)
	for _, ep := range []string{"01", "02"} {
		os.WriteFile(filepath.Join(dir, "Killjoys - S01E"+ep+".mkv"), nil, 0o644)
	}
	film := filepath.Join(h.enc.cfg.OutputDir, "Ip Man", "Ip Man.mkv")
	os.MkdirAll(filepath.Dir(film), 0o755)
	os.WriteFile(film, nil, 0o644)

	var out strings.Builder
	// a → by path → the season folder → DVD preset → not foreign (asked
	// once for both episodes); a → by path → the film → Blu-ray → foreign.
	in := "a\n2\n" + dir + "\n1\nn\n" + "a\n2\n" + film + "\n2\ny\n" + "\n"
	noPresets := func() ([]handbrake.Category, error) { return nil, errors.New("not needed") }
	if err := editQueue(ctx, h.store, h.enc.cfg, prompt.NewPrompter(strings.NewReader(in), &out), &out, noPresets); err != nil {
		t.Fatalf("%v\n%s", err, &out)
	}
	if n := strings.Count(out.String(), "Is Killjoys a foreign-language show"); n != 1 {
		t.Errorf("show asked %d times\n%s", n, &out)
	}
	es, _ := h.store.Encodes(ctx, store.EncodeWaiting)
	got := map[string]bool{}
	for _, e := range es {
		if !e.TracksAnswered {
			t.Errorf("%s: not answered", e.Title)
		}
		got[e.Title] = e.AllTracks
	}
	if len(got) != 4 || got["Killjoys - S01E01"] || got["Killjoys - S01E02"] || !got["Ip Man"] {
		t.Errorf("tracks = %v\n%s", got, &out)
	}
	if all, ok, _ := h.store.ShowAllTracks(ctx, "Killjoys"); !ok || all {
		t.Errorf("show answer not remembered: %v %v", all, ok)
	}
}

func TestParseSelection(t *testing.T) {
	got, err := parseSelection("3-5, 1 4", 6)
	if err != nil || len(got) != 4 || got[0] != 3 || got[3] != 1 {
		t.Errorf("got %v, %v", got, err)
	}
	for _, bad := range []string{"0", "7", "5-3", "x", ""} {
		if _, err := parseSelection(bad, 6); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

// encoderHarness has a rip at rips/Wild Card/Wild Card.mkv queued for
// the movies folder.
type encoderHarness struct {
	enc   *encoder
	store *store.Store
	entry store.Encode
	src   string
	out   *strings.Builder
	reqs  []handbrake.Request
}

func newEncoderHarness(t *testing.T) *encoderHarness {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := withEncodeConfig(t, config.Config{OutputDir: t.TempDir()})
	src := filepath.Join(cfg.OutputDir, "Wild Card", "Wild Card.mkv")
	os.MkdirAll(filepath.Dir(src), 0o755)
	os.WriteFile(src, []byte("a big rip"), 0o644)
	e := store.Encode{SourcePath: src, DiscKind: "Blu-ray", Title: "Wild Card", Preset: cfg.PresetBluRay, TracksAnswered: true,
		DestDir: cfg.MoviesDir, DestName: "Wild Card/Wild Card", QueuedAt: time.Now()}
	e.Status = store.EncodeWaiting
	if e.ID, err = st.AddEncode(ctx, e); err != nil {
		t.Fatal(err)
	}
	cats := []handbrake.Category{
		{Name: "My Presets", Custom: true, Presets: []handbrake.Preset{{Category: "My Presets", Name: "720pFast", Format: "mp4"}}},
	}
	out := &strings.Builder{}
	h := &encoderHarness{store: st, entry: e, src: src, out: out}
	h.enc = newEncoder(cfg, st, out, false, cats)
	h.enc.awake = nil // no real sleep lock in tests
	h.enc.encode = func(ctx context.Context, r handbrake.Request) error {
		h.reqs = append(h.reqs, r)
		if r.OnProgress != nil {
			r.OnProgress(handbrake.Progress{Task: "Encoding", Fraction: 0.5})
		}
		return os.WriteFile(r.Output, []byte("small"), 0o644)
	}
	h.enc.duration = func(context.Context, string, string) (time.Duration, error) { return time.Hour, nil }
	return h
}

func (h *encoderHarness) get(t *testing.T) store.Encode {
	e, err := h.store.GetEncode(context.Background(), h.entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestEncoderSuccessDeletesRip(t *testing.T) {
	h := newEncoderHarness(t)
	if err := h.enc.run(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, h.out)
	}
	final := filepath.Join(h.enc.cfg.MoviesDir, "Wild Card", "Wild Card.mp4")
	if b, err := os.ReadFile(final); err != nil || string(b) != "small" {
		t.Errorf("encode = %q, %v\n%s", b, err, h.out)
	}
	if len(h.reqs) != 1 || h.reqs[0].Preset != "My Presets/720pFast" || h.reqs[0].Format != "mp4" || h.reqs[0].AllTracks {
		t.Errorf("requests = %+v", h.reqs)
	}
	if _, err := os.Stat(filepath.Dir(h.src)); !os.IsNotExist(err) {
		t.Errorf("the rip's folder should be gone: %v", err)
	}
	if _, err := os.Stat(h.enc.cfg.OutputDir); err != nil {
		t.Errorf("the rips folder itself must be kept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.enc.cfg.MoviesDir, encodeTempDirName)); !os.IsNotExist(err) {
		t.Errorf("temp folder left behind: %v", err)
	}
	e := h.get(t)
	if e.Status != store.EncodeDone || e.OutputPath != final || !e.SourceDeleted || e.BytesIn != 9 || e.BytesOut != 5 {
		t.Errorf("entry = %+v", e)
	}
	if !strings.Contains(h.out.String(), "✓ Wild Card → ") || !strings.Contains(h.out.String(), "rip deleted") {
		t.Errorf("output:\n%s", h.out)
	}
}

func TestEncoderForeignIsMKVAndKeepsRipWhenSetSo(t *testing.T) {
	h := newEncoderHarness(t)
	h.enc.cfg.DeleteAfterEncode = false
	e := h.entry
	e.AllTracks = true
	h.store.UpdateEncode(context.Background(), e)
	if err := h.enc.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(h.reqs) != 1 || h.reqs[0].Format != "mkv" || !h.reqs[0].AllTracks || !strings.HasSuffix(h.reqs[0].Output, ".mkv") {
		t.Errorf("requests = %+v", h.reqs)
	}
	if _, err := os.Stat(filepath.Join(h.enc.cfg.MoviesDir, "Wild Card", "Wild Card.mkv")); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(h.src); err != nil {
		t.Errorf("rip should be kept: %v", err)
	}
}

func TestEncoderForeignDVDStaysMP4(t *testing.T) {
	h := newEncoderHarness(t)
	e := h.entry
	e.AllTracks, e.DiscKind = true, "DVD"
	h.store.UpdateEncode(context.Background(), e)
	if err := h.enc.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(h.reqs) != 1 || h.reqs[0].Format != "mp4" || !h.reqs[0].AllTracks || !strings.HasSuffix(h.reqs[0].Output, ".mp4") {
		t.Errorf("requests = %+v", h.reqs)
	}
	if _, err := os.Stat(filepath.Join(h.enc.cfg.MoviesDir, "Wild Card", "Wild Card.mp4")); err != nil {
		t.Error(err)
	}
}

func TestEncoderFailuresKeepTheRip(t *testing.T) {
	for name, setup := range map[string]func(h *encoderHarness){
		"handbrake fails": func(h *encoderHarness) {
			h.enc.encode = func(context.Context, handbrake.Request) error {
				return errors.New("HandBrakeCLI failed: exit status 3")
			}
		},
		"too short": func(h *encoderHarness) {
			n := 0
			h.enc.duration = func(context.Context, string, string) (time.Duration, error) {
				n++
				if n == 2 {
					return 10 * time.Minute, nil
				}
				return time.Hour, nil
			}
		},
		"unknown preset": func(h *encoderHarness) {
			e := h.entry
			e.Preset = "Nope/Gone"
			h.store.UpdateEncode(context.Background(), e)
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newEncoderHarness(t)
			setup(h)
			if err := h.enc.run(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(h.src); err != nil {
				t.Errorf("rip deleted after a failure: %v", err)
			}
			if e := h.get(t); e.Status != store.EncodeFailed || e.FailureReason == "" {
				t.Errorf("entry = %+v", e)
			}
			entries, _ := os.ReadDir(h.enc.cfg.MoviesDir)
			for _, en := range entries {
				t.Errorf("left in the movies folder: %s", en.Name())
			}
			if !strings.Contains(h.out.String(), "(the rip was kept)") {
				t.Errorf("output:\n%s", h.out)
			}
		})
	}
}

func TestEncoderMissingDestinationStops(t *testing.T) {
	h := newEncoderHarness(t)
	os.RemoveAll(h.enc.cfg.MoviesDir)
	if err := h.enc.run(context.Background()); !errors.Is(err, errStopEncoding) {
		t.Fatalf("err = %v", err)
	}
	if e := h.get(t); e.Status != store.EncodeWaiting {
		t.Errorf("entry should stay waiting: %+v", e)
	}
	if _, err := os.Stat(h.enc.cfg.MoviesDir); !os.IsNotExist(err) {
		t.Error("the missing folder must not be created")
	}
}

func TestEncoderCtrlCRequeues(t *testing.T) {
	h := newEncoderHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	h.enc.encode = func(ctx context.Context, r handbrake.Request) error {
		os.WriteFile(r.Output, []byte("half"), 0o644)
		cancel()
		<-ctx.Done()
		return ctx.Err()
	}
	if err := h.enc.run(ctx); !errors.Is(err, errInterrupted) {
		t.Fatalf("err = %v", err)
	}
	if e := h.get(t); e.Status != store.EncodeWaiting {
		t.Errorf("entry = %+v", e)
	}
	if _, err := os.Stat(filepath.Join(h.enc.cfg.MoviesDir, encodeTempDirName, "encode-1")); !os.IsNotExist(err) {
		t.Errorf("partial encode left: %v", err)
	}
}

func TestEncoderQStopsAfterCurrent(t *testing.T) {
	h := newEncoderHarness(t)
	ctx := context.Background()
	other := filepath.Join(h.enc.cfg.OutputDir, "Other", "Other.mkv")
	os.MkdirAll(filepath.Dir(other), 0o755)
	os.WriteFile(other, []byte("rip"), 0o644)
	second := h.entry
	second.SourcePath, second.Title, second.DestName = other, "Other", "Other/Other"
	id2, _ := h.store.AddEncode(ctx, second)

	keys := make(chan byte)
	h.enc.keys = keys
	encode := h.enc.encode
	h.enc.encode = func(ctx context.Context, r handbrake.Request) error {
		keys <- 'q' // pressed during the first encode
		time.Sleep(50 * time.Millisecond)
		return encode(ctx, r)
	}
	if err := h.enc.run(ctx); err != nil {
		t.Fatal(err)
	}
	if e := h.get(t); e.Status != store.EncodeDone {
		t.Errorf("first = %+v", e)
	}
	if e, _ := h.store.GetEncode(ctx, id2); e.Status != store.EncodeWaiting {
		t.Errorf("second should still be waiting: %+v", e)
	}
	if !strings.Contains(h.out.String(), "Stopped; 1 left in the queue") {
		t.Errorf("output:\n%s", h.out)
	}
}

func TestEditQueue(t *testing.T) {
	h := newEncoderHarness(t)
	ctx := context.Background()
	var out strings.Builder
	// Entry 1 → Tracks → all; then entry 1 → Remove; then finish.
	p := prompt.NewPrompter(strings.NewReader("1\n2\n2\n1\n5\n\n"), &out)
	noPresets := func() ([]handbrake.Category, error) { return nil, errors.New("not needed") }
	if err := editQueue(ctx, h.store, h.enc.cfg, p, &out, noPresets); err != nil {
		t.Fatalf("%v\n%s", err, &out)
	}
	e := h.get(t)
	if !e.AllTracks || e.Status != store.EncodeRemoved {
		t.Errorf("entry = %+v\n%s", e, &out)
	}
	if !strings.Contains(out.String(), "Foreign") || !strings.Contains(out.String(), "The encode queue is empty.") {
		t.Errorf("output:\n%s", &out)
	}
}

func TestEncodeListFlagCombinations(t *testing.T) {
	db := filepath.Join(t.TempDir(), "b.db")
	for _, args := range [][]string{{"-L", "-U"}, {"--list", "--wait"}, {"-a"}} {
		if err := runEncode(context.Background(), db, args); err == nil {
			t.Errorf("encode %v: no error", args)
		}
	}
}

func queueOf(titles ...string) []store.Encode {
	var es []store.Encode
	for i, t := range titles {
		e := store.Encode{ID: int64(i + 1), Title: t, Status: store.EncodeWaiting, DestName: t + "/" + t}
		switch {
		case strings.HasPrefix(t, "dvd"):
			e.DiscKind = "DVD"
		case strings.HasPrefix(t, "bd"):
			e.DiscKind = "Blu-ray"
		}
		if show, ep, ok := strings.Cut(t, ":"); ok { // "dvdKilljoys:1" = episode 1 of Killjoys
			e.DestName = show + "/Season 01/" + show + " - S01E0" + ep
		}
		es = append(es, e)
	}
	return es
}

func titlesOf(es []store.Encode, ids []int64) string {
	byID := map[int64]string{}
	for _, e := range es {
		byID[e.ID] = e.Title
	}
	var out []string
	for _, id := range ids {
		out = append(out, byID[id])
	}
	return strings.Join(out, " ")
}

func TestMoveEntries(t *testing.T) {
	es := queueOf("A", "B", "C", "D", "E")
	es[0].Status = store.EncodeEncoding
	sel := map[int64]bool{4: true, 5: true} // D, E
	for _, c := range []struct {
		where  string
		before int64
		want   string
	}{
		{"top", 0, "A D E B C"}, // A is being encoded, so stays first
		{"bottom", 0, "A B C D E"},
		{"before", 3, "A B D E C"},
	} {
		if got := titlesOf(es, moveEntries(es, sel, c.where, c.before)); got != c.want {
			t.Errorf("%s %d: %s, want %s", c.where, c.before, got, c.want)
		}
	}
	if got := titlesOf(es, moveEntries(es, map[int64]bool{2: true}, "bottom", 0)); got != "A C D E B" {
		t.Errorf("B to the bottom: %s", got)
	}
}

func TestGroupQueue(t *testing.T) {
	es := queueOf("bdFilm1", "dvdK:1", "dvdFilm2", "bdBones:1", "dvdK:2", "x", "bdBones:2")
	for how, want := range map[string]string{
		"dvd":    "dvdK:1 dvdFilm2 dvdK:2 bdFilm1 bdBones:1 bdBones:2 x",
		"bluray": "bdFilm1 bdBones:1 bdBones:2 dvdK:1 dvdFilm2 dvdK:2 x",
		"tv":     "dvdK:1 dvdK:2 bdBones:1 bdBones:2 bdFilm1 dvdFilm2 x",
		"films":  "bdFilm1 dvdFilm2 x dvdK:1 dvdK:2 bdBones:1 bdBones:2",
	} {
		if got := titlesOf(es, encodeIDs(groupQueue(es, how))); got != want {
			t.Errorf("%s: %s\n want %s", how, got, want)
		}
	}
	// Back to the order added; one being encoded stays first.
	mixed := []store.Encode{es[3], es[0], es[2], es[1]}
	mixed[2].Status = store.EncodeEncoding
	if got := titlesOf(es, encodeIDs(groupQueue(mixed, "added"))); got != "dvdFilm2 bdFilm1 dvdK:1 bdBones:1" {
		t.Errorf("added: %s", got)
	}
}

func TestEditQueueMoveAndGroup(t *testing.T) {
	h := newEncoderHarness(t)
	ctx := context.Background()
	first := h.get(t)
	for _, title := range []string{"B", "C"} {
		e := first
		e.Title, e.DiscKind = title, "DVD"
		if _, err := h.store.AddEncode(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	order := func() string {
		es, _ := h.store.Encodes(ctx, store.EncodeWaiting)
		var ts []string
		for _, e := range es {
			ts = append(ts, e.Title)
		}
		return strings.Join(ts, " ")
	}
	start := order() // the harness's entry, then B, C
	var out strings.Builder
	// 3 (C) → Move → the top; then g → DVDs first; then finish.
	p := prompt.NewPrompter(strings.NewReader("3\n1\n1\ng\n1\n\n"), &out)
	noPresets := func() ([]handbrake.Category, error) { return nil, errors.New("not needed") }
	if err := editQueue(ctx, h.store, h.enc.cfg, p, &out, noPresets); err != nil {
		t.Fatalf("%v\n%s", err, &out)
	}
	want := "C B " + first.Title
	if first.DiscKind == "DVD" {
		want = "C " + first.Title + " B"
	}
	if got := order(); got != want {
		t.Errorf("queue = %s (from %s), want %s\n%s", got, start, want, &out)
	}
}
