package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/computerguymackay/blurip/internal/config"
	"github.com/computerguymackay/blurip/internal/store"
)

// filmHarness has ripped Wild Card and queued it for encoding.
func filmHarness(t *testing.T) *harness {
	t.Helper()
	h, out := queueHarness(t, "n\nn\n", true) // keep the name; not foreign
	if err := h.sess.handleDisc(context.Background(), "/dev/sr1"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	h.sess.pending.Wait()
	if err := config.Save(context.Background(), h.store, h.sess.cfg); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) rename(t *testing.T, answers string, args ...string) (string, error) {
	t.Helper()
	var out strings.Builder
	err := filmRenameCmd(context.Background(), h.store.Path(), args, strings.NewReader(answers), &out)
	return out.String(), err
}

func (h *harness) entry(t *testing.T) store.Encode {
	t.Helper()
	es, err := h.store.Encodes(context.Background())
	if err != nil || len(es) != 1 {
		t.Fatalf("queue = %+v, %v", es, err)
	}
	return es[0]
}

// An encoded film: the rip, the encode (and its subtitles), the queue
// entry, the history and the disc's name all follow.
func TestRenameEncodedFilm(t *testing.T) {
	ctx := context.Background()
	h := filmHarness(t)
	e := h.entry(t)
	movie := filepath.Join(h.sess.cfg.MoviesDir, "Wild Card")
	os.MkdirAll(movie, 0o755)
	os.WriteFile(filepath.Join(movie, "Wild Card.mp4"), []byte("enc"), 0o644)
	os.WriteFile(filepath.Join(movie, "Wild Card.en.srt"), []byte("srt"), 0o644)
	e.Status, e.OutputPath, e.FinishedAt = store.EncodeDone, filepath.Join(movie, "Wild Card.mp4"), time.Now()
	h.store.FinishEncode(ctx, e)

	out, err := h.rename(t, "Wild Card (2015)\ny\n")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "The rename: Wild Card → Wild Card (2015)") || !strings.Contains(out, "✓ Wild Card renamed Wild Card (2015) (3 files, 2 folders)") {
		t.Errorf("output:\n%s", out)
	}
	rip := filepath.Join(h.sess.cfg.OutputDir, "Wild Card (2015)")
	if _, err := os.Stat(filepath.Join(rip, "Wild Card (2015).mkv")); err != nil {
		t.Error("rip not renamed:", err)
	}
	newMovie := filepath.Join(h.sess.cfg.MoviesDir, "Wild Card (2015)")
	for _, f := range []string{"Wild Card (2015).mp4", "Wild Card (2015).en.srt"} {
		if _, err := os.Stat(filepath.Join(newMovie, f)); err != nil {
			t.Error("encode not renamed:", err)
		}
	}
	for _, gone := range []string{filepath.Join(h.sess.cfg.OutputDir, "Wild Card"), movie} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s still there", gone)
		}
	}
	e = h.entry(t)
	if e.SourcePath != filepath.Join(rip, "Wild Card (2015).mkv") || e.Title != "Wild Card (2015)" ||
		e.DestName != filepath.Join("Wild Card (2015)", "Wild Card (2015)") || e.OutputPath != filepath.Join(newMovie, "Wild Card (2015).mp4") {
		t.Errorf("entry = %+v", e)
	}
	j := h.history(t)[0]
	if j.OutputPath != rip || j.DiscName != "Wild Card (2015)" {
		t.Errorf("history: %s, %s", j.OutputPath, j.DiscName)
	}
}

// A film waiting to be encoded: found by part of its name; its queue
// entry will encode under the new name.
func TestRenameQueuedFilm(t *testing.T) {
	h := filmHarness(t)
	if _, err := h.rename(t, "", "nope"); err == nil || !strings.Contains(err.Error(), `no ripped film matches "nope"`) {
		t.Errorf("no match: %v", err)
	}
	out, err := h.rename(t, "Joker\ny\n", "wild")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	e := h.entry(t)
	if e.Status != store.EncodeWaiting || e.SourcePath != filepath.Join(h.sess.cfg.OutputDir, "Joker", "Joker.mkv") ||
		e.DestName != filepath.Join("Joker", "Joker") {
		t.Errorf("entry = %+v", e)
	}
	if _, err := os.Stat(e.SourcePath); err != nil {
		t.Error(err)
	}
}

// Something already called the new name: refused, nothing changed.
func TestRenameFilmInTheWay(t *testing.T) {
	h := filmHarness(t)
	os.MkdirAll(filepath.Join(h.sess.cfg.OutputDir, "Joker"), 0o755)
	out, err := h.rename(t, "Joker\n")
	if err == nil || !strings.Contains(err.Error(), "is in the way") {
		t.Errorf("err = %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(h.sess.cfg.OutputDir, "Wild Card", "Wild Card.mkv")); err != nil {
		t.Error("rip changed:", err)
	}
	// Answering no changes nothing either.
	os.Remove(filepath.Join(h.sess.cfg.OutputDir, "Joker"))
	if out, _ := h.rename(t, "Joker\nn\n"); !strings.Contains(out, "Nothing was changed") {
		t.Errorf("output:\n%s", out)
	}
	if e := h.entry(t); e.Title != "Wild Card" {
		t.Errorf("entry changed: %+v", e)
	}
}

// Encoded straight into the films folder: only the film's own files
// there are renamed, not another film whose name starts the same.
func TestRenameFilmSharedFolder(t *testing.T) {
	ctx := context.Background()
	h := filmHarness(t)
	e := h.entry(t)
	movies := h.sess.cfg.MoviesDir
	os.MkdirAll(movies, 0o755)
	for _, f := range []string{"Wild Card.mp4", "Wild Card.en.srt", "Wild Card 2.mp4"} {
		os.WriteFile(filepath.Join(movies, f), []byte(f), 0o644)
	}
	e.Status, e.OutputPath, e.FinishedAt = store.EncodeDone, filepath.Join(movies, "Wild Card.mp4"), time.Now()
	h.store.FinishEncode(ctx, e)
	if out, err := h.rename(t, "Joker\ny\n"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, f := range []string{"Joker.mp4", "Joker.en.srt", "Wild Card 2.mp4"} {
		if _, err := os.Stat(filepath.Join(movies, f)); err != nil {
			t.Error(err)
		}
	}
	if e := h.entry(t); e.OutputPath != filepath.Join(movies, "Joker.mp4") {
		t.Errorf("output = %s", e.OutputPath)
	}
}

// Box set discs aren't films: they are renamed with boxset fix.
func TestRenameSkipsBoxSets(t *testing.T) {
	h := newFixHarness(t)
	h.mustDisc("FRIENDS_S1_D1", "\n")
	if rips, err := h.store.FilmRips(context.Background()); err != nil || len(rips) != 0 {
		t.Errorf("film rips = %+v, %v", rips, err)
	}
}
