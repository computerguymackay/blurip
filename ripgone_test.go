package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/computerguymackay/blurip/internal/drive"
	"github.com/computerguymackay/blurip/internal/prompt"
	"github.com/computerguymackay/blurip/internal/store"
)

// A film whose rip was deleted (after encoding, say) is asked about, even
// outside "ask every time" mode, and ripped again on yes.
func TestRipGoneAsksAgain(t *testing.T) {
	for _, tc := range []struct {
		name, answer string
		wantRip      bool
	}{{"yes", "y\n", true}, {"default no", "\n", false}} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, mkvOK, "")
			h.sess.canAsk = true
			ctx := context.Background()
			if err := h.sess.handleDisc(ctx, "/dev/sr1"); err != nil {
				t.Fatal(err)
			}
			film := filepath.Join(h.sess.cfg.OutputDir, "Wild Card")
			if err := os.Remove(filepath.Join(film, "Wild Card.mkv")); err != nil {
				t.Fatal(err)
			}
			h.out.Reset()
			h.sess.ask = prompt.NewPrompter(strings.NewReader(tc.answer), h.out)
			h.drive.status = drive.DiscOK
			if err := h.sess.handleDisc(ctx, "/dev/sr1"); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(h.out.String(), "those files have since been deleted") || !strings.Contains(h.out.String(), "Rip it again?") {
				t.Errorf("not asked:\n%s", h.out)
			}
			_, err := os.Stat(filepath.Join(film, "Wild Card.mkv"))
			if ripped := err == nil; ripped != tc.wantRip {
				t.Errorf("ripped again = %v, want %v\n%s", ripped, tc.wantRip, h.out)
			}
			jobs := h.history(t)
			want := store.StatusSkipped
			if tc.wantRip {
				want = store.StatusSucceeded
			}
			if len(jobs) != 2 || jobs[0].Status != want {
				t.Errorf("jobs = %+v", jobs)
			}
		})
	}
}

// A rip still there is skipped without a question, and with no one to ask
// a deleted one is skipped too.
func TestRipGoneOnlyWhenGoneAndAsked(t *testing.T) {
	h := newHarness(t, mkvOK, "")
	h.sess.canAsk = true
	ctx := context.Background()
	h.sess.handleDisc(ctx, "/dev/sr1")
	h.drive.status = drive.DiscOK
	h.out.Reset()
	if err := h.sess.handleDisc(ctx, "/dev/sr1"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.out.String(), "Rip it again?") || !strings.Contains(h.out.String(), "already ripped on") {
		t.Errorf("rip still there:\n%s", h.out)
	}

	os.RemoveAll(filepath.Join(h.sess.cfg.OutputDir, "Wild Card"))
	h.sess.canAsk = false
	h.drive.status = drive.DiscOK
	h.out.Reset()
	if err := h.sess.handleDisc(ctx, "/dev/sr1"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.out.String(), "Rip it again?") || !strings.Contains(h.out.String(), "already ripped on") {
		t.Errorf("no terminal:\n%s", h.out)
	}
}

// A box set disc's rip folder is the season folder: only its own episodes
// count, and ripped again it keeps its episode numbers.
func TestRipGoneBoxSet(t *testing.T) {
	h := newHarness(t, mkvOK, "")
	h.sess.canAsk = true
	dir := t.TempDir()
	h.sess.cfg.MakeMKVCon = fakeBoxSetMakeMKV(t, dir)
	h.sess.boxSet = &boxSetState{}
	ctx := context.Background()
	disc := func(label, answers string) string {
		t.Helper()
		os.WriteFile(filepath.Join(dir, "sr1"), []byte(label), 0o644)
		h.out.Reset()
		h.sess.ask = prompt.NewPrompter(strings.NewReader(answers), h.out)
		h.drive.status = drive.DiscOK
		if err := h.sess.handleDisc(ctx, "/dev/sr1"); err != nil {
			t.Fatalf("%s: %v\n%s", label, err, h.out)
		}
		return h.out.String()
	}
	disc("FRIENDS_S1_D1", "\n")
	disc("FRIENDS_S1_D2", "\n")
	season := filepath.Join(h.sess.cfg.OutputDir, "Friends", "Season 01")

	// Disc 1's episodes deleted; disc 2's still in the same folder.
	for _, ep := range []string{"S01E01", "S01E02", "S01E03"} {
		os.Remove(filepath.Join(season, "Friends - "+ep+".mkv"))
	}
	if out := disc("FRIENDS_S1_D2", ""); strings.Contains(out, "Rip it again?") {
		t.Errorf("disc 2 still there but asked:\n%s", out)
	}
	out := disc("FRIENDS_S1_D1", "y\n")
	if !strings.Contains(out, "those files have since been deleted") || !strings.Contains(out, "keeping Friends S01E01–S01E03") {
		t.Errorf("disc 1:\n%s", out)
	}
	if b, _ := os.ReadFile(filepath.Join(season, "Friends - S01E02.mkv")); string(b) != "title2" {
		t.Errorf("S01E02 holds %q, want title2", b)
	}
}
