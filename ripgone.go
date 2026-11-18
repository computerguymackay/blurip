package main

import (
	"context"
	"os"
	"path/filepath"

	"github.com/computerguymackay/blurip/internal/boxset"
	"github.com/computerguymackay/blurip/internal/store"
)

// earlierRip is what became of a disc's last successful rip.
type earlierRip struct {
	gone    bool   // its files have all been deleted (or moved)
	encoded string // where it was encoded to, if it was ("" = not)
}

// lookAtEarlierRip checks whether the files of a disc's last successful
// rip are still there. The files are, in order of preference: those
// queued for encoding from it (the exact paths), the episode files a box
// set disc was numbered as, or the MKVs in the rip's own folder. If none
// of them can be known, the rip counts as still there.
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
		d, numbered, err := s.store.EpisodesForDisc(ctx, discID)
		if err != nil {
			return r, err
		}
		if numbered {
			// A box set's rip folder is the season folder, shared with
			// the other discs: look for this disc's own episodes.
			for _, e := range d.Episodes {
				files = append(files, filepath.Join(prev.OutputPath, boxset.FileName(d.BoxSet.Show, d.BoxSet.Season, e.Episode)+".mkv"))
			}
		} else {
			files = mkvFilesIn(prev.OutputPath)
			if len(files) == 0 {
				return earlierRip{gone: true, encoded: r.encoded}, nil
			}
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
