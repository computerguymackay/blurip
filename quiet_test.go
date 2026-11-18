package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/computerguymackay/blurip/internal/config"
	"github.com/computerguymackay/blurip/internal/store"
)

// -s asks nothing: with no input at all, any question would fail.

// "Ask every time" mode rips the main feature; a film ripped before (even
// with its files deleted) is skipped.
func TestQuietFilm(t *testing.T) {
	h := newHarness(t, mkvOK, "")
	h.sess.quiet = true
	h.sess.cfg.RipMode = config.RipAsk
	if err := h.sess.handleDisc(context.Background(), "/dev/sr1"); err != nil {
		t.Fatalf("%v\n%s", err, h.out)
	}
	rip := filepath.Join(h.sess.cfg.OutputDir, "Wild Card")
	if _, err := os.Stat(filepath.Join(rip, "Wild Card.mkv")); err != nil {
		t.Fatalf("not ripped: %v\n%s", err, h.out)
	}
	os.RemoveAll(rip)
	if err := h.sess.handleDisc(context.Background(), "/dev/sr1"); err != nil {
		t.Fatalf("%v\n%s", err, h.out)
	}
	if j := h.history(t)[0]; j.Status != store.StatusSkipped {
		t.Errorf("second time: %s %s\n%s", j.Status, j.FailureReason, h.out)
	}
}

// Box-set mode takes each disc's episodes as detected, then skips a disc
// ripped before.
func TestQuietBoxSet(t *testing.T) {
	h := newFixHarness(t)
	h.sess.quiet, h.sess.canAsk = true, false
	out := h.mustDisc("FRIENDS_S1_D1", "")
	if !strings.Contains(out, "ripping as Friends S01E01–S01E03 (not checked: -s)") {
		t.Errorf("output:\n%s", out)
	}
	out = h.mustDisc("FRIENDS_S1_D2", "")
	if !strings.Contains(out, "S01E04–S01E06") {
		t.Errorf("disc 2:\n%s", out)
	}
	h.mustDisc("FRIENDS_S1_D1", "")
	if j := h.history(t)[0]; j.Status != store.StatusSkipped {
		t.Errorf("done disc: %s %s", j.Status, j.FailureReason)
	}
}
