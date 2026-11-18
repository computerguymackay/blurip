package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/computerguymackay/blurip/internal/boxset"
	"github.com/computerguymackay/blurip/internal/makemkv"
	"github.com/computerguymackay/blurip/internal/store"
)

// A box set put under the wrong show name or season number can be
// renamed as a whole: every episode's files (rips, encoded files and the
// subtitle files beside them) move to the new "Show/Season NN" folders
// with the new names, along with the encode queue, the discs' rip folders
// in the history and the box set itself.

// seasonRename is how a box set is to be renamed.
type seasonRename struct {
	box        store.BoxSet
	show       string
	season     int
	oldFolder  string // "Show/Season 08"
	newFolder  string
	episodes   int
	ops        *fixOps
	dirs       [][2]string // rip folders (old, new)
	leaveEmpty []string    // folders to remove afterwards if nothing is left in them
}

// movedDir is where the files in dir go: a folder ending in the season's
// folder ("…/Red Dwarf/Season 08") moves to the new one; any other folder
// stays where it is.
func (r *seasonRename) movedDir(dir string) string {
	dir = filepath.Clean(dir)
	old := string(filepath.Separator) + r.oldFolder
	if len(dir) > len(old) && strings.EqualFold(dir[len(dir)-len(old):], old) {
		return dir[:len(dir)-len(old)] + string(filepath.Separator) + r.newFolder
	}
	return dir
}

// planSeasonRename works out every file to move and queue entry to change.
func (s *session) planSeasonRename(ctx context.Context, b store.BoxSet, show string, season int) (*seasonRename, error) {
	if other, ok, err := s.store.FindBoxSet(ctx, show, season); err != nil {
		return nil, err
	} else if ok && other.ID != b.ID {
		return nil, fmt.Errorf("%s season %d already has a box set; forget it first (blurip boxset forget) if it shouldn't", other.Show, other.Season)
	}
	r := &seasonRename{box: b, show: show, season: season, ops: &fixOps{outputs: map[int64]string{}},
		oldFolder: filepath.Join(b.Show, boxset.SeasonFolder(b.Season)),
		newFolder: filepath.Join(show, boxset.SeasonFolder(season))}

	// Each disc's rip folder: where its last successful rip went.
	discs, err := s.store.BoxSetDiscs(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	ripDirOf := map[int64]string{}
	for _, d := range discs {
		dir := filepath.Join(s.cfg.DestDir(makemkv.KindOf(d.Type)), r.oldFolder)
		if prev, ok, err := s.store.LastSuccess(ctx, d.DiscID); err != nil {
			return nil, err
		} else if ok && prev.OutputPath != "" {
			dir = prev.OutputPath
		}
		ripDirOf[d.DiscID] = dir
		if pair := [2]string{dir, r.movedDir(dir)}; pair[0] != pair[1] && !slices.Contains(r.dirs, pair) {
			r.dirs = append(r.dirs, pair)
		}
	}

	eps, err := s.store.BoxSetEpisodes(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	r.episodes = len(eps)
	newBase := func(ep int) string { return boxset.FileName(show, season, ep) }
	for _, e := range eps {
		ripDir := ripDirOf[e.DiscID]
		p := &fixPlan{box: b, ripDir: ripDir, folder: r.oldFolder}
		files, _, err := s.episodeFiles(ctx, p, e.Episode, s.episodeDirs(p))
		if err != nil {
			return nil, err
		}
		ob, nb := p.base(e.Episode), newBase(e.Episode)
		moved := map[string]string{}
		for _, f := range files {
			dst := filepath.Join(r.movedDir(filepath.Dir(f)), nb+strings.TrimPrefix(filepath.Base(f), ob))
			r.ops.moves = append(r.ops.moves, [2]string{f, dst})
			moved[f] = dst
			if d := filepath.Dir(f); !slices.Contains(r.leaveEmpty, d) {
				r.leaveEmpty = append(r.leaveEmpty, d)
			}
		}
		es, err := s.store.EncodesForSource(ctx, p.ripPath(e.Episode))
		if err != nil {
			return nil, err
		}
		for _, en := range es {
			if en.Status == store.EncodeEncoding {
				return nil, fmt.Errorf("%s is being encoded; try again when it has finished", en.Title)
			}
			en.SourcePath = filepath.Join(r.movedDir(ripDir), nb+".mkv")
			if en.Title == ob {
				en.Title = nb
			}
			destDir := filepath.Dir(en.DestName)
			if strings.EqualFold(filepath.Clean(destDir), r.oldFolder) {
				destDir = r.newFolder
			}
			en.DestName = filepath.Join(destDir, nb)
			r.ops.encodes = append(r.ops.encodes, en)
			if dst, ok := moved[en.OutputPath]; ok {
				r.ops.outputs[en.ID] = dst
			}
		}
	}

	// Nothing may be in the way (a file already there that isn't moving).
	leaving, taken := map[string]bool{}, map[string]bool{}
	for _, m := range r.ops.moves {
		leaving[m[0]] = true
	}
	for _, m := range r.ops.moves {
		if taken[m[1]] {
			return nil, fmt.Errorf("two files would be renamed to %s", m[1])
		}
		taken[m[1]] = true
		if _, err := os.Lstat(m[1]); err == nil && !leaving[m[1]] {
			return nil, fmt.Errorf("%s is in the way", m[1])
		}
	}
	return r, nil
}

// printSeasonRename shows what the rename will do.
func (s *session) printSeasonRename(r *seasonRename) {
	fmt.Fprintf(s.out, "\nThe rename: %s season %d → %s season %d\n", r.box.Show, r.box.Season, r.show, r.season)
	fmt.Fprintf(s.out, "    %d episodes: %s - S%02dE… become %s - S%02dE…\n", r.episodes, r.box.Show, r.box.Season, r.show, r.season)
	fmt.Fprintf(s.out, "    %d files renamed (rips, encoded files, subtitles), %d queue entries updated\n",
		len(r.ops.moves), len(r.ops.encodes))
	var dirs [][2]string
	for _, m := range r.ops.moves {
		pair := [2]string{filepath.Dir(m[0]), filepath.Dir(m[1])}
		if pair[0] != pair[1] && !slices.Contains(dirs, pair) {
			dirs = append(dirs, pair)
		}
	}
	for _, d := range dirs {
		fmt.Fprintf(s.out, "    %s → %s\n", d[0], d[1])
	}
	fmt.Fprintln(s.out)
}

// applySeasonRename moves the files, then updates the queue and the box
// set, holding the box set lock. why says why it couldn't; err is a
// database error.
func (s *session) applySeasonRename(ctx context.Context, r *seasonRename) (why string, err error) {
	bs := s.boxSet
	// Not Lock: in box-set mode this runs holding the question lock, and
	// a disc being numbered holds mu while waiting for that lock.
	if !bs.mu.TryLock() {
		return "another disc is being numbered right now; try again in a moment (nothing was changed)", nil
	}
	defer bs.mu.Unlock()
	if bs.activeDiscs(r.oldFolder) > 0 {
		return "a disc of this season is being ripped; rename it when that has finished (nothing was changed)", nil
	}
	for _, m := range r.ops.moves {
		if err := os.MkdirAll(filepath.Dir(m[1]), 0o755); err != nil {
			return fmt.Sprintf("creating %s: %v (nothing was changed)", filepath.Dir(m[1]), err), nil
		}
	}
	if err := r.ops.apply(); err != nil {
		return err.Error(), nil
	}
	for _, e := range r.ops.encodes {
		if err := s.store.UpdateEncode(ctx, e); err != nil {
			return "", err
		}
		if out, ok := r.ops.outputs[e.ID]; ok {
			if err := s.store.SetEncodeOutput(ctx, e.ID, out); err != nil {
				return "", err
			}
		}
	}
	if err := s.store.RenameBoxSet(ctx, r.box.ID, r.show, r.season, r.dirs, s.now()); err != nil {
		if errors.Is(err, store.ErrBoxSetExists) {
			return "", fmt.Errorf("%w (the files were already renamed)", err)
		}
		return "", err
	}

	// This run carries on under the new name.
	old := series{r.box.Show, r.box.Season}
	if _, ok := bs.ripping[old.key()]; ok {
		delete(bs.ripping, old.key())
		bs.ripping[series{r.show, r.season}.key()] = series{r.show, r.season}
	}
	if bs.current != nil && bs.current.key() == old.key() {
		bs.current = &series{r.show, r.season}
	}

	// The old folders go if nothing else is in them (and the show's
	// folder too, if that was its only season).
	for _, d := range r.leaveEmpty {
		if os.Remove(d) == nil {
			os.Remove(filepath.Dir(d))
		} else if _, err := os.Stat(d); err == nil {
			fmt.Fprintf(s.out, "%s still has other files in it, so it was left.\n", d)
		}
	}
	return "", nil
}

// askSeasonRename asks for the box set's right show name and season,
// shows the plan and, if you say so, does it. done says what it did ("" =
// it wasn't renamed).
func (s *session) askSeasonRename(ctx context.Context, b store.BoxSet) (done string, err error) {
	show, err := s.ask.String("Show name", b.Show)
	if err != nil {
		return "", err
	}
	show = strings.TrimSpace(show)
	if show == "" || strings.ContainsAny(show, `/\`) || show == "." || show == ".." {
		fmt.Fprintf(s.out, "%q can't be a show's name (it's used as a folder name).\n", show)
		return "", nil
	}
	season, err := s.ask.Int("Season", b.Season, 0, 999)
	if err != nil {
		return "", err
	}
	if show == b.Show && season == b.Season {
		fmt.Fprintln(s.out, "That's how it is already: nothing to change.")
		return "", nil
	}
	r, err := s.planSeasonRename(ctx, b, show, season)
	if err != nil {
		fmt.Fprintf(s.out, "Can't rename it: %v\n", err)
		return "", nil
	}
	s.printSeasonRename(r)
	if ok, err := s.ask.YesNo("Go ahead?", false); err != nil || !ok {
		if err == nil {
			fmt.Fprintln(s.out, "Nothing was changed.")
		}
		return "", err
	}
	why, err := s.applySeasonRename(ctx, r)
	if err != nil {
		return "", err
	}
	if why != "" {
		fmt.Fprintf(s.out, "Couldn't rename it: %s\n", why)
		return "", nil
	}
	return fmt.Sprintf("✓ %s season %d renamed %s season %d (%d files)", b.Show, b.Season, show, season, len(r.ops.moves)), nil
}
