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
