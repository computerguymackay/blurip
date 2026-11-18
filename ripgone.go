package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/computerguymackay/blurip/internal/config"
	"github.com/computerguymackay/blurip/internal/makemkv"
	"github.com/computerguymackay/blurip/internal/store"
)

// alreadyRipped decides what to do with a disc that was ripped before:
//
//   - still in the drive since blurip last handled it (it wasn't ejected):
//     skipped without a question, so a restart doesn't treat it as new;
//   - a box set disc in box-set mode, with a terminal: the menu (skip, rip
//     it all again, or fix it; see askDoneBoxSetDisc), which returns fix;
//   - a box set disc otherwise: skipped, saying how to redo it;
//   - a film: ripped again if you say so ("ask every time" mode, or its
//     files were deleted), otherwise skipped.
//
// handled means it was skipped (recorded and ejected). With fix nil and
// handled false, it is ripped again as usual.
func (s *session) alreadyRipped(ctx context.Context, dev string, info *makemkv.Info, name string, discID int64,
	prev store.Job, job store.Job, holdAsk func()) (c ripChoice, fix *fixPlan, handled bool, err error) {
	dbctx := context.WithoutCancel(ctx)
	when := fmt.Sprintf("%s to %s", prev.StartedAt.Format("2006-01-02 15:04"), prev.OutputPath)
	skip := func(log, why string) (ripChoice, *fixPlan, bool, error) {
		s.view.Log("– %s: %s", name, log)
		job.FinishedAt, job.Status, job.FailureReason = s.now(), store.StatusSkipped, why
		if _, err := s.store.RecordJob(dbctx, job); err != nil {
			return c, nil, true, err
		}
		s.eject(ctx, dev)
		return c, nil, true, nil
	}

	if left, err := s.store.DriveLeftDisc(dbctx, dev); err != nil {
		return c, nil, false, err
	} else if left == discID && !s.once {
		return skip("still in the drive since blurip last handled it (it wasn't ejected); skipped. Take it out and put it back in to rip it again or fix it",
			fmt.Sprintf("already ripped (job %d); still in the drive since then", prev.ID))
	}

	d, numbered, err := s.store.EpisodesForDisc(dbctx, discID)
	if err != nil {
		return c, nil, false, err
	}
	if numbered {
		summary := episodeSummary(dbctx, s.store, discID)
		if s.boxSet == nil {
			return skip(fmt.Sprintf("already ripped as %s on %s; skipped (to rip it again or fix it, use blurip -B)", summary, when),
				fmt.Sprintf("already ripped successfully (job %d)", prev.ID))
		}
		if !s.canAsk {
			return skip(fmt.Sprintf("already ripped as %s on %s; skipped", summary, when),
				fmt.Sprintf("already ripped successfully (job %d)", prev.ID))
		}
		fix, why, err := s.askDoneBoxSetDisc(dbctx, info, name, d, discID, prev, holdAsk)
		if err != nil {
			return c, nil, false, err
		}
		if fix == nil {
			return skip(why, why)
		}
		c = ripChoice{Name: name, Folder: fix.folder}
		if s.queue {
			if c.AllTracks, err = s.boxSetTracks(dbctx, fix.box.Show, holdAsk); err != nil {
				return c, nil, false, err
			}
		}
		return c, fix, false, nil
	}

	again := false
	earlier, err := s.lookAtEarlierRip(dbctx, prev, discID)
	if err != nil {
		return c, nil, false, err
	}
	switch {
	case s.quiet: // -s: skipped, the default answer
	case earlier.gone && (s.canAsk || s.cfg.RipMode == config.RipAsk):
		// Its files were deleted (after encoding, say): it may well be in
		// the drive to be ripped again, so ask whatever the mode.
		holdAsk()
		fmt.Fprintf(s.out, "\n%s was ripped successfully on %s, but those files have since been deleted.\n", name, when)
		if earlier.encoded != "" {
			fmt.Fprintf(s.out, "It was encoded to %s.\n", earlier.encoded)
		}
		if again, err = s.ask.YesNo("Rip it again?", false); err != nil {
			return c, nil, false, err
		}
	case s.cfg.RipMode == config.RipAsk:
		holdAsk()
		if again, err = s.ask.YesNo(fmt.Sprintf("This disc was ripped successfully on %s. Rip it again?", when), false); err != nil {
			return c, nil, false, err
		}
	}
	if !again {
		return skip(fmt.Sprintf("already ripped on %s; skipped", when), fmt.Sprintf("already ripped successfully (job %d)", prev.ID))
	}
	return c, nil, false, nil
}

// earlierRip is what became of a disc's last successful rip.
type earlierRip struct {
	gone    bool   // its files have all been deleted (or moved)
	encoded string // where it was encoded to, if it was ("" = not)
}

// lookAtEarlierRip checks whether the files of a film's last successful
// rip are still there: those queued for encoding from it (the exact
// paths), or else the MKVs in its folder. If neither can be known, the
// rip counts as still there. (Box set discs get their own menu instead.)
func (s *session) lookAtEarlierRip(ctx context.Context, prev store.Job, discID int64) (earlierRip, error) {
	var r earlierRip
	var files []string
	encodes, err := s.store.EncodesForJob(ctx, prev.ID)
	if err != nil {
		return r, err
	}
	for _, e := range encodes {
		files = append(files, e.SourcePath)
		if e.Status == store.EncodeDone && r.encoded == "" {
			r.encoded = e.OutputPath
			if len(encodes) > 1 {
				r.encoded = filepath.Dir(e.OutputPath)
			}
		}
	}
	if len(files) == 0 && prev.OutputPath != "" {
		files = mkvFilesIn(prev.OutputPath)
		if len(files) == 0 {
			return earlierRip{gone: true, encoded: r.encoded}, nil
		}
	}
	if len(files) == 0 {
		return r, nil // nothing to go on
	}
	for _, f := range files {
		if _, err := os.Stat(f); err == nil {
			return r, nil
		}
	}
	r.gone = true
	return r, nil
}
